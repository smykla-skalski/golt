package goanalysis

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

type factVisibilityFact struct{}

func (*factVisibilityFact) AFact() {}

const factVisibilityC = `package c

type T struct{ Field int }

func (T) Method() {}

func (t T) unexportedMethod() {}

func Func() {}

func unexportedFunc() {}

var Var int

var unexportedVar int

func WithParam(param int) { _ = param }

const Const = 1
`

const factVisibilityB = `package b

import "example.com/c"

func Func() c.T { return c.T{} }

func unexportedFunc() {}
`

type factVisibilityGraph struct {
	fset    *token.FileSet
	runner  *runner
	actions map[string]*action
	pkgs    map[string]*types.Package
}

// newFactVisibilityGraph builds actions for a (imports b), b (imports c) and
// optionally a direct a -> c edge, with a fact on every object of b and c.
func newFactVisibilityGraph(t *testing.T, aImportsC bool) *factVisibilityGraph {
	t.Helper()

	g := &factVisibilityGraph{
		fset:    token.NewFileSet(),
		runner:  &runner{},
		actions: map[string]*action{},
		pkgs:    map[string]*types.Package{},
	}

	analyzer := &analysis.Analyzer{Name: "facts", FactTypes: []analysis.Fact{(*factVisibilityFact)(nil)}}

	check := func(path, src string) {
		file, err := parser.ParseFile(g.fset, path+".go", src, 0)
		require.NoError(t, err)

		conf := types.Config{Importer: importerFunc(func(p string) (*types.Package, error) {
			if pkg, ok := g.pkgs[p]; ok {
				return pkg, nil
			}

			return importer.Default().Import(p)
		})}

		pkg, err := conf.Check(path, g.fset, []*ast.File{file}, nil)
		require.NoError(t, err)

		g.pkgs[path] = pkg

		act := &action{
			Analyzer:     analyzer,
			Package:      &packages.Package{PkgPath: path, Types: pkg},
			runner:       g.runner,
			objectFacts:  map[objectFactKey]analysis.Fact{},
			packageFacts: map[packageFactKey]analysis.Fact{},
		}
		g.actions[path] = act

		for _, name := range pkg.Scope().Names() {
			obj := pkg.Scope().Lookup(name)
			act.objectFacts[objectFactKey{obj, act.factType(new(factVisibilityFact))}] = new(factVisibilityFact)

			if named, ok := obj.Type().(*types.Named); ok {
				for method := range named.Methods() {
					act.objectFacts[objectFactKey{method, act.factType(new(factVisibilityFact))}] = new(factVisibilityFact)
				}

				if st, ok := named.Underlying().(*types.Struct); ok {
					for field := range st.Fields() {
						act.objectFacts[objectFactKey{field, act.factType(new(factVisibilityFact))}] = new(factVisibilityFact)
					}
				}
			}
		}

		act.packageFacts[packageFactKey{pkg, act.factType(new(factVisibilityFact))}] = new(factVisibilityFact)
		act.registerFactOwner()
	}

	check("example.com/c", factVisibilityC)
	check("example.com/b", factVisibilityB)

	imports := `import "example.com/b"`
	if aImportsC {
		imports = "import (\n\t\"example.com/b\"\n\t\"example.com/c\"\n)\n\nvar _ c.T"
	}
	check("example.com/a", "package a\n\n"+imports+"\n\nvar _ = b.Func\n")

	g.actions["example.com/b"].Deps = []*action{g.actions["example.com/c"]}
	g.actions["example.com/a"].Deps = []*action{g.actions["example.com/b"]}
	if aImportsC {
		g.actions["example.com/a"].Deps = append(g.actions["example.com/a"].Deps, g.actions["example.com/c"])
	}

	return g
}

func (g *factVisibilityGraph) object(path, name string) types.Object {
	pkg := g.pkgs[path]

	owner, member, isMember := cutMember(name)
	obj := pkg.Scope().Lookup(owner)
	if !isMember {
		return obj
	}

	named := obj.Type().(*types.Named)
	for method := range named.Methods() {
		if method.Name() == member {
			return method
		}
	}

	st := named.Underlying().(*types.Struct)
	for field := range st.Fields() {
		if field.Name() == member {
			return field
		}
	}

	return nil
}

