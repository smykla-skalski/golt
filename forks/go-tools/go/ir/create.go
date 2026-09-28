// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ir

// This file implements the CREATE phase of IR construction.
// See builder.go for explanation.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"sync"

	"honnef.co/go/tools/internal/xtools-internal/versions"
)

// NewProgram returns a new IR Program.
//
// mode controls diagnostics and checking during IR construction.
//
// To construct an SSA program:
//
//   - Call NewProgram to create an empty Program.
//   - Call CreatePackage providing typed syntax for each package
//     you want to build, and call it with types but not
//     syntax for each of those package's direct dependencies.
//   - Call [Package.Build] on each syntax package you wish to build,
//     or [Program.Build] to build all of them.
//
// See the Example tests for simple examples.
func NewProgram(fset *token.FileSet, mode BuilderMode) *Program {
	return &Program{
		Fset:     fset,
		imported: make(map[string]*Package),
		packages: make(map[*types.Package]*Package),
		mode:     mode,
		canon:    newCanonizer(),
		ctxt:     types.NewContext(),
	}
}

// memberFromObject populates package pkg with a member for the
// typechecker object obj.
//
// For objects from Go source code, syntax is the associated syntax tree
// (for funcs and vars only) and goversion defines the appropriate
// interpretation; they will be used during the build phase.
func memberFromObject(pkg *Package, obj types.Object, syntax ast.Node, goversion string) {
	name := obj.Name()
	switch obj := obj.(type) {
	case *types.Builtin:
		if pkg.Pkg != types.Unsafe {
			panic("unexpected builtin object: " + obj.String())
		}

	case *types.TypeName:
		if name != "_" {
			pkg.Members[name] = &Type{
				object: obj,
				pkg:    pkg,
			}
		}

	case *types.Const:
		c := &NamedConst{
			object: obj,
			Value:  NewConst(obj.Val(), obj.Type(), syntax),
			pkg:    pkg,
		}
		pkg.values[obj] = c
		if name != "_" {
			pkg.Members[name] = c
		}

	case *types.Var:
		g := &Global{
			Pkg:    pkg,
			name:   name,
			object: obj,
			typ:    types.NewPointer(obj.Type()), // address
		}
		g.source = syntax
		pkg.values[obj] = g
		if name != "_" {
			pkg.Members[name] = g
		}

	case *types.Func:
		sig := obj.Type().(*types.Signature)
		if sig.Recv() == nil && name == "init" {
			pkg.ninit++
			name = fmt.Sprintf("init#%d", pkg.ninit)
		}
		fn := createFunction(pkg.Prog, obj, name, syntax, pkg.info, goversion)
		fn.Pkg = pkg
		pkg.created = append(pkg.created, fn)
		pkg.values[obj] = fn
		pkg.Functions = append(pkg.Functions, fn)
		if name != "_" && sig.Recv() == nil {
			pkg.Members[name] = fn // package-level function
		}

	default: // (incl. *types.Package)
		panic("unexpected Object type: " + obj.String())
	}
}

// createFunction creates a function or method. It supports both
// CreatePackage (with or without syntax) and the on-demand creation
// of methods in non-created packages based on their types.Func.
func createFunction(prog *Program, obj *types.Func, name string, syntax ast.Node, info *types.Info, goversion string) *Function {
	sig := obj.Type().(*types.Signature)

	/* declared function/method (from syntax or export data) */
	fn := &Function{
		name:           name,
		object:         obj,
		Signature:      sig,
		build:          (*builder).buildFromSyntax,
		info:           info,
		goversion:      goversion,
		pos:            obj.Pos(),
		syntax:         syntax,
		Pkg:            nil, // may be set by caller
		Prog:           prog,
		recvtypeparams: sig.RecvTypeParams(),
		typeparams:     sig.TypeParams(),
	}
	if syntax == nil {
		fn.Synthetic = "from type information"
		fn.build = (*builder).buildParamsOnly
	}
	if fn.hasTypeParams() {
		fn.generic = new(generic)
	}
	return fn
}

