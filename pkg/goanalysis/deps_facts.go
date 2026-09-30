package goanalysis

import (
	"go/ast"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// EnvDepsFacts selects which facts are computed for dependencies outside the
// main module. Both non-default modes trade findings for cold-cache speed:
//   - "light": only facts derived from declarations (deprecation notices and
//     //go:fix inline directives); dependencies are type-checked without
//     function bodies. Findings that need body-derived dependency facts
//     (printf wrappers, nilness, purity) can be missed.
//   - "project": no facts from outside the main module; dependencies load
//     from export data. SA1019 on dependency APIs is missed too.
const EnvDepsFacts = "GOLT_DEPS_FACTS"

type depsFactsMode string

const (
	depsFactsFull    depsFactsMode = ""
	depsFactsLight   depsFactsMode = "light"
	depsFactsProject depsFactsMode = "project"
)

// declarationFactAnalyzers produce facts from declarations only.
var declarationFactAnalyzers = map[string]bool{
	"fact_deprecated": true,
	"inline":          true,
}

func depsFactsModeFromEnv() depsFactsMode {
	switch mode := depsFactsMode(os.Getenv(EnvDepsFacts)); mode {
	case depsFactsLight, depsFactsProject:
		return mode
	default:
		return depsFactsFull
	}
}

// computesFactsFor reports whether analyzer a computes facts for imp.
func (m depsFactsMode) computesFactsFor(a *analysis.Analyzer, imp *packages.Package) bool {
	if isMainModulePackage(imp) {
		return true
	}

	switch m {
	case depsFactsProject:
		return false
	case depsFactsLight:
		return declarationFactAnalyzers[a.Name]
	default:
		return true
	}
}

func isMainModulePackage(pkg *packages.Package) bool {
	return pkg.Module != nil && pkg.Module.Main
}

// hasGoFixDirective reports whether a file declares //go:fix directives, whose
// inline analysis needs function bodies.
func hasGoFixDirective(files []*ast.File) bool {
	for _, f := range files {
		for _, group := range f.Comments {
			for _, c := range group.List {
				if strings.HasPrefix(c.Text, "//go:fix") {
					return true
				}
			}
		}
	}

	return false
}