func cutMember(name string) (owner, member string, ok bool) {
	for i := range name {
		if name[i] == '.' {
			return name[:i], name[i+1:], true
		}
	}

	return name, "", false
}

// TestObjectFactVisibility pins the facts visible through the action of a
// dependency to what copying facts along import edges with exportedFrom kept.
func TestObjectFactVisibility(t *testing.T) {
	testCases := []struct {
		desc      string
		aImportsC bool
		visible   []string
		hidden    []string
	}{
		{
			desc: "indirect dependency",
			visible: []string{
				"example.com/b:Func",
				"example.com/c:T", "example.com/c:T.Method", "example.com/c:T.unexportedMethod", "example.com/c:T.Field",
				"example.com/c:Const",
			},
			hidden: []string{
				"example.com/b:unexportedFunc",
				"example.com/c:Func", "example.com/c:unexportedFunc", "example.com/c:Var", "example.com/c:unexportedVar",
			},
		},
		{
			desc:      "direct dependency",
			aImportsC: true,
			visible: []string{
				"example.com/b:Func",
				"example.com/c:T", "example.com/c:T.Method", "example.com/c:T.Field", "example.com/c:Const",
				"example.com/c:Func", "example.com/c:Var", "example.com/c:unexportedVar",
			},
			hidden: []string{"example.com/b:unexportedFunc", "example.com/c:unexportedFunc"},
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			g := newFactVisibilityGraph(t, test.aImportsC)
			act := g.actions["example.com/a"]

			lookup := func(ref string) (types.Object, bool) {
				path, name, _ := cutRef(ref)
				obj := g.object(path, name)
				require.NotNil(t, obj, ref)

				return obj, act.ObjectFact(obj, new(factVisibilityFact))
			}

			all := act.AllObjectFacts()
			inAll := func(obj types.Object) bool {
				return slices.ContainsFunc(all, func(f analysis.ObjectFact) bool { return f.Object == obj })
			}

			for _, ref := range test.visible {
				obj, ok := lookup(ref)
				assert.True(t, ok, ref)
				assert.True(t, inAll(obj), ref)
			}

			for _, ref := range test.hidden {
				obj, ok := lookup(ref)
				assert.False(t, ok, ref)
				assert.False(t, inAll(obj), ref)
			}

			for _, path := range []string{"example.com/b", "example.com/c"} {
				assert.True(t, act.PackageFact(g.pkgs[path], new(factVisibilityFact)), path)
			}

			assert.Len(t, act.AllPackageFacts(), 3)

			assert.ElementsMatch(t, copiedObjectFacts(act), objectsOf(all))
		})
	}
}

// copiedObjectFacts returns the objects whose facts act saw when facts were
// copied along import edges filtered by exportedFrom, before owner lookups.
func copiedObjectFacts(act *action) []types.Object {
	var inherited func(a *action) map[types.Object]bool
	inherited = func(a *action) map[types.Object]bool {
		visible := map[types.Object]bool{}
		for key := range a.objectFacts {
			visible[key.obj] = true
		}

		for _, dep := range a.Deps {
			for obj := range inherited(dep) {
				if exportedFrom(obj, dep.Package.Types) {
					visible[obj] = true
				}
			}
		}

		return visible
	}

	var objs []types.Object
	for obj := range inherited(act) {
		objs = append(objs, obj)
	}

	return objs
}

func objectsOf(facts []analysis.ObjectFact) []types.Object {
	objs := make([]types.Object, 0, len(facts))
	for _, fact := range facts {
		objs = append(objs, fact.Object)
	}

	return objs
}

func TestFactOwnerUnregister(t *testing.T) {
	g := newFactVisibilityGraph(t, false)
	act := g.actions["example.com/a"]
	b := g.actions["example.com/b"]

	obj := g.object("example.com/b", "Func")
	require.True(t, act.ObjectFact(obj, new(factVisibilityFact)))

	b.unregisterFactOwner(g.pkgs["example.com/b"])
	assert.False(t, act.ObjectFact(obj, new(factVisibilityFact)))
}

func cutRef(ref string) (path, name string, ok bool) {
	for i := range ref {
		if ref[i] == ':' {
			return ref[:i], ref[i+1:], true
		}
	}

	return ref, "", false
}
