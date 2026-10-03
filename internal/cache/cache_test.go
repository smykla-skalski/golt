package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

func setupCache(t *testing.T) *Cache {
	t.Helper()

	log := logutils.NewStderrLog("skip")
	sw := timeutils.NewStopwatch("pkgcache", log)

	pkgCache, err := NewCache(sw, log)
	require.NoError(t, err)

	return pkgCache
}

func fakePackage() *packages.Package {
	return &packages.Package{
		PkgPath: "github.com/golangci/example",
		CompiledGoFiles: []string{
			"./testdata/hello.go",
		},
		Imports: map[string]*packages.Package{
			"a": {
				PkgPath: "github.com/golangci/example/a",
			},
			"b": {
				PkgPath: "github.com/golangci/example/b",
			},
			"unsafe": {
				PkgPath: "unsafe",
			},
		},
	}
}

type Foo struct {
	Value string
}

func TestCache_Put(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())

	pkgCache := setupCache(t)

	pkg := fakePackage()

	in := &Foo{Value: "hello"}

	err := pkgCache.Put(pkg, HashModeNeedAllDeps, "key", in)
	require.NoError(t, err)

	out := &Foo{}
	err = pkgCache.Get(pkg, HashModeNeedAllDeps, "key", out)
	require.NoError(t, err)

	assert.Equal(t, in, out)

	pkgCache.Close()
}

func TestCache_Get_missing_data(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())

	pkgCache := setupCache(t)

	pkg := fakePackage()

	out := &Foo{}
	err := pkgCache.Get(pkg, HashModeNeedAllDeps, "key", out)
	require.Error(t, err)

	require.ErrorIs(t, err, ErrMissing)

	pkgCache.Close()
}

func TestCache_buildKey(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	actionID, err := pkgCache.buildKey(pkg, HashModeNeedAllDeps, "")
	require.NoError(t, err)

	assert.Equal(t, "f32bf1bf010aa9b570e081c64ec9e22e17aafa1e822990ba952905ec5fdf8d9d", fmt.Sprintf("%x", actionID))
}

func TestCache_pkgActionID(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	actionID, err := pkgCache.pkgActionID(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "f690f05acd1024386ae912d9ad9c04080523b9a899f6afe56ab3108d88215c1d", fmt.Sprintf("%x", actionID))
}

func TestCache_packageHash_load(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	pkgCache.pkgHashes.Store(pkg, hashResults{HashModeNeedAllDeps: "fake"})

	hash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "fake", hash)
}

func TestCache_packageHash_store(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	hash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", hash)

	results, ok := pkgCache.pkgHashes.Load(pkg)
	require.True(t, ok)

	hashRes := results.(hashResults)

	require.Len(t, hashRes, 3)

	assert.Equal(t, "8978e3d76c6f99e9663558d7147a7790f229a676804d1fde706a611898547b74", hashRes[HashModeNeedOnlySelf])
	assert.Equal(t, "b1aef902a0619b5cbfc2d6e2e91a73dd58dd448e58274b2d7a5ff8efd97aefa4", hashRes[HashModeNeedDirectDeps])
	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", hashRes[HashModeNeedAllDeps])
}

func TestCache_exportDepsHash_fallsBackWithoutExportFile(t *testing.T) {
	pkgCache := setupCache(t)
	pkg := fakePackage()

	fullHash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)
	exportHash, err := pkgCache.packageHash(pkg, HashModeNeedExportDeps)
	require.NoError(t, err)
	assert.Equal(t, fullHash, exportHash)
}

func TestCache_exportDepsHash_prunesDependencyBodyEdits(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/pruning\n\ngo 1.26.0\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "dep"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "user"), 0o700))
	depFile := filepath.Join(dir, "dep", "dep.go")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "user", "user.go"),
		[]byte("package user\nimport \"example.com/pruning/dep\"\nvar Value = dep.Value()\n"), 0o600))
	loadHash := func() string {
		t.Helper()
		pkgs, err := packages.Load(&packages.Config{
			Dir: dir,
			Mode: packages.NeedName | packages.NeedModule | packages.NeedCompiledGoFiles |
				packages.NeedImports | packages.NeedDeps | packages.NeedExportFile,
		}, "./user")
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
		require.Empty(t, pkgs[0].Errors)
		hash, err := setupCache(t).packageHash(pkgs[0], HashModeNeedExportDeps)
		require.NoError(t, err)
		return hash
	}
	require.NoError(t, os.WriteFile(depFile,
		[]byte("package dep\n//go:noinline\nfunc Value() int { return 1 }\n"), 0o600))
	initial := loadHash()
	require.NoError(t, os.WriteFile(depFile,
		[]byte("package dep\n//go:noinline\nfunc Value() int { value := 1; return value }\n"), 0o600))
	assert.Equal(t, initial, loadHash())
	require.NoError(t, os.WriteFile(depFile,
		[]byte("package dep\n//go:noinline\nfunc Value() string { return \"changed\" }\n"), 0o600))
	assert.NotEqual(t, initial, loadHash())
}