// membersFromDecl populates package pkg with members for each
// typechecker object (var, func, const or type) associated with the
// specified decl.
func membersFromDecl(pkg *Package, decl ast.Decl, goversion string) {
	switch decl := decl.(type) {
	case *ast.GenDecl: // import, const, type or var
		switch decl.Tok {
		case token.CONST:
			for _, spec := range decl.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					memberFromObject(pkg, pkg.info.Defs[id], nil, "")
				}
			}

		case token.VAR:
			for _, spec := range decl.Specs {
				for _, rhs := range spec.(*ast.ValueSpec).Values {
					pkg.initVersion[rhs] = goversion
				}
				for _, id := range spec.(*ast.ValueSpec).Names {
					memberFromObject(pkg, pkg.info.Defs[id], spec, goversion)
				}
			}

		case token.TYPE:
			for _, spec := range decl.Specs {
				id := spec.(*ast.TypeSpec).Name
				memberFromObject(pkg, pkg.info.Defs[id], nil, "")
			}
		}

	case *ast.FuncDecl:
		id := decl.Name
		memberFromObject(pkg, pkg.info.Defs[id], decl, goversion)
	}
}

// CreatePackage creates and returns an IR Package from the
// specified type-checked, error-free file ASTs, and populates its
// Members mapping.
//
// importable determines whether this package should be returned by a
// subsequent call to ImportedPackage(pkg.Path()).
//
// The real work of building IR form for each function is not done
// until a subsequent call to Package.Build.
func (prog *Program) CreatePackage(pkg *types.Package, files []*ast.File, info *types.Info, importable bool) *Package {
	if pkg == nil {
		panic("nil pkg") // otherwise pkg.Scope below returns types.Universe!
	}
	p := &Package{
		Prog:    prog,
		Members: make(map[string]Member),
		values:  make(map[types.Object]Member),
		Pkg:     pkg,
		syntax:  info != nil,
		// transient values (cleared after Package.Build)
		info:        info,
		files:       files,
		initVersion: make(map[ast.Expr]string),
	}

	/* synthesized package initializer */
	p.init = &Function{
		name:      "init",
		Signature: new(types.Signature),
		Synthetic: "package initializer",
		Pkg:       p,
		Prog:      prog,
		build:     (*builder).buildPackageInit,
		info:      p.info,
		goversion: "", // See Package.build for details.
	}
	p.Members[p.init.name] = p.init
	p.Functions = append(p.Functions, p.init)
	p.created = append(p.created, p.init)

	// Allocate all package members: vars, funcs, consts and types.
	if len(files) > 0 {
		// Go source package.
		for _, file := range files {
			goversion := versions.Lang(versions.FileVersion(p.info, file))
			for _, decl := range file.Decls {
				membersFromDecl(p, decl, goversion)
			}
		}
	} else {
		// GC-compiled binary package (or "unsafe"): no code, no positions.
		// Analyses reference few dependency members, so create them on
		// demand instead of allocating one per exported object.
		p.lazy.Store(true)
	}

	if prog.mode&BareInits == 0 {
		// Add initializer guard variable.
		initguard := &Global{
			Pkg:  p,
			name: "init$guard",
			typ:  types.NewPointer(tBool),
		}
		p.Members[initguard.Name()] = initguard
	}

	if prog.mode&GlobalDebug != 0 {
		p.SetDebugMode(true)
	}

	if prog.mode&PrintPackages != 0 {
		printMu.Lock()
		p.WriteTo(os.Stdout)
		printMu.Unlock()
	}

	if importable {
		prog.imported[p.Pkg.Path()] = p
	}
	prog.packages[p.Pkg] = p

	return p
}

// printMu serializes printing of Packages/Functions to stdout.
var printMu sync.Mutex

