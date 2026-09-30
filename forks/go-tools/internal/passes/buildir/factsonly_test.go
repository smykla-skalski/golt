package buildir_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"honnef.co/go/tools/analysis/driver"
	"honnef.co/go/tools/analysis/facts/nilness"
	"honnef.co/go/tools/analysis/facts/purity"
	"honnef.co/go/tools/internal/passes/buildir"
	"honnef.co/go/tools/staticcheck/sa5012"
)

type discardT struct{}

func (discardT) Errorf(string, ...any) {}

// TestFactsOnlyKeepsFacts checks that building only the bodies
// FactsNeedBody selects leaves the facts of go-tools' fact analyzers intact.
func TestFactsOnlyKeepsFacts(t *testing.T) {
	cases := []struct {
		analyzer *analysis.Analyzer
		dir      string
		pkgs     []string
	}{
		{purity.Analyzer, "../../../analysis/facts/purity/testdata", []string{"example.com/Purity"}},
		{nilness.Analysis, "../../../analysis/facts/nilness/testdata", []string{"example.com/..."}},
		{sa5012.Analyzer, "testdata", []string{"factsonly/evens"}},
	}

	for _, c := range cases {
		t.Run(c.analyzer.Name, func(t *testing.T) {
			dir, err := filepath.Abs(c.dir)
			if err != nil {
				t.Fatal(err)
			}

			facts := func(factsOnly bool) []string {
				driver.FactsOnly = func(*analysis.Pass) bool { return factsOnly }
				defer func() { driver.FactsOnly = nil }()

				var out []string
				for _, res := range analysistest.Run(discardT{}, dir, c.analyzer, c.pkgs...) {
					for obj, fs := range res.Facts {
						for _, f := range fs {
							out = append(out, fmt.Sprintf("%s %s: %s", res.Pass.Fset.Position(obj.Pos()), obj, f))
						}
					}
				}
				slices.Sort(out)

				return out
			}

			full, filtered := facts(false), facts(true)
			if len(full) == 0 {
				t.Fatal("no facts in testdata")
			}
			if !slices.Equal(full, filtered) {
				t.Errorf("facts differ with bodies filtered:\nfull:\n%v\nfiltered:\n%v", full, filtered)
			}
		})
	}
}

func TestFactsNeedBody(t *testing.T) {
	const src = `package p

type Msg struct{ name string }

type Pairs []string

type Point struct{ X, Y int }

func (m *Msg) GetName() string { return m.name }
func (m *Msg) Reset()          {}
func (m *Msg) Clone() *Msg     { return m }
func (p Pairs) Check()         {}
func Add(a, b int) int          { return a + b }
func Dist(p Point) int          { return p.X }
func Load(path string) error    { return nil }
func Set(kv ...string)          {}
func Log(msg string)            {}
func Name(m Msg) string         { return m.name }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("p", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"(*p.Msg).GetName": false,
		"(*p.Msg).Reset":   false,
		"(*p.Msg).Clone":   true,
		"(p.Pairs).Check":  true,
		"p.Add":            true,
		"p.Dist":           true,
		"p.Load":           true,
		"p.Set":            true,
		"p.Log":            false,
		"p.Name":           true,
	}

	check := func(fn *types.Func) {
		if got := buildir.FactsNeedBody(fn.Signature()); got != want[fn.FullName()] {
			t.Errorf("FactsNeedBody(%s) = %t, want %t", fn.FullName(), got, want[fn.FullName()])
		}
	}

	for _, name := range pkg.Scope().Names() {
		switch obj := pkg.Scope().Lookup(name).(type) {
		case *types.Func:
			check(obj)
		case *types.TypeName:
			if named, ok := obj.Type().(*types.Named); ok {
				for m := range named.Methods() {
					check(m)
				}
			}
		}
	}
}
