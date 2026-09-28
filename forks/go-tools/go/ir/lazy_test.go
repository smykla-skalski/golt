package ir_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"sync"
	"testing"

	"honnef.co/go/tools/go/ir"
	"honnef.co/go/tools/internal/xtools-internal/testenv"
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

// eagerObjects returns the objects that creation from type information
// gives members: package-level objects and methods of named types.
func eagerObjects(pkg *types.Package) (names map[string]bool, funcs []*types.Func, values []types.Object) {
	names = map[string]bool{"init": true, "init$guard": true}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		switch obj := obj.(type) {
		case *types.Builtin:
			continue
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
		default:
			names[name] = true
			values = append(values, obj)
		}
	}
	return names, funcs, values
}

func lookupBeforeMaterialize(t *testing.T, prog *ir.Program, pkg *types.Package, funcs []*types.Func, values []types.Object) map[*types.Func]*ir.Function {
	t.Helper()
	byObj := make(map[*types.Func]*ir.Function)
	for _, fn := range funcs {
		got := prog.FuncValue(fn)
		if got == nil || got.Object() != fn {
			t.Fatalf("%s: FuncValue(%v) = %v", pkg.Path(), fn, got)
		}
		if again := prog.FuncValue(fn); again != got {
			t.Fatalf("%s: FuncValue(%v) not stable", pkg.Path(), fn)
		}
		byObj[fn] = got
	}
	for _, obj := range values {
		if c, ok := obj.(*types.Const); ok && prog.ConstValue(c) == nil {
			t.Fatalf("%s: ConstValue(%v) = nil", pkg.Path(), obj)
		}
	}
	return byObj
}

func TestLazyDependencyMembers(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := ir.NewProgram(fset, 0)
	for _, pkg := range pkgs {
		prog.CreatePackage(pkg, nil, nil, true)
	}

	for _, pkg := range pkgs {
		t.Run(pkg.Path(), func(t *testing.T) {
			names, funcs, values := eagerObjects(pkg)
			byObj := lookupBeforeMaterialize(t, prog, pkg, funcs, values)

			irpkg := prog.ImportedPackage(pkg.Path())
			if len(irpkg.Members) != len(names) {
				t.Errorf("%d members, want %d", len(irpkg.Members), len(names))
			}
			for name := range names {
				if irpkg.Members[name] == nil {
					t.Errorf("missing member %s", name)
				}
			}
			for obj, mem := range byObj {
				if prog.FuncValue(obj) != mem {
					t.Errorf("%v changed identity after materialize", obj)
				}
				if obj.Signature().Recv() == nil && irpkg.Func(obj.Name()) != mem {
					t.Errorf("Func(%s) differs from FuncValue", obj.Name())
				}
			}
			withInit := len(funcs) + 1
			if len(irpkg.Functions) != withInit {
				t.Errorf("%d functions, want %d", len(irpkg.Functions), withInit)
			}
		})
	}
}

func TestLazyDependencyNonMembers(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := ir.NewProgram(fset, 0)
	for _, pkg := range pkgs {
		prog.CreatePackage(pkg, nil, nil, true)
	}
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

func TestLazyDependencyConcurrent(t *testing.T) {
	fset, pkgs := importLazyTestPkgs(t)
	prog := ir.NewProgram(fset, 0)
	var funcs []*types.Func
	for _, pkg := range pkgs {
		prog.CreatePackage(pkg, nil, nil, true)
		_, fns, _ := eagerObjects(pkg)
		funcs = append(funcs, fns...)
	}

	const workers = 8
	results := make([][]*ir.Function, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			got := make([]*ir.Function, len(funcs))
			for i := range funcs {
				interleaved := (i*(w+1) + w) % len(funcs)
				got[interleaved] = prog.FuncValue(funcs[interleaved])
			}
			if w == 0 {
				prog.AllPackages()
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

	prog := ir.NewProgram(fset, 0)
	prog.CreatePackage(pkg, nil, nil, true)
	named := pkg.Scope().Lookup("T").Type().(*types.Named)
	for i := range named.NumMethods() {
		method := named.Method(i)
		fn := prog.FuncValue(method)
		if fn == nil || fn.Pkg == nil {
			t.Fatalf("FuncValue(%v) = %v before materialize, want member of dep", method, fn)
		}
		prog.AllPackages()
		if again := prog.FuncValue(method); again != fn {
			t.Errorf("FuncValue(%v) changed identity after materialize", method)
		}
	}
}
