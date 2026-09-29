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
		{
			desc:   "defaults cap at half of memory",
			total:  16 * gib,
			want:   gcSettings{percent: goltGCPercent, limit: int64(8 * gib)},
			wantOK: true,
		},
		{
			desc:   "user GOMEMLIMIT is kept",
			env:    map[string]string{envGOMEMLIMIT: "2GiB"},
			total:  16 * gib,
			want:   gcSettings{percent: goltGCPercent, limit: -1},
			wantOK: true,
		},
		{desc: "user GOGC disables the policy", env: map[string]string{envGOGC: "100"}, total: 16 * gib},
		{desc: "GOLT_GC=default disables the policy", env: map[string]string{envGoltGC: "default"}, total: 16 * gib},
		{desc: "unknown memory disables the policy", totalErr: errors.New("unsupported")},
		{desc: "zero memory disables the policy"},
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
