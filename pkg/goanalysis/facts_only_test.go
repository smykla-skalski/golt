package goanalysis

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
)

func TestMarkFactsOnlyIR(t *testing.T) {
	buildir := &analysis.Analyzer{Name: buildirName}
	purity := &analysis.Analyzer{Name: "fact_purity", Requires: []*analysis.Analyzer{buildir}}
	nilness := &analysis.Analyzer{Name: "nilness", Requires: []*analysis.Analyzer{buildir}}
	printf := &analysis.Analyzer{Name: "printf"}
	other := &analysis.Analyzer{Name: "other", Requires: []*analysis.Analyzer{purity}}

	testCases := []struct {
		desc      string
		initial   bool
		analyzers []*analysis.Analyzer
		want      bool
	}{
		{desc: "dependency with known IR users", analyzers: []*analysis.Analyzer{buildir, purity, nilness, printf}, want: true},
		{desc: "dependency with another IR user", analyzers: []*analysis.Analyzer{buildir, purity, other}},
		{desc: "initial package", initial: true, analyzers: []*analysis.Analyzer{buildir, purity}},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			lp := &loadingPackage{isInitial: test.initial}
			var ir *action
			for _, a := range test.analyzers {
				act := &action{Analyzer: a}
				if a == buildir {
					ir = act
				}
				lp.actions = append(lp.actions, act)
			}

			lp.markFactsOnlyIR()

			assert.Equal(t, test.want, ir.factsOnlyIR)
		})
	}
}
