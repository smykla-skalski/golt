package goanalysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/lint/lifecycle"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

func TestRunnerStopsAnalyzerDependentsAfterParentCancellation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/cancellation\n\ngo 1.26.0\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "example.go"),
		[]byte("package example\n"), 0o600))
	pkgs, err := packages.Load(&packages.Config{Mode: packages.LoadAllSyntax, Dir: dir}, "./...")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var dependentRan atomic.Bool
	blocking := &analysis.Analyzer{Name: "blocking", Run: func(*analysis.Pass) (any, error) {
		close(started)
		<-release
		return nil, nil
	}}
	dependent := &analysis.Analyzer{
		Name: "dependent", Requires: []*analysis.Analyzer{blocking},
		Run: func(*analysis.Pass) (any, error) {
			dependentRan.Store(true)
			return nil, nil
		},
	}
	logger := logutils.NewStderrLog("")
	r := &runner{
		prefix: "test", log: logger, loadGuard: load.NewGuard(),
		loadMode: LoadModeTypesInfo, passToPkg: map[*analysis.Pass]*packages.Package{},
		sw: timeutils.NewStopwatch("test", logger),
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	completed := make(chan []error, 1)
	go func() {
		_, errs, _ := r.run(ctx, []*analysis.Analyzer{dependent}, pkgs, nil)
		completed <- errs
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking analyzer did not start")
	}
	cancel()
	unblock()
	select {
	case errs := <-completed:
		require.ErrorIs(t, errors.Join(errs...), context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
	assert.False(t, dependentRan.Load())
}

func TestPrepareAnalysisSkipsCachedRoots(t *testing.T) {
	first := &packages.Package{PkgPath: "example.com/first"}
	second := &packages.Package{PkgPath: "example.com/second"}
	required := &analysis.Analyzer{Name: "required"}
	prunable := &analysis.Analyzer{Name: "prunable", Requires: []*analysis.Analyzer{required}}
	factful := &analysis.Analyzer{Name: "factful"}
	r := &runner{}

	_, actions, roots := r.prepareAnalysis([]*packages.Package{first, second},
		[]*analysis.Analyzer{prunable, factful}, func(a *analysis.Analyzer, pkg *packages.Package) bool {
			return a == prunable && pkg == first
		})

	require.Len(t, roots, 3)
	require.Len(t, actions, 4)
	for _, root := range roots {
		assert.False(t, root.Analyzer == prunable && root.Package == first)
	}
}

func TestPrunableAnalyzersExcludeFactsInRequirements(t *testing.T) {
	factful := &analysis.Analyzer{Name: "factful", FactTypes: []analysis.Fact{new(testPruningFact)}}
	prunable := &analysis.Analyzer{Name: "prunable"}
	needsFact := &analysis.Analyzer{Name: "needsFact", Requires: []*analysis.Analyzer{factful}}

	assert.Equal(t, []*analysis.Analyzer{prunable},
		NewLinter("prunable", "", []*analysis.Analyzer{prunable}, nil).getPrunableAnalyzers())
	assert.Empty(t, NewLinter("factful", "", []*analysis.Analyzer{needsFact}, nil).getPrunableAnalyzers())
	reporter := NewLinter("reported", "", []*analysis.Analyzer{prunable}, nil).
		WithIssuesReporter(func(*linter.Context) []*Issue { return nil })
	assert.Empty(t, reporter.getPrunableAnalyzers())
	assert.Equal(t, []*analysis.Analyzer{prunable}, reporter.WithCacheableIssuesReporter().getPrunableAnalyzers())
}

type testPruningFact struct{ Value string }

func (*testPruningFact) AFact() {}

func TestCollectAnalyzerStats(t *testing.T) {
	alpha := &analysis.Analyzer{Name: "alpha"}
	zeta := &analysis.Analyzer{Name: "zeta"}

	stats := collectAnalyzerStats([]*action{
		{Analyzer: zeta, Duration: 2 * time.Millisecond, Diagnostics: make([]analysis.Diagnostic, 2)},
		{Analyzer: alpha, Duration: time.Millisecond},
		{Analyzer: zeta, Err: errors.New("failed")},
	})

	require.Len(t, stats, 2)
	assert.Equal(t, alpha, stats[0].analyzer)
	assert.Equal(t, time.Millisecond, stats[0].elapsed)
	assert.Equal(t, 1, stats[0].actions)
	assert.Equal(t, 1, stats[0].executedActions)
	assert.Equal(t, zeta, stats[1].analyzer)
	assert.Equal(t, 2*time.Millisecond, stats[1].elapsed)
	assert.Equal(t, 2, stats[1].actions)
	assert.Equal(t, 1, stats[1].executedActions)
	assert.Equal(t, 2, stats[1].diagnostics)
	assert.Equal(t, 1, stats[1].errors)
}

func TestAnalysisLifecycleRecordsPanic(t *testing.T) {
	recorder := lifecycle.NewRecorder()
	metrics := &analysisLifecycle{
		recorder:      recorder,
		started:       time.Now(),
		report:        &lifecycle.AnalysisRun{},
		linterIndexes: map[string]int{},
	}

	assert.PanicsWithError(t, "boom", func() {
		var issues []*result.Issue
		var runErr error
		defer func() { metrics.finishRecovered(issues, runErr, recover()) }()

		panic(errors.New("boom"))
	})

	report := recorder.Snapshot()
	require.Len(t, report.Analysis, 1)
	assert.Equal(t, "boom", report.Analysis[0].Error)
}

func TestAnalysisLifecycleRecordsSchedulerStats(t *testing.T) {
	recorder := lifecycle.NewRecorder()
	metrics := &analysisLifecycle{
		recorder: recorder,
		report:   &lifecycle.AnalysisRun{},
	}

	metrics.recordStats(&analysisStats{
		scheduler: schedulerStats{
			rootActions:            3,
			horizontalEdges:        4,
			verticalEdges:          5,
			sourceActions:          6,
			sourceLoads:            7,
			exportLoads:            8,
			peakPackageWorkers:     2,
			peakActionGoroutines:   9,
			peakExecutingActions:   2,
			packageDependencyWait:  10 * time.Millisecond,
			analyzerDependencyWait: 11 * time.Millisecond,
		},
	})

	require.NotNil(t, metrics.report.Scheduler)
	assert.Equal(t, 3, metrics.report.Scheduler.RootActions)
	assert.Equal(t, 4, metrics.report.Scheduler.HorizontalEdges)
	assert.Equal(t, 5, metrics.report.Scheduler.VerticalEdges)
	assert.Equal(t, 6, metrics.report.Scheduler.SourceActions)
	assert.Equal(t, 7, metrics.report.Scheduler.SourceLoads)
	assert.Equal(t, 8, metrics.report.Scheduler.ExportLoads)
	assert.Equal(t, 2, metrics.report.Scheduler.PeakPackageWorkers)
	assert.Equal(t, 9, metrics.report.Scheduler.PeakActionGoroutines)
	assert.Equal(t, 2, metrics.report.Scheduler.PeakExecutingActions)
	assert.Equal(t, int64(10*time.Millisecond), metrics.report.Scheduler.PackageDependencyWaitNS)
	assert.Equal(t, int64(11*time.Millisecond), metrics.report.Scheduler.AnalyzerDependencyWaitNS)
}
