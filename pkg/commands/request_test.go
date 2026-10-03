package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/config"
)

type safeBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestSerialRunWaitsAndReportsProgress(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	first := &runCommand{cfg: &config.Config{}, cmd: &cobra.Command{}, debugf: func(string, ...any) {}}
	first.cmd.SetContext(t.Context())
	first.cfg.Run.AllowSerialRunners = true
	require.NoError(t, first.acquireFileLock())
	t.Cleanup(first.releaseFileLock)

	second := &runCommand{cfg: &config.Config{}, cmd: &cobra.Command{}, debugf: func(string, ...any) {}}
	second.cmd.SetContext(t.Context())
	second.cfg.Run.AllowSerialRunners = true
	var output safeBuffer
	second.cmd.SetErr(&output)
	result := make(chan error, 1)
	go func() { result <- second.acquireFileLock() }()

	require.Eventually(t, func() bool { return output.String() != "" }, time.Second, time.Millisecond)
	assert.Contains(t, output.String(), "Waiting for another golangci-lint run")
	select {
	case err := <-result:
		t.Fatalf("second run acquired lock early: %v", err)
	default:
	}

	first.releaseFileLock()
	require.NoError(t, <-result)
	second.releaseFileLock()
	_, err := os.Stat(filepath.Join(os.TempDir(), "golangci-lint.lock"))
	require.NoError(t, err)
}

func TestRequestKeySupersedesOlderRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := &runCommand{cmd: &cobra.Command{}, requestKey: t.Name()}
	first.cmd.SetContext(t.Context())
	require.NoError(t, first.beginRequest())
	t.Cleanup(first.stopRequest)

	second := &runCommand{cmd: &cobra.Command{}, requestKey: t.Name()}
	second.cmd.SetContext(t.Context())
	require.NoError(t, second.beginRequest())
	t.Cleanup(second.stopRequest)

	require.Eventually(t, func() bool {
		return context.Cause(first.cmd.Context()) == errRequestSuperseded
	}, time.Second, 10*time.Millisecond)
	assert.NoError(t, second.cmd.Context().Err())
}

func TestSupersededRequestStopsWaitingForLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	owner := &runCommand{cfg: &config.Config{}, cmd: &cobra.Command{}, debugf: func(string, ...any) {}}
	owner.cmd.SetContext(t.Context())
	owner.cfg.Run.AllowSerialRunners = true
	require.NoError(t, owner.acquireFileLock())
	t.Cleanup(owner.releaseFileLock)

	older := &runCommand{
		cfg: &config.Config{}, cmd: &cobra.Command{},
		debugf: func(string, ...any) {}, requestKey: t.Name(),
	}
	older.cmd.SetContext(t.Context())
	older.cfg.Run.AllowSerialRunners = true
	older.cmd.SetErr(&safeBuffer{})
	require.NoError(t, older.beginRequest())
	t.Cleanup(older.stopRequest)
	result := make(chan error, 1)
	go func() { result <- older.acquireFileLock() }()

	newer := &runCommand{cmd: &cobra.Command{}, requestKey: t.Name()}
	newer.cmd.SetContext(t.Context())
	require.NoError(t, newer.beginRequest())
	t.Cleanup(newer.stopRequest)
	require.ErrorIs(t, <-result, errRequestSuperseded)
	assert.Nil(t, older.flock)
}
