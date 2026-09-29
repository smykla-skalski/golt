package goanalysis

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func testLoadingPackage(name string, files int, imports ...*loadingPackage) *loadingPackage {
	pkg := &packages.Package{PkgPath: name}
	for i := range files {
		pkg.CompiledGoFiles = append(pkg.CompiledGoFiles, fmt.Sprintf("%s/%d.go", name, i))
	}

	lp := &loadingPackage{pkg: pkg, imports: map[string]*loadingPackage{}}
	for _, imp := range imports {
		lp.imports[imp.pkg.PkgPath] = imp
	}

	return lp
}

func TestPackagePrioritiesFollowHeaviestDependentChain(t *testing.T) {
	base := testLoadingPackage("base", 1)
	left := testLoadingPackage("left", 2, base)
	right := testLoadingPackage("right", 9, base)
	root := testLoadingPackage("root", 4, left, right)
	leaf := testLoadingPackage("leaf", 0)

	priorities := packagePriorities(map[*loadingPackage]struct{}{
		base: {}, left: {}, right: {}, root: {}, leaf: {},
	})

	assert.Equal(t, int64(5), priorities[root])
	assert.Equal(t, int64(8), priorities[left])
	assert.Equal(t, int64(15), priorities[right])
	assert.Equal(t, int64(17), priorities[base], "base takes the heavier dependent chain")
	assert.Equal(t, int64(1), priorities[leaf])
}

func waitForWaiters(t *testing.T, sem *prioritySemaphore, n int) {
	t.Helper()

	require.Eventually(t, func() bool {
		sem.mu.Lock()
		defer sem.mu.Unlock()

		return len(sem.waiters) == n
	}, 5*time.Second, time.Millisecond)
}

func TestPrioritySemaphoreGrantsByPriorityThenArrival(t *testing.T) {
	sem := newPrioritySemaphore(1)
	ctx := t.Context()
	require.True(t, sem.acquire(ctx, 0))

	var (
		mu    sync.Mutex
		order []string
		wg    sync.WaitGroup
	)
	for i, waiter := range []struct {
		name     string
		priority int64
	}{
		{"low", 1}, {"high", 3}, {"mid-first", 2}, {"mid-second", 2},
	} {
		wg.Go(func() {
			if !sem.acquire(ctx, waiter.priority) {
				return
			}
			mu.Lock()
			order = append(order, waiter.name)
			mu.Unlock()
			sem.release()
		})
		waitForWaiters(t, sem, i+1)
	}

	sem.release()
	wg.Wait()

	assert.Equal(t, []string{"high", "mid-first", "mid-second", "low"}, order)
}

func TestPrioritySemaphoreCancelledWaiterLeavesQueue(t *testing.T) {
	sem := newPrioritySemaphore(1)
	require.True(t, sem.acquire(t.Context(), 0))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool)
	go func() { done <- sem.acquire(ctx, 5) }()
	waitForWaiters(t, sem, 1)

	cancel()
	assert.False(t, <-done)
	waitForWaiters(t, sem, 0)

	sem.release()
	require.True(t, sem.acquire(t.Context(), 0), "the slot is free again")
}

func TestPrioritySemaphoreRejectsDoneContext(t *testing.T) {
	sem := newPrioritySemaphore(1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	assert.False(t, sem.acquire(ctx, 1))

	sem.mu.Lock()
	defer sem.mu.Unlock()
	assert.Equal(t, 1, sem.free)
}

func TestPrioritySemaphoreBoundsConcurrency(t *testing.T) {
	const slots = 3

	sem := newPrioritySemaphore(slots)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var (
		active, peak atomic.Int32
		wg           sync.WaitGroup
	)
	for i := range 200 {
		wg.Go(func() {
			waitCtx := ctx
			if i%7 == 0 {
				short, stop := context.WithTimeout(ctx, time.Duration(i%3)*time.Millisecond)
				defer stop()
				waitCtx = short
			}
			if !sem.acquire(waitCtx, int64(i%5)) {
				return
			}
			defer sem.release()

			now := active.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(50 * time.Microsecond)
			active.Add(-1)
		})
	}
	wg.Wait()

	assert.LessOrEqual(t, peak.Load(), int32(slots))

	sem.mu.Lock()
	defer sem.mu.Unlock()
	assert.Equal(t, slots, sem.free, "every slot is returned")
	assert.Empty(t, sem.waiters)
}
