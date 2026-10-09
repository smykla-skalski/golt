package commands

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	envAdmission         = "GOLT_ADMISSION"
	admissionSlots       = 3
	admissionLightWeight = 1
	admissionHeavyWeight = 2
	admissionRetry       = 50 * time.Millisecond
)

func (c *runCommand) acquireAdmission() error {
	stateDir, err := userStateDir()
	if err != nil {
		return err
	}

	weight := c.admissionWeight
	if weight == 0 {
		weight = admissionHeavyWeight
	}
	ctx := c.cmd.Context()
	ticker := time.NewTicker(admissionRetry)
	defer ticker.Stop()

	for {
		if err := admissionContextError(ctx); err != nil {
			return err
		}
		runLock, slots, err := tryAdmission(stateDir, weight)
		if err != nil {
			return err
		}
		if runLock != nil {
			c.flock = runLock
			c.admissionSlots = slots
			if err := admissionContextError(ctx); err != nil {
				c.releaseFileLock()
				return err
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return admissionContextError(ctx)
		case <-ticker.C:
		}
	}
}

func admissionContextError(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), errRequestSuperseded) {
		return errRequestSuperseded
	}
	return ctx.Err()
}

func tryAdmission(stateDir string, weight int) (runLock *flock.Flock, slots []*flock.Flock, err error) {
	gate := flock.New(filepath.Join(stateDir, "admission.lock"))
	locked, err := gate.TryLock()
	if err != nil || !locked {
		return nil, nil, err
	}
	defer func() {
		if unlockErr := gate.Unlock(); unlockErr != nil {
			if runLock != nil {
				err = errors.Join(err, runLock.Unlock(), releaseAdmissionSlots(slots))
				runLock, slots = nil, nil
			}
			err = errors.Join(err, fmt.Errorf("unlock admission gate: %w", unlockErr))
		}
	}()

	slots, err = tryReserveAdmissionSlots(stateDir, weight)
	if err != nil || len(slots) != weight {
		return nil, nil, err
	}

	runLock = flock.New(filepath.Join(stateDir, "run.lock"))
	locked, err = runLock.TryRLock()
	if err != nil || !locked {
		releaseErr := releaseAdmissionSlots(slots)
		return nil, nil, errors.Join(err, releaseErr)
	}
	return runLock, slots, nil
}

func tryReserveAdmissionSlots(stateDir string, weight int) ([]*flock.Flock, error) {
	slots := make([]*flock.Flock, 0, weight)
	for i := range admissionSlots {
		f := flock.New(filepath.Join(stateDir, fmt.Sprintf("admission-%d.lock", i)))
		locked, err := f.TryLock()
		if err != nil {
			return nil, errors.Join(err, releaseAdmissionSlots(slots))
		}
		if !locked {
			continue
		}
		slots = append(slots, f)
		if len(slots) == weight {
			return slots, nil
		}
	}
	return nil, releaseAdmissionSlots(slots)
}

func releaseAdmissionSlots(slots []*flock.Flock) error {
	var err error
	for _, slot := range slots {
		err = errors.Join(err, slot.Unlock())
	}
	return err
}
