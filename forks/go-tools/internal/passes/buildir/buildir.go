// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package buildir defines an Analyzer that constructs the IR
// of an error-free package and returns the set of all
// functions within it. It does not report any diagnostics itself but
// may be used as an input to other analyzers.
//
// THIS INTERFACE IS EXPERIMENTAL AND MAY BE SUBJECT TO INCOMPATIBLE CHANGE.
package buildir

import (
	"go/types"
	"reflect"

	"honnef.co/go/tools/analysis/driver"
	"honnef.co/go/tools/go/ir"
	"honnef.co/go/tools/go/types/typeutil"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
)

var Debug = struct {
	Mode ir.BuilderMode
}{}

var Analyzer = &analysis.Analyzer{
	Name:       "buildir",
	Doc:        "build IR for later passes",
	Run:        run,
	ResultType: reflect.TypeFor[*IR](),
	Requires:   []*analysis.Analyzer{ctrlflow.Analyzer},
}

// IR provides intermediate representation for all the
// source functions in the current package.
type IR struct {
	Pkg      *ir.Package
	SrcFuncs []*ir.Function
}

func run(pass *analysis.Pass) (any, error) {
	cfgs := pass.ResultOf[ctrlflow.Analyzer].(*ctrlflow.CFGs)

	// Plundered from ssautil.BuildPackage.

	// We must create a new Program for each Package because the
	// analysis API provides no place to hang a Program shared by
	// all Packages. Consequently, IR Packages and Functions do not
	// have a canonical representation across an analysis session of
	// multiple packages. This is unlikely to be a problem in
	// practice because the analysis API essentially forces all
	// packages to be analysed independently, so any given call to
	// Analysis.Run on a package will see only IR objects belonging
	// to a single Program.

	factsOnly := driver.FactsOnly != nil && driver.FactsOnly(pass)

	mode := ir.GlobalDebug
	if factsOnly {
		// Debug references map values back to source expressions for
		// diagnostics, which facts-only passes discard.
		mode = 0
	}
	if Debug.Mode != 0 {
		mode = Debug.Mode
	}

	prog := ir.NewProgram(pass.Fset, mode)

	prog.SetNoReturn(cfgs.NoReturn)

	if factsOnly {
		prog.SetBodyFilter(func(fn *ir.Function) bool {
			return !FactsNeedBody(fn.Signature)
		})
	}

	// Create IR packages for direct imports.
	for _, p := range pass.Pkg.Imports() {
		prog.CreatePackage(p, nil, nil, true)
	}

	// Create and build the primary package.
	irpkg := prog.CreatePackage(pass.Pkg, pass.Files, pass.TypesInfo, false)
	irpkg.Build()

	// Compute list of source functions, including literals,
	// in source order.
	var addAnons func(f *ir.Function)
	funcs := make([]*ir.Function, len(irpkg.Functions))
	copy(funcs, irpkg.Functions)
	addAnons = func(f *ir.Function) {
		for _, anon := range f.AnonFuncs {
			funcs = append(funcs, anon)
			addAnons(anon)
		}
	}
	for _, fn := range irpkg.Functions {
		addAnons(fn)
	}

	return &IR{Pkg: irpkg, SrcFuncs: funcs}, nil
}

// FactsNeedBody reports whether a go-tools fact analyzer can derive a fact
// from the body of a function with signature sig:
//   - nilness: a result that may be pointer-like;
//   - purity: results, and only basic-typed parameters and receiver (its
//     stdlib allowlist is checked without the body);
//   - SA5012: a slice-typed parameter or receiver.
//
// Keep in sync with those analyzers; their tests run with bodies filtered.
func FactsNeedBody(sig *types.Signature) bool {
	params := sig.Params()
	recv := sig.Recv()

	isSlice := func(t types.Type) bool { return typeutil.All(t, typeutil.IsSlice) }
	if recv != nil && isSlice(recv.Type()) {
		return true
	}
	for p := range params.Variables() {
		if isSlice(p.Type()) {
			return true
		}
	}

	results := sig.Results()
	if results.Len() == 0 {
		return false
	}
	for r := range results.Variables() {
		if typeutil.MaybePointerLike(r.Type()) {
			return true
		}
	}

	if recv != nil && !isBasic(recv.Type()) {
		return false
	}
	for p := range params.Variables() {
		if !isBasic(p.Type()) {
			return false
		}
	}

	return true
}

// isBasic matches purity's notion of a basic type.
func isBasic(typ types.Type) bool {
	switch u := typ.Underlying().(type) {
	case *types.Basic:
		return true
	case *types.Struct:
		for field := range u.Fields() {
			if !isBasic(field.Type()) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
