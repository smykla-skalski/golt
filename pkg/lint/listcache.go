package lint

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ldez/grignotin/goenv"
	"golang.org/x/tools/go/packages"
)

const (
	envListCache    = "GOLT_LIST_CACHE"
	listCacheSchema = "golt-list-v2"
	listCacheDir    = "golt-list"
	listCacheKeep   = 8

	// walkIOPerCPU oversubscribes directory reads, which mostly wait on I/O.
	walkIOPerCPU = 4

	// racyWindow covers filesystems with coarse (up to 1s) mtime granularity.
	racyWindow = time.Second
)

// moduleStateFiles change the package graph without touching any listed file.
var moduleStateFiles = []string{goModFile, "go.sum", "go.work", "go.work.sum", filepath.Join("vendor", "modules.txt")}

// listCache stores the result of packages.Load and reuses it while nothing
// that go list reads has changed: the go environment, the load request, and
// the timestamps of every mutable file and directory it listed. Files under
// GOROOT and GOMODCACHE are immutable and not checked.
type listCache struct {
	dir      string
	key      string
	goroot   string
	modcache string
	gocache  string
	goarch   string
}

type listCacheEntry struct {
	listCacheHeader
	listCacheBody
}

// listCacheHeader is decoded first so a stale entry is rejected without
// decoding the package graph.
type listCacheHeader struct {
	Schema   string
	Snapshot listSnapshot
}

type listCacheBody struct {
	Roots    []string
	Packages []cachedPackage
}

type cachedPackage struct {
	ID              string
	Name            string
	PkgPath         string
	Errors          []packages.Error
	GoFiles         []string
	CompiledGoFiles []string
	OtherFiles      []string
	EmbedFiles      []string
	EmbedPatterns   []string
	IgnoredFiles    []string
	ExportFile      string
	BuildID         string
	Target          string
	ForTest         string
	Module          *packages.Module
	Imports         map[string]string
	HasTypesSizes   bool
}

type fileStamp struct {
	Size    int64
	ModTime int64
}

type listSnapshot struct {
	Files       map[string]fileStamp
	Exports     map[string]fileStamp
	PackageDirs map[string]int64
	ModuleDirs  []string
	Dirs        map[string]int64
}

// newListCache returns nil when caching is disabled or the environment cannot
// be identified, in which case packages are always loaded with go list.
func newListCache(ctx context.Context, cacheRoot string, conf *packages.Config, args []string) *listCache {
	if os.Getenv(envListCache) == "0" || cacheRoot == "" {
		return nil
	}

	env, err := goenv.GetAll(ctx)
	if err != nil {
		return nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil
	}

	exeStamp := statStamp(exe)

	// GOGCCFLAGS embeds a per-invocation temp dir.
	delete(env, "GOGCCFLAGS")

	envJSON, err := json.Marshal(env)
	if err != nil {
		return nil
	}

	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d %d\n%s\n", listCacheSchema, exe, exeStamp.Size, exeStamp.ModTime, envJSON)
	fmt.Fprintf(h, "wd %s\nmode %d\ntests %t\n", wd, conf.Mode, conf.Tests)
	fmt.Fprintf(h, "flags %q\nargs %q\n", conf.BuildFlags, args)

	return &listCache{
		dir:      filepath.Join(cacheRoot, listCacheDir),
		key:      hex.EncodeToString(h.Sum(nil)),
		goroot:   env[goenv.GOROOT],
		modcache: env[goenv.GOMODCACHE],
		gocache:  env[goenv.GOCACHE],
		goarch:   env[goenv.GOARCH],
	}
}

func (c *listCache) path() string {
	return filepath.Join(c.dir, c.key+".gob")
}

// load returns the cached packages when every recorded stamp still matches.
func (c *listCache) load() ([]*packages.Package, bool) {
	pkgs, status := c.loadWithStatus()

	return pkgs, status == "hit"
}

func (c *listCache) loadWithStatus() ([]*packages.Package, string) {
	f, err := os.Open(c.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "missing"
		}

		return nil, "read-error"
	}
	defer f.Close()

	dec := gob.NewDecoder(bufio.NewReader(f))

	var entry listCacheEntry
	if err := dec.Decode(&entry.listCacheHeader); err != nil || entry.Schema != listCacheSchema {
		return nil, "invalid-header"
	}

	if !entry.Snapshot.matches() {
		return nil, "changed-input"
	}

	if err := dec.Decode(&entry.listCacheBody); err != nil {
		return nil, "invalid-body"
	}

	now := time.Now()
	_ = os.Chtimes(c.path(), now, now)

	return c.decode(&entry), "hit"
}

