package main

import (
	"math"
	"os"
	"runtime/debug"

	"github.com/shirou/gopsutil/v4/mem"
)

const (
	envGOGC       = "GOGC"
	envGOMEMLIMIT = "GOMEMLIMIT"
	envGoltGC     = "GOLT_GC"

	goltGCPercent      = 400
	goltMemoryFraction = 0.5
)

type gcSettings struct {
	percent int
	limit   int64
}

// gcPolicy returns the GC settings golt applies at startup, and false when the
// runtime defaults or the user's own GOGC/GOMEMLIMIT must be left alone.
//
// Analysis allocates heavily, so collection dominates at the default GOGC=100.
// golt raises the GC percent to trade idle memory for less collection work, and
// without an explicit GOMEMLIMIT caps the heap at half of physical memory so
// the runtime collects aggressively again before the machine runs short.
func gcPolicy(getenv func(string) string, totalMemory func() (uint64, error)) (gcSettings, bool) {
	if getenv(envGoltGC) == "default" || getenv(envGOGC) != "" {
		return gcSettings{}, false
	}

	settings := gcSettings{percent: goltGCPercent, limit: -1}
	if getenv(envGOMEMLIMIT) != "" {
		return settings, true
	}

	total, err := totalMemory()
	if err != nil || total == 0 {
		return gcSettings{}, false
	}

	limit := float64(total) * goltMemoryFraction
	if limit >= math.MaxInt64 {
		limit = math.MaxInt64
	}
	settings.limit = int64(limit)

	return settings, true
}

func configureGC() {
	settings, ok := gcPolicy(os.Getenv, func() (uint64, error) {
		stat, err := mem.VirtualMemory()
		if err != nil {
			return 0, err
		}
		return stat.Total, nil
	})
	if !ok {
		return
	}

	if settings.limit >= 0 {
		debug.SetMemoryLimit(settings.limit)
	}
	debug.SetGCPercent(settings.percent)
}
