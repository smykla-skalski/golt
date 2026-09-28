package ssa_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"sync"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"golang.org/x/tools/internal/testenv"
)

var lazyTestPkgs = []string{"fmt", "net/http", "slices", "sync", "sync/atomic", "unsafe"}

func importLazyTestPkgs(t *testing.T) (*token.FileSet, []*types.Package) {
	t.Helper()
	testenv.NeedsGoBuild(t)

	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", nil)
	var pkgs []*types.Package
	for _, path := range lazyTestPkgs {
		pkg, err := imp.Import(path)
		if err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, pkg)
	}
	return fset, pkgs
}

func newLazyProgram(fset *token.FileSet, pkgs []*types.Package) *ssa.Program {
	prog := ssa.NewProgram(fset, 0)
	for _, pkg := range pkgs {
		prog.CreatePackage(pkg, nil, nil, true)
	}
	return prog
}

// eagerObjects returns the objects that creation from type information
// gives members: package-level objects and methods of named types.
func eagerObjects(pkg *types.Package) (names map[string]bool, funcs []*types.Func, consts []*types.Const) {
	names = map[string]bool{"init": true, "init$guard": true}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		switch obj := scope.Lookup(name).(type) {
		case *types.Builtin:
		case *types.TypeName:
			names[name] = true
			if named, ok := obj.Type().(*types.Named); ok {
				for i := range named.NumMethods() {
					funcs = append(funcs, named.Method(i))
				}
			}
		case *types.Func:
			names[name] = true
			funcs = append(funcs, obj)
		case *types.Const:
			names[name] = true
			consts = append(consts, obj)
		default:
			names[name] = true
		}
	}
	return names, funcs, consts
}

func TestLazyDependencyMembers(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := newLazyProgram(fset, pkgs)

	for _, pkg := range pkgs {
		t.Run(pkg.Path(), func(t *testing.T) {
			names, funcs, consts := eagerObjects(pkg)
			byObj := make(map[*types.Func]*ssa.Function)
			for _, obj := range funcs {
				fn := prog.FuncValue(obj)
				if fn == nil || fn.Object() != obj || fn.Pkg == nil {
					t.Fatalf("FuncValue(%v) = %v before materialize", obj, fn)
				}
				if prog.FuncValue(obj) != fn {
					t.Fatalf("FuncValue(%v) not stable", obj)
				}
				byObj[obj] = fn
			}
			for _, obj := range consts {
				if prog.ConstValue(obj) == nil {
					t.Fatalf("ConstValue(%v) = nil", obj)
				}
			}

			ssapkg := prog.ImportedPackage(pkg.Path())
			if len(ssapkg.Members) != len(names) {
				t.Errorf("%d members, want %d", len(ssapkg.Members), len(names))
			}
			for name := range names {
				if ssapkg.Members[name] == nil {
					t.Errorf("missing member %s", name)
				}
			}
			for obj, fn := range byObj {
				if prog.FuncValue(obj) != fn {
					t.Errorf("%v changed identity after materialize", obj)
				}
				if obj.Signature().Recv() == nil && ssapkg.Func(obj.Name()) != fn {
					t.Errorf("Func(%s) differs from FuncValue", obj.Name())
				}
			}
		})
	}
}

func TestLazyDependencyNonMembers(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := newLazyProgram(fset, pkgs)
	noSig := types.NewSignatureType(nil, nil, nil, nil, nil, false)

	t.Run("interface method", func(t *testing.T) {
		stringer := pkgs[0].Scope().Lookup("Stringer").Type().Underlying().(*types.Interface)
		if fn := prog.FuncValue(stringer.Method(0)); fn != nil {
			t.Errorf("FuncValue(fmt.Stringer.String) = %v, want nil", fn)
		}
	})
	t.Run("package not created", func(t *testing.T) {
		other := types.NewPackage("example.com/other", "other")
		if fn := prog.FuncValue(types.NewFunc(token.NoPos, other, "F", noSig)); fn != nil {
			t.Errorf("FuncValue(other.F) = %v, want nil", fn)
		}
	})
	t.Run("same name but not in scope", func(t *testing.T) {
		if fn := prog.FuncValue(types.NewFunc(token.NoPos, pkgs[0], "Println", noSig)); fn != nil {
			t.Errorf("FuncValue(fake fmt.Println) = %v, want nil", fn)
		}
	})
}

func TestLazyDependencyAliasReceiver(t *testing.T) {
	const src = `package dep

type T struct{}

type A = T

func (A) M()  {}
func (*A) N() {}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dep.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := new(types.Config).Check("dep", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}

	prog := newLazyProgram(fset, []*types.Package{pkg})
	named := pkg.Scope().Lookup("T").Type().(*types.Named)
	for i := range named.NumMethods() {
		method := named.Method(i)
		fn := prog.FuncValue(method)
		if fn == nil || fn.Pkg == nil {
			t.Fatalf("FuncValue(%v) = %v before materialize, want member of dep", method, fn)
		}
		prog.Package(pkg)
		if prog.FuncValue(method) != fn {
			t.Errorf("FuncValue(%v) changed identity after materialize", method)
		}
	}
}

func TestLazyDependencyAllFunctions(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	names := func(prog *ssa.Program) []string {
		var out []string
		for fn := range ssautil.AllFunctions(prog) {
			out = append(out, fn.String())
		}
		slices.Sort(out)
		return out
	}

	lazy := newLazyProgram(fset, pkgs)
	complete := newLazyProgram(fset, pkgs)
	for _, pkg := range pkgs {
		complete.Package(pkg)
	}
	if got, want := names(lazy), names(complete); !slices.Equal(got, want) {
		t.Errorf("AllFunctions on lazy program: %d functions, want %d", len(got), len(want))
	}
}

func TestLazyDependencyConcurrent(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := newLazyProgram(fset, pkgs)
	var funcs []*types.Func
	for _, pkg := range pkgs {
		_, fns, _ := eagerObjects(pkg)
		funcs = append(funcs, fns...)
	}

	const workers = 8
	results := make([][]*ssa.Function, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			got := make([]*ssa.Function, len(funcs))
			for i := range funcs {
				interleaved := (i*(w+1) + w) % len(funcs)
				got[interleaved] = prog.FuncValue(funcs[interleaved])
			}
			if w == 0 {
				for _, pkg := range pkgs {
					prog.Package(pkg)
				}
			}
			results[w] = got
		})
	}
	wg.Wait()

	for i, fn := range funcs {
		for w := 1; w < workers; w++ {
			if results[w][i] != results[0][i] {
				t.Fatalf("FuncValue(%v) differs between goroutines", fn)
			}
		}
	}
}