// AllPackages returns a new slice containing all packages created by
// prog.CreatePackage in unspecified order.
func (prog *Program) AllPackages() []*Package {
	pkgs := make([]*Package, 0, len(prog.packages))
	for _, pkg := range prog.packages {
		pkg.materialize()
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

// materialize creates every member of a package created without
// syntax, so that p.Members and p.Functions are complete. Members
// created earlier on demand keep their identity.
func (p *Package) materialize() {
	if !p.lazy.Load() {
		return
	}
	p.lazyMu.Lock()
	defer p.lazyMu.Unlock()
	if !p.lazy.Load() {
		return
	}
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		p.createLazyMember(obj)
		if obj, ok := obj.(*types.TypeName); ok {
			// No Unalias: aliases should not duplicate methods.
			if named, ok := obj.Type().(*types.Named); ok {
				for i, n := 0, named.NumMethods(); i < n; i++ {
					p.createLazyMember(named.Method(i))
				}
			}
		}
	}
	p.lazy.Store(false)
}

// lazyValue returns the member for obj, creating it if eager package
// creation would have. It returns nil otherwise.
func (p *Package) lazyValue(obj types.Object) Member {
	p.lazyMu.Lock()
	defer p.lazyMu.Unlock()
	if v, ok := p.values[obj]; ok || !p.lazy.Load() {
		return v
	}
	if !p.eagerMember(obj) {
		return nil
	}
	p.createLazyMember(obj)
	return p.values[obj]
}

// eagerMember reports whether obj is a package-level object or a
// method of a package-level named type of p, i.e. one that eager
// creation from type information would have given a member.
func (p *Package) eagerMember(obj types.Object) bool {
	scope := p.Pkg.Scope()
	fn, ok := obj.(*types.Func)
	if !ok || fn.Signature().Recv() == nil {
		return scope.Lookup(obj.Name()) == obj
	}
	recv := types.Unalias(fn.Signature().Recv().Type())
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = types.Unalias(ptr.Elem())
	}
	named, ok := recv.(*types.Named)
	if !ok {
		return false
	}
	tn := named.Obj()
	if scope.Lookup(tn.Name()) != tn {
		return false
	}
	origin, ok := tn.Type().(*types.Named)
	if !ok {
		return false
	}
	for i, n := 0, origin.NumMethods(); i < n; i++ {
		if origin.Method(i) == fn {
			return true
		}
	}
	return false
}

// createLazyMember creates the member for obj unless it exists.
// p.lazyMu must be held.
func (p *Package) createLazyMember(obj types.Object) {
	if _, ok := obj.(*types.TypeName); ok {
		if _, ok := p.Members[obj.Name()]; ok {
			return
		}
	} else if _, ok := p.values[obj]; ok {
		return
	}
	memberFromObject(p, obj, nil, "")
}

// ImportedPackage returns the importable Package whose PkgPath
// is path, or nil if no such Package has been created.
//
// A parameter to CreatePackage determines whether a package should be
// considered importable. For example, no import declaration can resolve
// to the ad-hoc main package created by 'go build foo.go'.
//
// TODO(adonovan): rethink this function and the "importable" concept;
// most packages are importable. This function assumes that all
// types.Package.Path values are unique within the ir.Program, which is
// false---yet this function remains very convenient.
// Clients should use (*Program).Package instead where possible.
// IR doesn't really need a string-keyed map of packages.
//
// Furthermore, the graph of packages may contain multiple variants
// (e.g. "p" vs "p as compiled for q.test"), and each has a different
// view of its dependencies.
func (prog *Program) ImportedPackage(path string) *Package {
	p := prog.imported[path]
	if p != nil {
		p.materialize()
	}
	return p
}

// SetNoReturn sets the predicate used when building the ir.Program
// prog that reports whether a given function cannot return.
// This may be used to prune spurious control flow edges
// after (e.g.) log.Fatal, improving the precision of analyses.
//
// A typical implementation is the [ctrlflow.CFGs.NoReturn] method from
// [golang.org/x/tools/go/analysis/passes/ctrlflow].
func (prog *Program) SetNoReturn(fn func(*types.Func) bool) {
	prog.noReturn = fn
}
