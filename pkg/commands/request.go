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

func (c *runCommand) beginRequest() error {
	if c.requestKey == "" {
		return nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("get cache directory: %w", err)
	}
	dir := filepath.Join(cacheDir, "golt", "requests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create request directory: %w", err)
	}

	key := sha256.Sum256([]byte(wd + "\x00" + c.requestKey))
	path := filepath.Join(dir, hex.EncodeToString(key[:]))
	token := make([]byte, 16)
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
		ticker := time.NewTicker(100 * time.Millisecond)
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

func writeRequestIdentity(path, identity string) error {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock request: %w", err)
	}
	defer lock.Unlock()

	return os.WriteFile(path, []byte(identity), 0o600)
}

func readRequestIdentity(path string) (string, error) {
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return "", fmt.Errorf("lock request: %w", err)
	}
	defer lock.Unlock()

	data, err := os.ReadFile(path)
	return string(data), err
}
