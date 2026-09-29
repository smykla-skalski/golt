package packages_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/internal/testenv"
)

func writeBuildIDModule(t *testing.T, dir, body string) {
	t.Helper()
	files := map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc F() int { " + body + " }\n",
		"b/b.go": "package b\n\nimport \"example.com/m/a\"\n\nvar V = a.F\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadBuildIDs(t *testing.T, dir string, mode packages.LoadMode) map[string]string {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{Mode: mode, Dir: dir, Env: append(os.Environ(), "GOFLAGS=-mod=mod")}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, pkg := range pkgs {
		ids[pkg.PkgPath] = pkg.BuildID
	}
	return ids
}

func TestBuildID(t *testing.T) {
	testenv.NeedsGoBuild(t)
	testenv.NeedsGoPackages(t)
	dir := t.TempDir()
	writeBuildIDModule(t, dir, "return 1")

	ids := loadBuildIDs(t, dir, packages.NeedName|packages.NeedExportFile)
	for _, path := range []string{"example.com/m/a", "example.com/m/b"} {
		if !strings.Contains(ids[path], "/") {
			t.Errorf("BuildID(%s) = %q, want actionID/contentID", path, ids[path])
		}
	}

	t.Run("cleared without NeedExportFile", func(t *testing.T) {
		for path, id := range loadBuildIDs(t, dir, packages.NeedName) {
			if id != "" {
				t.Errorf("BuildID(%s) = %q without NeedExportFile, want empty", path, id)
			}
		}
	})

	t.Run("JSON round trip", func(t *testing.T) {
		data, err := json.Marshal(&packages.Package{ID: "p", BuildID: ids["example.com/m/a"]})
		if err != nil {
			t.Fatal(err)
		}
		var got packages.Package
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.BuildID != ids["example.com/m/a"] {
			t.Errorf("round-tripped BuildID = %q, want %q", got.BuildID, ids["example.com/m/a"])
		}
	})

	t.Run("changes with source", func(t *testing.T) {
		writeBuildIDModule(t, dir, "return 2")
		changed := loadBuildIDs(t, dir, packages.NeedName|packages.NeedExportFile)
		if changed["example.com/m/a"] == ids["example.com/m/a"] {
			t.Errorf("BuildID of edited package a did not change")
		}
	})
}
