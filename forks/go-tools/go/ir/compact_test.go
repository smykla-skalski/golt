package ir_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"testing"

	"honnef.co/go/tools/go/ir"
	"honnef.co/go/tools/internal/xtools-internal/graph"
)

func TestFunctionIsCompactGraph(t *testing.T) {
	const src = `package p

func f(x int) int {
	for i := 0; i < x; i++ {
		if i%2 == 0 {
			x += i
		}
	}
	return x
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
		Instances:  map[*ast.Ident]types.Instance{},
	}
	pkg, err := new(types.Config).Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	prog := ir.NewProgram(fset, ir.SanityCheckFunctions)
	irpkg := prog.CreatePackage(pkg, []*ast.File{file}, info, false)
	irpkg.Build()
	fn := irpkg.Func("f")

	cg, index := graph.Compact[int](fn)
	if cg != graph.CompactGraph(fn) {
		t.Fatal("graph.Compact wrapped *ir.Function; want it used directly")
	}
	for i, b := range fn.Blocks {
		if index.Value(i) != i || index.Index(i) != i {
			t.Fatalf("index maps block %d to %d/%d", i, index.Value(i), index.Index(i))
		}
		var want []int
		for _, succ := range b.Succs {
			want = append(want, succ.Index)
		}
		if got := slices.Collect(cg.Out(i)); !slices.Equal(got, want) {
			t.Errorf("Out(%d) = %v, want %v", i, got, want)
		}
	}
}
