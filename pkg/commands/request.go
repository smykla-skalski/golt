package commands

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

var errRequestSuperseded = errors.New("request superseded by a newer run")

const (
	requestDirMode      = 0o700
	requestFileMode     = 0o600
	requestTokenSize    = 16
	requestPollInterval = 100 * time.Millisecond
)

func (c *runCommand) beginRequest() error {
	if c.requestKey == "" {
		return nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	stateDir, err := userStateDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(stateDir, "requests")
	if err := os.MkdirAll(dir, requestDirMode); err != nil {
		return fmt.Errorf("create request directory: %w", err)
	}

	key := sha256.Sum256([]byte(wd + "\x00" + c.requestKey))
	path := filepath.Join(dir, hex.EncodeToString(key[:]))
	token := make([]byte, requestTokenSize)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("create request token: %w", err)
	}
	identity := hex.EncodeToString(token)
	if err := writeRequestIdentity(path, identity); err != nil {
		return err
	}

	ctx, cancel := context.WithCancelCause(c.cmd.Context())
	c.cmd.SetContext(ctx)
	stop := make(chan struct{})
	var once sync.Once
	c.stopRequest = func() {
		once.Do(func() {
			close(stop)
			cancel(nil)
		})
	}

	go func() {
		ticker := time.NewTicker(requestPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				current, err := readRequestIdentity(path)
				if err != nil {
					cancel(err)
					return
				}
				if current != identity {
					cancel(errRequestSuperseded)
					return
				}
			}
		}
	}()

	return nil
}

func userStateDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("get user cache directory: %w", err)
	}
	dir := filepath.Join(cacheDir, "golt")
	if err := os.MkdirAll(dir, requestDirMode); err != nil {
		return "", fmt.Errorf("create user state directory: %w", err)
	}
	return dir, nil
}

func writeRequestIdentity(path, identity string) (err error) {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock request: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()

	return os.WriteFile(path, []byte(identity), requestFileMode)
}

func readRequestIdentity(path string) (identity string, err error) {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return "", fmt.Errorf("lock request: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()

	data, err := os.ReadFile(path)
	return string(data), err
}
