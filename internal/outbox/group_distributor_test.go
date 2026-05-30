package outbox

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func grpItem(id, group string) Item { return Item{ID: id, MessageGroup: &group} }

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Block-on-error: a failing item stops the group; the remaining items are
// released (onAbort), not dispatched ahead of the failed one.
func TestGroupDistributorBlockOnError(t *testing.T) {
	d := NewGroupDistributor(0, true)

	var mu sync.Mutex
	var dispatched, aborted []string
	rec := func(s *[]string, id string) { mu.Lock(); *s = append(*s, id); mu.Unlock() }

	items := []struct {
		id string
		ok bool
	}{{"A", true}, {"B", false}, {"C", true}}

	// Gate dispatch on `start` so all three are queued before the drain runs —
	// otherwise the drain could outrun submission and the test would be racy.
	start := make(chan struct{})
	var remaining int32 = int32(len(items))
	done := make(chan struct{})
	finish := func() {
		if atomic.AddInt32(&remaining, -1) == 0 {
			close(done)
		}
	}
	for _, it := range items {
		it := it
		d.Submit(grpItem(it.id, "g1"),
			func() bool { <-start; rec(&dispatched, it.id); finish(); return it.ok },
			func() { rec(&aborted, it.id); finish() })
	}
	close(start)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for group drain")
	}
	if want := []string{"A", "B"}; !eqStrings(dispatched, want) {
		t.Fatalf("dispatched=%v, want %v (C must NOT dispatch after B fails)", dispatched, want)
	}
	if want := []string{"C"}; !eqStrings(aborted, want) {
		t.Fatalf("aborted=%v, want %v (C released, not dispatched)", aborted, want)
	}
}

// With blockOnError disabled the group continues past a failure (legacy behaviour).
func TestGroupDistributorNoBlockWhenDisabled(t *testing.T) {
	d := NewGroupDistributor(0, false)
	var mu sync.Mutex
	var dispatched []string
	start := make(chan struct{})
	var remaining int32 = 3
	done := make(chan struct{})
	for _, id := range []string{"A", "B", "C"} {
		id := id
		ok := id != "B"
		d.Submit(grpItem(id, "g1"),
			func() bool {
				<-start
				mu.Lock()
				dispatched = append(dispatched, id)
				mu.Unlock()
				if atomic.AddInt32(&remaining, -1) == 0 {
					close(done)
				}
				return ok
			}, nil)
	}
	close(start)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	if want := []string{"A", "B", "C"}; !eqStrings(dispatched, want) {
		t.Fatalf("dispatched=%v, want all three (no block)", dispatched)
	}
}

// OB7: at most maxConcurrentGroups groups drain at once.
func TestGroupDistributorBoundedConcurrency(t *testing.T) {
	d := NewGroupDistributor(1, false)
	var running, maxRunning int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for _, g := range []string{"g1", "g2", "g3"} {
		wg.Add(1)
		d.Submit(grpItem("x", g), func() bool {
			cur := atomic.AddInt32(&running, 1)
			for {
				m := atomic.LoadInt32(&maxRunning)
				if cur <= m || atomic.CompareAndSwapInt32(&maxRunning, m, cur) {
					break
				}
			}
			<-release
			atomic.AddInt32(&running, -1)
			wg.Done()
			return true
		}, nil)
	}
	close(release)
	wg.Wait()
	if maxRunning > 1 {
		t.Fatalf("max concurrent groups = %d, want <= 1", maxRunning)
	}
}
