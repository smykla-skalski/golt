package goanalysis

import (
	"context"
	"runtime/pprof"
	"sync/atomic"
)

var profileLabels atomic.Bool

// EnableProfileLabels tags CPU profile samples with the package kind
// (initial or dependency), the phase and the analyzer, so profiles attribute
// work that shared stacks (lazy IR, type checking) otherwise hide.
func EnableProfileLabels() {
	profileLabels.Store(true)
}

func withProfileLabels(kind, phase, analyzer string, f func()) {
	if !profileLabels.Load() {
		f()
		return
	}

	pprof.Do(context.Background(), pprof.Labels("golt_kind", kind, "golt_phase", phase, "golt_analyzer", analyzer), func(context.Context) {
		f()
	})
}

func packageKind(initial bool) string {
	if initial {
		return "initial"
	}

	return "dependency"
}
