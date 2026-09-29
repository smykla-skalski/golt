package main

import (
	"errors"
	"testing"
)

func TestGCPolicy(t *testing.T) {
	const gib = uint64(1 << 30)

	tests := []struct {
		desc     string
		env      map[string]string
		total    uint64
		totalErr error
		want     gcSettings
		wantOK   bool
	}{
		{desc: "disabled by default", total: 16 * gib},
		{desc: "unknown GOLT_GC value is disabled", env: map[string]string{envGoltGC: "default"}, total: 16 * gib},
		{
			desc:   "fast caps at half of memory",
			env:    map[string]string{envGoltGC: goltGCFast},
			total:  16 * gib,
			want:   gcSettings{percent: goltGCPercent, limit: int64(8 * gib)},
			wantOK: true,
		},
		{
			desc:   "user GOMEMLIMIT is kept",
			env:    map[string]string{envGoltGC: goltGCFast, envGOMEMLIMIT: "2GiB"},
			total:  16 * gib,
			want:   gcSettings{percent: goltGCPercent, limit: -1},
			wantOK: true,
		},
		{desc: "user GOGC disables the policy", env: map[string]string{envGoltGC: goltGCFast, envGOGC: "100"}, total: 16 * gib},
		{desc: "unknown memory disables the policy", env: map[string]string{envGoltGC: goltGCFast}, totalErr: errors.New("unsupported")},
		{desc: "zero memory disables the policy", env: map[string]string{envGoltGC: goltGCFast}},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			getenv := func(key string) string { return test.env[key] }
			totalMemory := func() (uint64, error) { return test.total, test.totalErr }

			got, ok := gcPolicy(getenv, totalMemory)
			if ok != test.wantOK || got != test.want {
				t.Errorf("gcPolicy() = %+v, %t; want %+v, %t", got, ok, test.want, test.wantOK)
			}
		})
	}
}