// store saves pkgs loaded by a go list started at loadStart. It skips results
// with package errors, which may be transient (e.g. a failed module download),
// and results racing with edits: a file changed while go list ran could be
// listed with its old content yet stamped with its new mtime.
func (c *listCache) store(pkgs []*packages.Package, loadStart time.Time) error {
	var hasErrors bool
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if len(pkg.Errors) > 0 {
			hasErrors = true
		}
	})
	if hasErrors {
		return nil
	}

	entry := c.encode(pkgs)

	if entry.Snapshot.modifiedSince(loadStart.Add(-racyWindow).UnixNano()) {
		return nil
	}

	var buf bytes.Buffer

	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(entry.listCacheHeader); err != nil {
		return err
	}

	if err := enc.Encode(entry.listCacheBody); err != nil {
		return err
	}

	if err := os.MkdirAll(c.dir, os.ModePerm); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(c.dir, c.key+".*.tmp")
	if err != nil {
		return err
	}

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), c.path()); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	c.evict()

	return nil
}

// evict keeps the most recently used entries; keys change with every golt
// build and configuration, so stale ones would otherwise accumulate.
func (c *listCache) evict() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}

	type usedEntry struct {
		path string
		used time.Time
	}

	var used []usedEntry

	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !strings.HasSuffix(e.Name(), ".gob") {
			continue
		}

		used = append(used, usedEntry{path: filepath.Join(c.dir, e.Name()), used: info.ModTime()})
	}

	if len(used) <= listCacheKeep {
		return
	}

	slices.SortFunc(used, func(a, b usedEntry) int { return b.used.Compare(a.used) })

	for _, e := range used[listCacheKeep:] {
		_ = os.Remove(e.path)
	}
}

func (c *listCache) encode(pkgs []*packages.Package) *listCacheEntry {
	entry := &listCacheEntry{listCacheHeader: listCacheHeader{Schema: listCacheSchema}}

	var all []*packages.Package
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		all = append(all, pkg)
	})

	for _, pkg := range pkgs {
		entry.Roots = append(entry.Roots, pkg.ID)
	}

	for _, pkg := range all {
		imports := make(map[string]string, len(pkg.Imports))
		for path, imp := range pkg.Imports {
			imports[path] = imp.ID
		}

		entry.Packages = append(entry.Packages, cachedPackage{
			ID:              pkg.ID,
			Name:            pkg.Name,
			PkgPath:         pkg.PkgPath,
			Errors:          pkg.Errors,
			GoFiles:         pkg.GoFiles,
			CompiledGoFiles: pkg.CompiledGoFiles,
			OtherFiles:      pkg.OtherFiles,
			EmbedFiles:      pkg.EmbedFiles,
			EmbedPatterns:   pkg.EmbedPatterns,
			IgnoredFiles:    pkg.IgnoredFiles,
			ExportFile:      pkg.ExportFile,
			BuildID:         pkg.BuildID,
			Target:          pkg.Target,
			ForTest:         pkg.ForTest,
			Module:          pkg.Module,
			Imports:         imports,
			HasTypesSizes:   pkg.TypesSizes != nil,
		})
	}

	entry.Snapshot = c.snapshot(all)

	return entry
}

func (c *listCache) decode(entry *listCacheEntry) []*packages.Package {
	sizes := types.SizesFor("gc", c.goarch)

	byID := make(map[string]*packages.Package, len(entry.Packages))
	for i := range entry.Packages {
		cp := &entry.Packages[i]

		pkg := &packages.Package{
			ID:              cp.ID,
			Name:            cp.Name,
			PkgPath:         cp.PkgPath,
			Errors:          cp.Errors,
			GoFiles:         cp.GoFiles,
			CompiledGoFiles: cp.CompiledGoFiles,
			OtherFiles:      cp.OtherFiles,
			EmbedFiles:      cp.EmbedFiles,
			EmbedPatterns:   cp.EmbedPatterns,
			IgnoredFiles:    cp.IgnoredFiles,
			ExportFile:      cp.ExportFile,
			BuildID:         cp.BuildID,
			Target:          cp.Target,
			ForTest:         cp.ForTest,
			Module:          cp.Module,
		}
		if cp.HasTypesSizes {
			pkg.TypesSizes = sizes
		}

		byID[cp.ID] = pkg
	}

	for i := range entry.Packages {
		cp := &entry.Packages[i]
		pkg := byID[cp.ID]

		if len(cp.Imports) > 0 {
			pkg.Imports = make(map[string]*packages.Package, len(cp.Imports))
			for path, id := range cp.Imports {
				pkg.Imports[path] = byID[id]
			}
		}
	}

	roots := make([]*packages.Package, 0, len(entry.Roots))
	for _, id := range entry.Roots {
		roots = append(roots, byID[id])
	}

	return roots
}

