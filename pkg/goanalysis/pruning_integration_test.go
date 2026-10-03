package goanalysis

import (
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/internal/cache"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

func TestPrunedCacheMergesDiagnosticsAndWritesFullCache(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/pruning\n\ngo 1.26.0\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "dep"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "user"), 0o700))
	depFile := filepath.Join(dir, "dep", "dep.go")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "user", "user.go"),
		[]byte("package user\nimport \"example.com/pruning/dep\"\nvar Value = dep.Value()\n"), 0o600))
	writeDep := func(body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(depFile, []byte("package dep\n//go:noinline\n"+body), 0o600))
	}
	writeDep("func Value() int { return 1 }\n")

	run := func() ([]string, [3]int32) {
		t.Helper()
		pkgs, err := packages.Load(&packages.Config{
			Dir: dir,
			Mode: packages.NeedName | packages.NeedModule | packages.NeedCompiledGoFiles |
				packages.NeedImports | packages.NeedDeps | packages.NeedExportFile |
				packages.NeedTypesSizes,
		}, "./user")
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
		require.Empty(t, pkgs[0].Errors)
		fset := token.NewFileSet()
		seen := map[*packages.Package]bool{}
		var setFileSet func(*packages.Package)
		setFileSet = func(pkg *packages.Package) {
			if seen[pkg] {
				return
			}
			seen[pkg] = true
			pkg.Fset = fset
			for _, imported := range pkg.Imports {
				setFileSet(imported)
			}
		}
		setFileSet(pkgs[0])

		var counts [3]atomic.Int32
		prunable := NewLinterFromAnalyzer(&analysis.Analyzer{
			Name: "prunable",
			Doc:  "Checks prunable diagnostics",
			Run: func(pass *analysis.Pass) (any, error) {
				counts[0].Add(1)
				pass.Report(analysis.Diagnostic{Pos: pass.Files[0].Pos(), Message: "prunable issue"})
				return nil, nil
			},
		}).WithLoadMode(LoadModeSyntax)
		var reported []*Issue
		reporter := NewLinterFromAnalyzer(&analysis.Analyzer{
			Name: "reporter",
			Doc:  "Checks reporter diagnostics",
			Run: func(pass *analysis.Pass) (any, error) {
				counts[1].Add(1)
				reported = append(reported, NewIssue(&result.Issue{
					FromLinter: "reporter", Text: "reported issue",
					Pos: pass.Fset.Position(pass.Files[0].Pos()),
				}, pass))
				return nil, nil
			},
		}).WithIssuesReporter(func(*linter.Context) []*Issue { return reported }).
			WithCacheableIssuesReporter().WithLoadMode(LoadModeSyntax)
		factful := NewLinterFromAnalyzer(&analysis.Analyzer{
			Name: "factful", Doc: "Checks factful diagnostics", FactTypes: []analysis.Fact{new(testPruningFact)},
			Run: func(pass *analysis.Pass) (any, error) {
				if pass.Pkg.Path() == "example.com/pruning/user" {
					counts[2].Add(1)
					pass.Report(analysis.Diagnostic{Pos: pass.Files[0].Pos(), Message: "factful issue"})
				}
				pass.ExportPackageFact(new(testPruningFact))
				return nil, nil
			},
		}).WithLoadMode(LoadModeSyntax)
		logger := logutils.NewStderrLog("")
		pkgCache, err := cache.NewCache(timeutils.NewStopwatch("cache", logger), logger)
		require.NoError(t, err)
		issues, err := NewMetaLinter([]*Linter{prunable, reporter, factful}).Run(t.Context(), &linter.Context{
			Packages: pkgs, OriginalPackages: pkgs, Log: logger,
			PkgCache: pkgCache, LoadGuard: load.NewGuard(),
		})
		require.NoError(t, err)
		texts := make([]string, 0, len(issues))
		for _, issue := range issues {
			texts = append(texts, issue.Text)
		}
		sort.Strings(texts)
		return texts, [3]int32{counts[0].Load(), counts[1].Load(), counts[2].Load()}
	}
	want := []string{"factful issue", "prunable issue", "reported issue"}
	texts, counts := run()
	assert.Equal(t, want, texts)
	assert.Equal(t, [3]int32{1, 1, 1}, counts)

	writeDep("func Value() int { value := 1; return value }\n")
	texts, counts = run()
	assert.Equal(t, want, texts)
	assert.Equal(t, [3]int32{0, 0, 1}, counts)

	texts, counts = run()
	assert.Equal(t, want, texts)
	assert.Equal(t, [3]int32{}, counts)

	writeDep("func Value() string { return \"changed\" }\n")
	texts, counts = run()
	assert.Equal(t, want, texts)
	assert.Equal(t, [3]int32{1, 1, 1}, counts)
}
