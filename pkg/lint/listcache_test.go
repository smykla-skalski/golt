package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const listCacheTestMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedModule |
	packages.NeedImports | packages.NeedDeps | packages.NeedExportFile | packages.NeedTypesSizes

func writeTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	files := map[string]string{
		"go.mod":         "module example.com/m\n\ngo 1.24\n",
		"a/a.go":         "package a\n\nimport \"strings\"\n\nfunc A() string { return strings.ToUpper(\"a\") }\n",
		"a/a_test.go":    "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { _ = A() }\n",
		"b/b.go":         "package b\n\nimport \"example.com/m/a\"\n\nvar B = a.A()\n",
		"testdata/x.go":  "package x\n",
		".hidden/h.go":   "package h\n",
		"nested/go.mod":  "module example.com/nested\n\ngo 1.24\n",
		"nested/n/n.go":  "package n\n",
		"b/internal/.gk": "",
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	ageTree(t, dir)

	return dir
}

// ageTree moves all timestamps into the past so later edits get a distinct
// mtime even on filesystems with coarse timestamp granularity.
func ageTree(t *testing.T, dir string) {
	t.Helper()

	past := time.Now().Add(-time.Hour)

	var paths []string

	require.NoError(t, filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)

		return err
	}))

	for _, path := range paths {
		require.NoError(t, os.Chtimes(path, past, past))
	}
}

func newTestListCache(t *testing.T, dir string) (*listCache, *packages.Config) {
	t.Helper()

	t.Chdir(dir)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")

	conf := &packages.Config{Mode: listCacheTestMode, Tests: true, Context: t.Context()}

	lc := newListCache(t.Context(), t.TempDir(), conf, []string{"./..."})
	require.NotNil(t, lc)

	return lc, conf
}

func loadAndStore(t *testing.T, lc *listCache, conf *packages.Config) []*packages.Package {
	t.Helper()

	pkgs, err := packages.Load(conf, "./...")
	require.NoError(t, err)
	require.NoError(t, lc.store(pkgs))

	return pkgs
}

func TestListCache_roundTrip(t *testing.T) {
	lc, conf := newTestListCache(t, writeTestModule(t))

	pkgs := loadAndStore(t, lc, conf)

	cached, ok := lc.load()
	require.True(t, ok)

	assert.Equal(t, lc.encode(pkgs).Packages, lc.encode(cached).Packages)

	for _, pkg := range cached {
		assert.NotNil(t, pkg.TypesSizes, pkg.ID)
	}
}

func TestListCache_invalidation(t *testing.T) {
	testCases := []struct {
		desc   string
		change func(t *testing.T, dir string)
		hit    bool
	}{
		{
			desc:   "unchanged",
			change: func(*testing.T, string) {},
			hit:    true,
		},
		{
			desc: "file edited",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte("package b\n\nvar B = 1\n"), 0o600))
			},
		},
		{
			desc: "file touched",
			change: func(t *testing.T, dir string) {
				t.Helper()
				now := time.Now()
				require.NoError(t, os.Chtimes(filepath.Join(dir, "a", "a.go"), now, now))
			},
		},
		{
			desc: "file added to package",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "a2.go"), []byte("package a\n"), 0o600))
			},
		},
		{
			desc: "file removed from package",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.Remove(filepath.Join(dir, "a", "a_test.go")))
			},
		},
		{
			desc: "package added",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "b", "internal", "c"), 0o750))
			},
		},
		{
			desc: "go.mod changed",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.25\n"), 0o600))
			},
		},
		{
			desc: "go.work added",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.24\n\nuse .\n"), 0o600))
			},
		},
		{
			desc: "ignored directory changed",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "testdata", "y.go"), []byte("package x\n"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden", "i.go"), []byte("package h\n"), 0o600))
			},
			hit: true,
		},
		{
			desc: "nested module changed",
			change: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested", "m"), 0o750))
			},
			hit: true,
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			dir := writeTestModule(t)
			lc, conf := newTestListCache(t, dir)

			loadAndStore(t, lc, conf)

			test.change(t, dir)

			_, ok := lc.load()
			assert.Equal(t, test.hit, ok)
		})
	}
}

func TestListCache_skipsResultsWithErrors(t *testing.T) {
	dir := writeTestModule(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte("package b\n\nimport \"example.com/missing\"\n"), 0o600))

	lc, conf := newTestListCache(t, dir)

	loadAndStore(t, lc, conf)

	_, ok := lc.load()
	assert.False(t, ok)
}

func TestListCache_key(t *testing.T) {
	dir := writeTestModule(t)
	lc, conf := newTestListCache(t, dir)

	same := newListCache(t.Context(), t.TempDir(), conf, []string{"./..."})
	require.NotNil(t, same)
	assert.Equal(t, lc.key, same.key)

	other := newListCache(t.Context(), t.TempDir(), conf, []string{"./a/..."})
	require.NotNil(t, other)
	assert.NotEqual(t, lc.key, other.key)

	tags := *conf
	tags.BuildFlags = []string{"-tags", "foo"}
	withTags := newListCache(t.Context(), t.TempDir(), &tags, []string{"./..."})
	require.NotNil(t, withTags)
	assert.NotEqual(t, lc.key, withTags.key)

	t.Setenv("GOFLAGS", "-mod=readonly")
	goflags := newListCache(t.Context(), t.TempDir(), conf, []string{"./..."})
	require.NotNil(t, goflags)
	assert.NotEqual(t, lc.key, goflags.key)

	t.Setenv(envListCache, "0")
	assert.Nil(t, newListCache(t.Context(), t.TempDir(), conf, []string{"./..."}))
}

func TestListCache_evict(t *testing.T) {
	lc := &listCache{dir: t.TempDir()}

	base := time.Now().Add(-time.Hour)

	for i := range listCacheKeep + 3 {
		path := filepath.Join(lc.dir, fmt.Sprintf("%02d.gob", i))
		require.NoError(t, os.WriteFile(path, nil, 0o600))

		used := base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(path, used, used))
	}

	lc.evict()

	entries, err := os.ReadDir(lc.dir)
	require.NoError(t, err)
	require.Len(t, entries, listCacheKeep)
	assert.Equal(t, "03.gob", entries[0].Name())
}
