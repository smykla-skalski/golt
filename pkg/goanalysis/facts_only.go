package goanalysis

import (
	"sync"

	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/analysis/driver"
)

// factsOnlyIRUsers are the go-tools fact analyzers whose needs
// buildir.FactsNeedBody encodes.
var factsOnlyIRUsers = map[string]bool{
	"fact_purity": true,
	"nilness":     true,
	"SA5012":      true,
}

const buildirName = "buildir"

var factsOnlyPasses sync.Map // *analysis.Pass -> struct{}

func isFactsOnlyPass(pass *analysis.Pass) bool {
	_, ok := factsOnlyPasses.Load(pass)
	return ok
}

func installFactsOnlyHook() {
	driver.FactsOnly = isFactsOnlyPass
}

// markFactsOnlyIR lets buildir skip function bodies no fact analyzer can use
// on dependencies: their diagnostics are discarded, so the IR only feeds the
// fact analyzers. It is limited to packages where every IR user is one of the
// fact analyzers whose needs buildir knows.
func (lp *loadingPackage) markFactsOnlyIR() {
	if lp.isInitial {
		return
	}

	var builders []*action

	for _, act := range lp.actions {
		if act.Analyzer.Name == buildirName {
			builders = append(builders, act)
			continue
		}

		if requiresAnalyzer(act.Analyzer, buildirName) && !factsOnlyIRUsers[act.Analyzer.Name] {
			return
		}
	}

	for _, act := range builders {
		act.factsOnlyIR = true
	}
}

func requiresAnalyzer(a *analysis.Analyzer, name string) bool {
	for _, req := range a.Requires {
		if req.Name == name || requiresAnalyzer(req, name) {
			return true
		}
	}

	return false
}