func (c *listCache) immutable(path string) bool {
	return isWithin(path, c.goroot) || isWithin(path, c.modcache)
}

func (c *listCache) snapshot(all []*packages.Package) listSnapshot {
	snap := listSnapshot{Files: map[string]fileStamp{}, Exports: map[string]fileStamp{}, Dirs: map[string]int64{}}

	var moduleDirs []string

	for _, pkg := range all {
		for _, files := range [][]string{pkg.GoFiles, pkg.CompiledGoFiles, pkg.OtherFiles, pkg.EmbedFiles, pkg.IgnoredFiles} {
			for _, file := range files {
				if c.immutable(file) {
					continue
				}

				// cgo sources generated by go list live in GOCACHE.
				if isWithin(file, c.gocache) {
					snap.Exports[file] = statStamp(file)

					continue
				}

				snap.Files[file] = statStamp(file)

				// A new file in a listed package changes the directory mtime.
				dir := filepath.Dir(file)
				if _, ok := snap.PackageDirs[dir]; !ok {
					if snap.PackageDirs == nil {
						snap.PackageDirs = map[string]int64{}
					}

					snap.PackageDirs[dir] = dirModTime(dir)
				}
			}
		}

		if pkg.ExportFile != "" {
			snap.Exports[pkg.ExportFile] = statStamp(pkg.ExportFile)
		}

		if pkg.Module == nil || pkg.Module.Dir == "" || c.immutable(pkg.Module.Dir) {
			continue
		}

		for _, name := range moduleStateFiles {
			file := filepath.Join(pkg.Module.Dir, name)
			snap.Files[file] = statStamp(file)
		}

		if pkg.Module.Main {
			moduleDirs = append(moduleDirs, pkg.Module.Dir)
		}
	}

	slices.Sort(moduleDirs)
	snap.ModuleDirs = slices.Compact(moduleDirs)

	for _, dir := range snap.ModuleDirs {
		walkModuleDirs(dir, snap.Dirs)
	}

	return snap
}

func (s *listSnapshot) matches() bool {
	for _, files := range []map[string]fileStamp{s.Files, s.Exports} {
		for file, want := range files {
			if statStamp(file) != want {
				return false
			}
		}
	}

	for dir, want := range s.PackageDirs {
		if dirModTime(dir) != want {
			return false
		}
	}

	current := make(map[string]int64, len(s.Dirs))
	for _, dir := range s.ModuleDirs {
		walkModuleDirs(dir, current)
	}

	return maps.Equal(current, s.Dirs)
}

// modifiedSince reports whether any go list input changed at or after t.
// Export files are go list outputs and are excluded.
func (s *listSnapshot) modifiedSince(t int64) bool {
	for _, stamp := range s.Files {
		if stamp.ModTime >= t {
			return true
		}
	}

	for _, dirs := range []map[string]int64{s.PackageDirs, s.Dirs} {
		for _, modTime := range dirs {
			if modTime >= t {
				return true
			}
		}
	}

	return false
}

// walkModuleDirs records the modification time of every directory a ./...
// pattern would descend into within a main module, so added or removed
// packages invalidate the cache. Directories are read in parallel: large
// modules have tens of thousands of them.
func walkModuleDirs(root string, dirs map[string]int64) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, walkIOPerCPU*runtime.GOMAXPROCS(0))
	)

	record := func(dir string, stamp int64) {
		mu.Lock()
		dirs[dir] = stamp
		mu.Unlock()
	}

	var visit func(dir string)
	visit = func(dir string) {
		defer wg.Done()

		sem <- struct{}{}
		stamp := dirModTime(dir)
		entries, err := os.ReadDir(dir)
		<-sem

		if err != nil {
			record(dir, -1)

			return
		}

		if dir != root && slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() == goModFile && !e.IsDir() }) {
			record(dir, -3)

			return
		}

		record(dir, stamp)

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}

			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
				continue
			}

			wg.Add(1)
			go visit(filepath.Join(dir, name))
		}
	}

	wg.Add(1)
	visit(root)
	wg.Wait()
}

func dirModTime(dir string) int64 {
	info, err := os.Stat(dir)
	if err != nil {
		return -1
	}

	return info.ModTime().UnixNano()
}

func statStamp(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileStamp{Size: -1, ModTime: -1}
		}

		return fileStamp{Size: -2, ModTime: -2}
	}

	return fileStamp{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
}

func isWithin(path, dir string) bool {
	if dir == "" {
		return false
	}

	rel, err := filepath.Rel(dir, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
