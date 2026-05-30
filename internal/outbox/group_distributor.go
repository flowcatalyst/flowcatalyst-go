package outbox

import "sync"

// GroupDistributor enforces FIFO ordering within a message group. When
// blockOnError is set it also stops advancing a group as soon as one of its
// items fails — releasing the rest of that group's already-claimed items so
// they re-run in order behind the failed one, rather than being delivered
// ahead of a not-yet-succeeded item (the OB4 block-on-error guarantee). Items
// without a message_group dispatch in parallel (no ordering). The number of
// groups draining concurrently is capped by maxConcurrentGroups (OB7).
//
// This is the Go-architecture realisation of Rust's message_group_processor:
// the in-memory per-group queue + block-on-error semantics, driven by the
// DB-claim poll loop rather than a long-lived per-group actor.
type GroupDistributor struct {
	mu     sync.Mutex
	groups map[string]*groupQueue

	blockOnError bool
	sem          chan struct{} // group-concurrency semaphore; nil = unbounded
}

// groupWork is one item's dispatch + its release-on-block hook.
type groupWork struct {
	// dispatch runs the item and returns true on success (the group continues),
	// false on failure (with blockOnError, the group blocks).
	dispatch func() bool
	// onAbort releases an undispatched item back to PENDING when the group
	// blocks before reaching it.
	onAbort func()
}

type groupQueue struct {
	pending []groupWork
	running bool
}

// NewGroupDistributor builds a distributor. maxConcurrentGroups <= 0 leaves
// group concurrency unbounded; blockOnError stops a group on its first failing
// item (the rest are released to re-run behind it).
func NewGroupDistributor(maxConcurrentGroups int, blockOnError bool) *GroupDistributor {
	d := &GroupDistributor{groups: make(map[string]*groupQueue), blockOnError: blockOnError}
	if maxConcurrentGroups > 0 {
		d.sem = make(chan struct{}, maxConcurrentGroups)
	}
	return d
}

// Submit dispatches work for an item, respecting FIFO order within its
// message_group. dispatch returns true on success (the group continues) and
// false on failure (with blockOnError, the group's remaining items are released
// via onAbort and the group stops for this drain). Ungrouped items run
// immediately in parallel and ignore both signals.
func (d *GroupDistributor) Submit(item Item, dispatch func() bool, onAbort func()) {
	if item.MessageGroup == nil || *item.MessageGroup == "" {
		go dispatch()
		return
	}
	group := *item.MessageGroup
	d.mu.Lock()
	q, ok := d.groups[group]
	if !ok {
		q = &groupQueue{}
		d.groups[group] = q
	}
	q.pending = append(q.pending, groupWork{dispatch: dispatch, onAbort: onAbort})
	shouldStart := !q.running
	if shouldStart {
		q.running = true
	}
	d.mu.Unlock()

	if shouldStart {
		go d.drain(group)
	}
}

func (d *GroupDistributor) drain(group string) {
	// OB7: cap concurrently-draining groups. Blocking here (not the poll loop)
	// queues the group's drain until a slot frees.
	if d.sem != nil {
		d.sem <- struct{}{}
		defer func() { <-d.sem }()
	}
	for {
		d.mu.Lock()
		q := d.groups[group]
		if q == nil || len(q.pending) == 0 {
			if q != nil {
				q.running = false
			}
			d.mu.Unlock()
			return
		}
		work := q.pending[0]
		q.pending = q.pending[1:]
		d.mu.Unlock()

		if work.dispatch() || !d.blockOnError {
			continue
		}
		// Block-on-error: this item failed. Release the rest of the group's
		// claimed-but-undispatched items so they re-run in order on the next
		// poll, behind the failed item, and stop draining this group.
		d.mu.Lock()
		remaining := q.pending
		q.pending = nil
		q.running = false
		d.mu.Unlock()
		for _, w := range remaining {
			if w.onAbort != nil {
				w.onAbort()
			}
		}
		return
	}
}
