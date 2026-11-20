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

	assert.Equal(t, "4b048b43c3506df16209043a08cdd4498b471590cddd14d89de39907a87e9d1d", fmt.Sprintf("%x", actionID))
}

func TestCache_pkgActionID(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	actionID, err := pkgCache.pkgActionID(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "cd892bf2556f11d7eb91626f5e228859646a001a9bd489f3d267ef687879ccf2", fmt.Sprintf("%x", actionID))
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

	assert.Equal(t, "9114847c7d8ad5ee2e3f04f989a069fcdd91f762f220b8b16cad72de68df3530", hash)

	results, ok := pkgCache.pkgHashes.Load(pkg)
	require.True(t, ok)

	hashRes := results.(hashResults)

	require.Len(t, hashRes, 3)

	assert.Equal(t, "3699dbf7b96814ac7f486053ce9de0409a360f3b6990a417e656fd7452043dc3", hashRes[HashModeNeedOnlySelf])
	assert.Equal(t, "3a1961cdd38edf42038087ec5e6b44176c8d40b8c04598ef4599f38e21801ad6", hashRes[HashModeNeedDirectDeps])
	assert.Equal(t, "9114847c7d8ad5ee2e3f04f989a069fcdd91f762f220b8b16cad72de68df3530", hashRes[HashModeNeedAllDeps])
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

	testCases := []struct {
		desc string
		pkg  func() *packages.Package

		expectedHashModeNeedOnlySelf   string
		expectedHashModeNeedDirectDeps string
		expectedHashModeNeedAllDeps    string
	}{
		{
			desc: "case a",
			pkg: func() *packages.Package {
				pkg := fakePackage()

				pkg.CompiledGoFiles = []string{
					"./testdata/hello.go",
					"./testdata/world.go",
				}

				pkg.IgnoredFiles = []string{}

				return pkg
			},
			expectedHashModeNeedOnlySelf:   "3aa2ecfb2d98dbc96766d158663942aa72ff354e388b9df61f008726fbf8af94",
			expectedHashModeNeedDirectDeps: "542a5cbc0c2d710b35a5ea07dadaa3d562010e7d191a91d0e0eb66421df17d05",
			expectedHashModeNeedAllDeps:    "5d7d365ffb255000579a39d580ce0674d1bef7f2d3a4b70da01c92e4795ef0f8",
		},
		{
			desc: "case b",
			pkg: func() *packages.Package {
				pkg := fakePackage()

				pkg.CompiledGoFiles = []string{
					"./testdata/hello.go",
				}

				pkg.IgnoredFiles = []string{
					"./testdata/world.go",
				}

				return pkg
			},
			expectedHashModeNeedOnlySelf:   "ad81fd8c68683d7445ee55c8284041e4ad2f42be429ed35d0583f0da69c7f1ae",
			expectedHashModeNeedDirectDeps: "48b3430ddb0300030cdd30d57390afde734adafe3e4d09b70f97614f9826c5e5",
			expectedHashModeNeedAllDeps:    "ffa6661e52a4fcdab4c1c378677f8ff9b89cdb92712cf0d7fb3658df7147d3de",
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			results, err := pkgCache.computePkgHash(test.pkg())
			require.NoError(t, err)

			require.Len(t, results, 3)

			assert.Equal(t, test.expectedHashModeNeedOnlySelf, results[HashModeNeedOnlySelf])
			assert.Equal(t, test.expectedHashModeNeedDirectDeps, results[HashModeNeedDirectDeps])
			assert.Equal(t, test.expectedHashModeNeedAllDeps, results[HashModeNeedAllDeps])
		})
	}
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