func TestCache_computeHash(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	results, err := pkgCache.computePkgHash(pkg)
	require.NoError(t, err)

	require.Len(t, results, 3)

	assert.Equal(t, "8978e3d76c6f99e9663558d7147a7790f229a676804d1fde706a611898547b74", results[HashModeNeedOnlySelf])
	assert.Equal(t, "b1aef902a0619b5cbfc2d6e2e91a73dd58dd448e58274b2d7a5ff8efd97aefa4", results[HashModeNeedDirectDeps])
	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", results[HashModeNeedAllDeps])
}

func TestCache_computeHash_buildID(t *testing.T) {
	missing := []string{"./testdata/does-not-exist.go"}
	dependency := &packages.Module{Path: "example.com/dep", Version: "v1.0.0", Dir: "/modcache/example.com/dep@v1.0.0"}
	workspace := &packages.Module{Path: "example.com/main", Dir: "./testdata", Main: true}

	tests := []struct {
		desc    string
		pkg     *packages.Package
		wantErr bool
		otherID string
	}{
		{
			desc:    "stdlib uses build ID without reading files",
			pkg:     &packages.Package{PkgPath: "fmt", CompiledGoFiles: missing, IgnoredFiles: missing, BuildID: "a/b"},
			otherID: "c/d",
		},
		{
			desc:    "module cache dependency uses build ID",
			pkg:     &packages.Package{PkgPath: "example.com/dep", Module: dependency, CompiledGoFiles: missing, BuildID: "a/b"},
			otherID: "c/d",
		},
		{
			desc:    "workspace package hashes files",
			pkg:     &packages.Package{PkgPath: "example.com/main", Module: workspace, CompiledGoFiles: missing, BuildID: "a/b"},
			wantErr: true,
		},
		{
			desc:    "dependency without build ID hashes files",
			pkg:     &packages.Package{PkgPath: "example.com/dep", Module: dependency, CompiledGoFiles: missing},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			results, err := setupCache(t).computePkgHash(test.pkg)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			other := *test.pkg
			other.BuildID = test.otherID
			otherResults, err := setupCache(t).computePkgHash(&other)
			require.NoError(t, err)

			assert.NotEqual(t, results[HashModeNeedOnlySelf], otherResults[HashModeNeedOnlySelf])
		})
	}
}

func TestCache_pkgActionID_sharedSalt(t *testing.T) {
	dependency := &packages.Module{Path: "example.com/dep", Version: "v1.0.0", Dir: "/modcache/example.com/dep@v1.0.0"}
	workspace := &packages.Module{Path: "example.com/main", Dir: "./testdata", Main: true}

	dep := &packages.Package{PkgPath: "example.com/dep", Module: dependency, BuildID: "a/b"}
	root := &packages.Package{
		PkgPath:         "example.com/main",
		Module:          workspace,
		CompiledGoFiles: []string{"./testdata/hello.go"},
		Imports:         map[string]*packages.Package{"example.com/dep": dep},
	}

	actionIDs := func(projectSalt string) (depID, rootID [32]byte) {
		t.Helper()

		SetSharedSalt(bytes.NewBufferString("shared"))
		SetSalt(bytes.NewBufferString("shared" + projectSalt))
		t.Cleanup(func() {
			SetSalt(bytes.NewBuffer(nil))
			SetSharedSalt(bytes.NewBuffer(nil))
		})

		pkgCache := setupCache(t)

		depID, err := pkgCache.pkgActionID(dep, HashModeNeedAllDeps)
		require.NoError(t, err)

		rootID, err = pkgCache.pkgActionID(root, HashModeNeedAllDeps)
		require.NoError(t, err)

		return depID, rootID
	}

	depA, rootA := actionIDs("go.mod A")
	depB, rootB := actionIDs("go.mod B")

	assert.Equal(t, depA, depB, "dependency keys ignore the project salt")
	assert.NotEqual(t, rootA, rootB, "workspace keys include the project salt")
}
