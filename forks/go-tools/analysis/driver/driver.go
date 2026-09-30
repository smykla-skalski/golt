// Package driver lets analysis drivers tell go-tools analyzers how their
// results will be used.
package driver

import "golang.org/x/tools/go/analysis"

// FactsOnly, when set by a driver, reports whether a pass analyzes a package
// only so that fact analyzers can export facts for its importers: its
// diagnostics are discarded and only go-tools' fact analyzers use its IR.
// buildir then builds only the function bodies those fact analyzers can use.
var FactsOnly func(pass *analysis.Pass) bool
