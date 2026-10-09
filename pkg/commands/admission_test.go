package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/config"
)

func TestAdmissionReservesWeightedSlots(t *testing.T) {
	dir := t.TempDir()
	heavy, heavySlots, err := tryAdmission(dir, admissionHeavyWeight)
	require.NoError(t, err)
	require.NotNil(t, heavy)
	t.Cleanup(func() {
		require.NoError(t, releaseAdmissionSlots(heavySlots))
		require.NoError(t, heavy.Unlock())
	})

	light, lightSlots, err := tryAdmission(dir, admissionLightWeight)
	require.NoError(t, err)
	require.NotNil(t, light)
	require.Len(t, lightSlots, 1)

	blocked, blockedSlots, err := tryAdmission(dir, admissionLightWeight)
	require.NoError(t, err)
	assert.Nil(t, blocked)
	assert.Empty(t, blockedSlots)

	require.NoError(t, releaseAdmissionSlots(lightSlots))
	require.NoError(t, light.Unlock())
	available, availableSlots, err := tryAdmission(dir, admissionLightWeight)
	require.NoError(t, err)
	require.NotNil(t, available)
	require.NoError(t, releaseAdmissionSlots(availableSlots))
	require.NoError(t, available.Unlock())
}

func TestAdmissionRespectsSerialRunner(t *testing.T) {
	dir := t.TempDir()
	serial := flock.New(filepath.Join(dir, "run.lock"))
	require.NoError(t, serial.Lock())
	t.Cleanup(func() { require.NoError(t, serial.Unlock()) })

	admitted, slots, err := tryAdmission(dir, admissionLightWeight)
	require.NoError(t, err)
	assert.Nil(t, admitted)
	assert.Empty(t, slots)
}

func TestAdmissionStopsWhenRequestIsCancelled(t *testing.T) {
	isolateUserCache(t)
	t.Setenv(envAdmission, "1")
	owner := &runCommand{
		cfg: &config.Config{}, cmd: &cobra.Command{},
		debugf: func(string, ...any) {}, admissionWeight: admissionHeavyWeight,
	}
	owner.cfg.Run.AllowSerialRunners = true
	owner.cmd.SetContext(t.Context())
	require.NoError(t, owner.acquireFileLock())
	t.Cleanup(owner.releaseFileLock)

	light := &runCommand{
		cfg: &config.Config{}, cmd: &cobra.Command{},
		debugf: func(string, ...any) {}, admissionWeight: admissionLightWeight,
	}
	light.cfg.Run.AllowSerialRunners = true
	light.cmd.SetContext(t.Context())
	require.NoError(t, light.acquireFileLock())
	t.Cleanup(light.releaseFileLock)

	ctx, cancel := context.WithCancelCause(t.Context())
	blocked := &runCommand{
		cfg: &config.Config{}, cmd: &cobra.Command{},
		debugf: func(string, ...any) {}, admissionWeight: admissionLightWeight,
	}
	blocked.cfg.Run.AllowSerialRunners = true
	blocked.cmd.SetContext(ctx)
	result := make(chan error, 1)
	go func() { result <- blocked.acquireFileLock() }()
	cancel(errRequestSuperseded)
	require.ErrorIs(t, <-result, errRequestSuperseded)
	assert.Nil(t, blocked.flock)
	assert.Empty(t, blocked.admissionSlots)
	assert.True(t, errors.Is(context.Cause(ctx), errRequestSuperseded))
}
