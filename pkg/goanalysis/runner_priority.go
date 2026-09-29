package goanalysis

import (
	"container/heap"
	"context"
	"sync"
)

// packagePriorities returns, for each package, the cost of the heaviest chain
// from it up to an initial package: its own cost plus that of its most
// expensive dependent chain. Packages on the critical path of the import graph
// get the highest priority, so they are not left to finish last on one core.
func packagePriorities(pkgs map[*loadingPackage]struct{}) map[*loadingPackage]int64 {
	dependents := make(map[*loadingPackage][]*loadingPackage, len(pkgs))
	for lp := range pkgs {
		for _, imp := range lp.imports {
			dependents[imp] = append(dependents[imp], lp)
		}
	}

	priorities := make(map[*loadingPackage]int64, len(pkgs))

	var visit func(*loadingPackage) int64
	visit = func(lp *loadingPackage) int64 {
		if priority, ok := priorities[lp]; ok {
			return priority
		}

		var heaviest int64
		for _, dependent := range dependents[lp] {
			heaviest = max(heaviest, visit(dependent))
		}

		priority := packageCost(lp) + heaviest
		priorities[lp] = priority

		return priority
	}

	for lp := range pkgs {
		visit(lp)
	}

	return priorities
}

func packageCost(lp *loadingPackage) int64 {
	return int64(len(lp.pkg.CompiledGoFiles)) + 1
}

// prioritySemaphore bounds concurrent work like a counting semaphore, but hands
// each released slot to the highest-priority waiter, first come first served
// among equal priorities.
type prioritySemaphore struct {
	mu      sync.Mutex
	free    int
	waiters semWaiters
	seq     uint64
}

func newPrioritySemaphore(slots int) *prioritySemaphore {
	return &prioritySemaphore{free: slots}
}

// acquire blocks until a slot is granted or ctx is done; it reports whether the
// caller holds a slot and must release it.
func (s *prioritySemaphore) acquire(ctx context.Context, priority int64) bool {
	if ctx.Err() != nil {
		return false
	}

	s.mu.Lock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		s.mu.Unlock()

		return true
	}

	waiter := &semWaiter{priority: priority, seq: s.seq, ready: make(chan struct{})}
	s.seq++
	heap.Push(&s.waiters, waiter)
	s.mu.Unlock()

	select {
	case <-waiter.ready:
		return true
	case <-ctx.Done():
		s.mu.Lock()
		granted := waiter.index < 0
		if !granted {
			heap.Remove(&s.waiters, waiter.index)
		}
		s.mu.Unlock()

		if granted {
			s.release()
		}

		return false
	}
}

func (s *prioritySemaphore) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.waiters) > 0 {
		waiter := heap.Pop(&s.waiters).(*semWaiter)
		close(waiter.ready)

		return
	}

	s.free++
}

type semWaiter struct {
	priority int64
	seq      uint64
	ready    chan struct{}
	index    int
}

type semWaiters []*semWaiter

func (w semWaiters) Len() int { return len(w) }

func (w semWaiters) Less(i, j int) bool {
	if w[i].priority != w[j].priority {
		return w[i].priority > w[j].priority
	}

	return w[i].seq < w[j].seq
}

func (w semWaiters) Swap(i, j int) {
	w[i], w[j] = w[j], w[i]
	w[i].index = i
	w[j].index = j
}

func (w *semWaiters) Push(x any) {
	waiter := x.(*semWaiter)
	waiter.index = len(*w)
	*w = append(*w, waiter)
}

func (w *semWaiters) Pop() any {
	old := *w
	n := len(old)
	waiter := old[n-1]
	old[n-1] = nil
	waiter.index = -1
	*w = old[:n-1]

	return waiter
}
