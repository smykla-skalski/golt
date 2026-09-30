package goanalysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

func TestDepsFactsMode_computesFactsFor(t *testing.T) {
	deprecated := &analysis.Analyzer{Name: "fact_deprecated"}
	nilness := &analysis.Analyzer{Name: "nilness"}

	project := &packages.Package{Module: &packages.Module{Main: true}}
	dependency := &packages.Package{Module: &packages.Module{Path: "example.com/dep", Version: "v1.0.0"}}
	stdlib := &packages.Package{}

	testCases := []struct {
		mode depsFactsMode
		a    *analysis.Analyzer
		imp  *packages.Package
		want bool
	}{
		{mode: depsFactsFull, a: nilness, imp: dependency, want: true},
		{mode: depsFactsLight, a: deprecated, imp: dependency, want: true},
		{mode: depsFactsLight, a: nilness, imp: stdlib},
		{mode: depsFactsLight, a: nilness, imp: project, want: true},
		{mode: depsFactsProject, a: deprecated, imp: dependency},
		{mode: depsFactsProject, a: nilness, imp: project, want: true},
	}

	for _, test := range testCases {
		assert.Equal(t, test.want, test.mode.computesFactsFor(test.a, test.imp), "%q %s", test.mode, test.a.Name)
	}
}

func TestHasGoFixDirective(t *testing.T) {
	parse := func(src string) []*ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.ParseComments)
		require.NoError(t, err)

		return []*ast.File{f}
	}

	assert.True(t, hasGoFixDirective(parse("package p\n\n//go:fix inline\nfunc F() {}\n")))
	assert.False(t, hasGoFixDirective(parse("package p\n\n// go:fix inline is not a directive\nfunc F() {}\n")))
}
