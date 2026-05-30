package router

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// Pool is a passive dispatch worker that respects:
//   - configured concurrency (semaphore-style worker cap),
//   - configured rate limit (per-pool token bucket),
//   - per-endpoint circuit breakers,
//   - FIFO ordering within message groups (when DispatchMode requires it).
//
// A Pool does NOT own a queue or poll. The Manager polls every queue and
// routes each message to the pool named by its pool_code (DEFAULT-POOL
// fallback), then calls Submit. Because a pool processes messages from many
// queues, ack/nack/defer target each message's SOURCE consumer, resolved by
// the message's QueueIdentifier via resolveConsumer.
type Pool struct {
	cfg      common.PoolConfig
	mediator Mediator
	limiter  *RateLimiter
	tracker  *InFlightTracker
	metrics  *PoolMetricsCollector

	// resolveConsumer maps a message's origin queue (QueueIdentifier) to the
	// consumer that delivered it. nil result → the queue was deregistered
	// between routing and processing; the action is skipped (logged).
	resolveConsumer func(queueID string) queue.Consumer

	// sem is the pool-wide concurrency semaphore. Buffered chan: a send
	// is an acquire (blocks when full); the matching receive is a
	// release. UpdateConcurrency swaps the chan atomically; workers
	// snapshot the chan locally before acquire so the matching release
	// goes back to the same chan they acquired from. During a resize,
	// in-flight workers continue against the old chan (effective
	// concurrency = old_in_flight + new_cap) and the old chan is GC'd
	// once those workers finish.
	sem         atomic.Value // chan struct{}
	concurrency atomic.Uint32

	mu      sync.Mutex
	groupQs map[string]*groupQueue // ordered FIFO queues per message-group

	queueSize     atomic.Uint32 // pending in groupQs (pre-dispatch)
	activeWorkers atomic.Uint32 // currently inside processOne

	stopped atomic.Bool

	// Batch+group FIFO cascade tracking. Mirrors Rust pool.rs
	// failed_batch_groups / batch_group_message_count: once any message in a
	// (batch_id, message_group) fails, the rest of that batch+group is NACKed
	// un-attempted to preserve FIFO ordering. The state clears once all of the
	// batch+group's messages have drained (count → 0). Only messages carrying
	// a BatchID participate.
	bgMu              sync.Mutex
	failedBatchGroups map[string]struct{}
	batchGroupCount   map[string]int
}

// groupQueue is the per-message-group buffer. High-priority messages
// (Message.HighPriority=true) drain ahead of regular messages within
// the same group; ordering within each priority class is FIFO. Mirrors
// the Rust MessageGroupHandler in crates/fc-router/src/pool.rs:99-140
// where high_priority and regular sit in separate VecDeques and the
// drain loop pops high_priority first.
type groupQueue struct {
	highPriority []common.QueuedMessage
	regular      []common.QueuedMessage
	working      bool
}

// pop returns the next message to dispatch (high-priority first) and
// whether the queue is now empty. Caller holds p.mu.
func (gq *groupQueue) pop() (common.QueuedMessage, bool) {
	if len(gq.highPriority) > 0 {
		m := gq.highPriority[0]
		gq.highPriority = gq.highPriority[1:]
		return m, len(gq.highPriority) == 0 && len(gq.regular) == 0
	}
	m := gq.regular[0]
	gq.regular = gq.regular[1:]
	return m, len(gq.highPriority) == 0 && len(gq.regular) == 0
}

// empty reports whether the queue holds no pending messages. Caller
// holds p.mu.
func (gq *groupQueue) empty() bool {
	return len(gq.highPriority) == 0 && len(gq.regular) == 0
}

// NewPool wires a pool. tracker may be nil; if so, in-flight tracking
// (and consequently stall detection + duplicate filtering) is disabled
// for messages handled by this pool.
func NewPool(cfg common.PoolConfig, mediator Mediator, tracker *InFlightTracker, resolveConsumer func(queueID string) queue.Consumer) *Pool {
	rate := uint32(0)
	if cfg.RateLimitPerMinute != nil {
		rate = *cfg.RateLimitPerMinute
	}
	concurrency := cfg.Concurrency
	if concurrency == 0 {
		// Rust parity (pool.rs): when concurrency is unset, derive it from
		// the rate limit — max(rate_per_minute/60, 1) — rather than always 1.
		concurrency = rate / 60
		if concurrency < 1 {
			concurrency = 1
		}
	}
	p := &Pool{
		cfg:             cfg,
		mediator:        mediator,
		limiter:         NewRateLimiter(rate),
		tracker:         tracker,
		metrics:         NewPoolMetricsCollector(),
		resolveConsumer: resolveConsumer,
		groupQs:         make(map[string]*groupQueue),

		failedBatchGroups: make(map[string]struct{}),
		batchGroupCount:   make(map[string]int),
	}
	p.sem.Store(make(chan struct{}, concurrency))
	p.concurrency.Store(concurrency)
	return p
}

// loadSem returns the current concurrency channel. Callers should
// snapshot it locally before an acquire so that the matching release
// receives from the same channel even if UpdateConcurrency swaps it
// mid-flight.
func (p *Pool) loadSem() chan struct{} { return p.sem.Load().(chan struct{}) }

// consumerFor resolves the source consumer for a message via its origin
// queue (QueueIdentifier); nil when that queue was deregistered between
// routing and processing.
func (p *Pool) consumerFor(qm common.QueuedMessage) queue.Consumer {
	if p.resolveConsumer == nil {
		return nil
	}
	return p.resolveConsumer(qm.QueueIdentifier)
}

// ackMsg / nackMsg / deferMsg resolve a message's source consumer and apply
// the terminal action there — a pool processes messages routed from many
// queues, so the action must target the queue the message arrived on. A
// missing consumer (deregistered queue) is logged and skipped.
func (p *Pool) ackMsg(ctx context.Context, qm common.QueuedMessage) {
	c := p.consumerFor(qm)
	if c == nil {
		slog.Warn("ack: no consumer for queue", "queue", qm.QueueIdentifier, "msg", qm.Message.ID)
		return
	}
	if err := c.Ack(ctx, qm.ReceiptHandle); err != nil {
		slog.Warn("ack failed", "msg", qm.Message.ID, "err", err)
	}
}

func (p *Pool) nackMsg(ctx context.Context, qm common.QueuedMessage, delay *uint32, reason string) {
	c := p.consumerFor(qm)
	if c == nil {
		slog.Warn("nack: no consumer for queue", "queue", qm.QueueIdentifier, "msg", qm.Message.ID, "reason", reason)
		return
	}
	if err := c.Nack(ctx, qm.ReceiptHandle, delay); err != nil {
		slog.Warn("nack failed", "reason", reason, "msg", qm.Message.ID, "err", err)
	}
}

func (p *Pool) deferMsg(ctx context.Context, qm common.QueuedMessage, delay *uint32, reason string) {
	c := p.consumerFor(qm)
	if c == nil {
		slog.Warn("defer: no consumer for queue", "queue", qm.QueueIdentifier, "msg", qm.Message.ID, "reason", reason)
		return
	}
	if err := c.Defer(ctx, qm.ReceiptHandle, delay); err != nil {
		slog.Warn("defer failed", "reason", reason, "msg", qm.Message.ID, "err", err)
	}
}

// Identifier is the pool code.
func (p *Pool) Identifier() string { return p.cfg.Code }

// SetRateLimit hot-swaps the rate-limit-per-minute value.
func (p *Pool) SetRateLimit(perMinute uint32) { p.limiter.SetRate(perMinute) }

// UpdateRateLimit is the API-facing alias for SetRateLimit. A nil value
// disables rate limiting (the Rust equivalent of `Option::None`).
func (p *Pool) UpdateRateLimit(perMinute *uint32) {
	var v uint32
	if perMinute != nil {
		v = *perMinute
	}
	p.limiter.SetRate(v)
}

// UpdateConcurrency swaps the semaphore to a new capacity. Returns false
// on n==0 (invalid). Existing in-flight workers continue to release into
// the old channel, which is GC'd once empty; new work uses the new
// channel. Effective concurrency during the transition is bounded by
// old_in_flight + new_cap; steady state is new_cap.
func (p *Pool) UpdateConcurrency(n uint32) bool {
	if n == 0 {
		return false
	}
	old := p.concurrency.Load()
	if n == old {
		return true
	}
	p.sem.Store(make(chan struct{}, n))
	p.concurrency.Store(n)
	slog.Info("pool concurrency updated", "pool", p.cfg.Code, "from", old, "to", n)
	return true
}

// Metrics exposes the pool's metric collector. The HTTP API hits this
// when building EnhancedPoolMetrics for /monitoring/pool-stats.
func (p *Pool) Metrics() *PoolMetricsCollector { return p.metrics }

// submit routes one polled message, 1:1 with Rust ProcessPool::submit. It
// runs the shared bookkeeping — capacity backpressure, batch+group FIFO
// count, and the early failed-group NACK — then branches on DispatchMode:
// IMMEDIATE-mode messages (the default, RequiresOrdering()==false) dispatch
// concurrently via runImmediate (one worker per message, bounded only by
// the pool semaphore), while ordered modes enqueue into the per-group FIFO
// buffer and drain serially.
func (p *Pool) submit(ctx context.Context, m common.QueuedMessage) {
	// Reject when the pool is stopping (Rust submit nacks on !running).
	if p.stopped.Load() {
		p.nackMsg(ctx, m, ptrU32(10), "pool stopped")
		return
	}
	// Capacity backpressure: NACK (delay 10) when the pre-dispatch buffer is
	// already at capacity = max(concurrency*20, 50). Mirrors Rust's submit.
	capacity := p.concurrency.Load() * queueCapacityMultiplier
	if capacity < minQueueCapacity {
		capacity = minQueueCapacity
	}
	if p.queueSize.Load() >= capacity {
		p.nackMsg(ctx, m, ptrU32(10), "pool at capacity")
		return
	}

	// Batch+group FIFO cascade (Rust pool.rs submit): count the message, and
	// if its batch+group already failed, NACK it now so ordering is preserved.
	if key, ok := p.batchKey(m); ok {
		p.bgIncrement(key)
		if p.bgFailed(key) {
			p.bgDecrementAndCleanup(key)
			p.nackMsg(ctx, m, ptrU32(10), "batch+group failed")
			return
		}
	}

	if !m.Message.DispatchMode.RequiresOrdering() {
		// IMMEDIATE: no ordering — dispatch concurrently. queueSize is
		// incremented here and decremented once the worker holds a semaphore
		// slot, so the "queued (pre-dispatch)" gauge mirrors the ordered path.
		p.queueSize.Add(1)
		go p.runImmediate(ctx, m)
		return
	}

	group := ""
	if m.Message.MessageGroupID != nil {
		group = *m.Message.MessageGroupID
	}
	p.enqueue(group, m)
	p.tryDrainGroup(ctx, group)
}

// runImmediate dispatches a single IMMEDIATE-mode message concurrently:
// acquire a pool semaphore slot, then process it. Unlike the ordered drain,
// an IMMEDIATE message does NOT mark its batch+group failed on error
// (cascade=false) — each message is independent. 1:1 with Rust
// spawn_immediate_task.
func (p *Pool) runImmediate(ctx context.Context, m common.QueuedMessage) {
	sem := p.loadSem()
	select {
	case <-ctx.Done():
		// Shutdown before we could start — release the bookkeeping and NACK
		// for prompt redelivery (mirrors Rust's semaphore-closed path).
		p.queueSize.Add(^uint32(0))
		if key, ok := p.batchKey(m); ok {
			p.bgDecrementAndCleanup(key)
		}
		p.nackMsg(ctx, m, ptrU32(10), "shutdown before dispatch")
		return
	case sem <- struct{}{}:
	}
	p.queueSize.Add(^uint32(0)) // now active, not queued
	defer func() { <-sem }()    // release on every exit path (acquired above)
	p.processOne(ctx, m, false)
}

// Stop signals the pool to exit. Run will return on its next loop.
func (p *Pool) Stop() { p.stopped.Store(true) }

// InFlight returns the count of messages currently in worker goroutines.
// Backward-compat shim for callers that still read inFlight as int64.
func (p *Pool) InFlight() int64 { return int64(p.activeWorkers.Load()) }

// ActiveWorkers returns the count of goroutines currently inside processOne.
func (p *Pool) ActiveWorkers() uint32 { return p.activeWorkers.Load() }

// QueueSize returns the count of messages buffered in group queues
// awaiting dispatch (pre-semaphore).
func (p *Pool) QueueSize() uint32 { return p.queueSize.Load() }

// Concurrency returns the current concurrency cap.
func (p *Pool) Concurrency() uint32 { return p.concurrency.Load() }

// RateLimitPerMinute returns the current rate-limit (or nil if disabled).
// Mirrors the way Rust's PoolStats reports the field.
func (p *Pool) RateLimitPerMinute() *uint32 {
	rate := p.limiter.Rate()
	if rate == 0 {
		return nil
	}
	return &rate
}

// IsRateLimited reports whether the limiter currently has no spare tokens.
func (p *Pool) IsRateLimited() bool { return p.limiter.IsLimited() }

// MessageGroupCount returns the number of message groups currently
// holding buffered messages.
func (p *Pool) MessageGroupCount() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return uint32(len(p.groupQs))
}

// Stats returns the dashboard-shaped snapshot of this pool.
func (p *Pool) Stats() PoolStats {
	concurrency := p.concurrency.Load()
	capacity := concurrency * queueCapacityMultiplier
	if capacity < minQueueCapacity {
		capacity = minQueueCapacity
	}
	m := p.metrics.Snapshot()
	return PoolStats{
		PoolCode:           p.cfg.Code,
		Concurrency:        concurrency,
		ActiveWorkers:      p.activeWorkers.Load(),
		QueueSize:          p.queueSize.Load(),
		QueueCapacity:      capacity,
		MessageGroupCount:  p.MessageGroupCount(),
		RateLimitPerMinute: p.RateLimitPerMinute(),
		IsRateLimited:      p.IsRateLimited(),
		Metrics:            &m,
		Histogram:          p.metrics.HistogramSnapshot(),
	}
}

// queueCapacityMultiplier and minQueueCapacity mirror the Java/Rust
// derivation: capacity = max(concurrency * 20, 50). Used by Stats() so
// the dashboard's "queue capacity" matches the reference implementations.
const (
	queueCapacityMultiplier uint32 = 20
	minQueueCapacity        uint32 = 50
)

// batchKey returns the batch+group tracking key for m and whether m
// participates in batch+group FIFO tracking (only messages with a BatchID
// do). The key is internal-only and never crosses the wire.
func (p *Pool) batchKey(m common.QueuedMessage) (string, bool) {
	if m.BatchID == "" {
		return "", false
	}
	group := ""
	if m.Message.MessageGroupID != nil {
		group = *m.Message.MessageGroupID
	}
	return m.BatchID + "\x00" + group, true
}

// bgIncrement records one more in-flight message for the batch+group.
func (p *Pool) bgIncrement(key string) {
	p.bgMu.Lock()
	p.batchGroupCount[key]++
	p.bgMu.Unlock()
}

// bgFailed reports whether the batch+group has already had a failure.
func (p *Pool) bgFailed(key string) bool {
	p.bgMu.Lock()
	_, ok := p.failedBatchGroups[key]
	p.bgMu.Unlock()
	return ok
}

// bgMarkFailed marks the batch+group failed so its remaining messages cascade-NACK.
func (p *Pool) bgMarkFailed(key string) {
	p.bgMu.Lock()
	p.failedBatchGroups[key] = struct{}{}
	p.bgMu.Unlock()
}

// bgDecrementAndCleanup decrements the batch+group's in-flight count and, when
// it reaches zero, drops both the count and failed-state entries so a later
// batch reusing the same key starts clean. Mirrors Rust
// decrement_and_cleanup_batch_group.
func (p *Pool) bgDecrementAndCleanup(key string) {
	p.bgMu.Lock()
	if n, ok := p.batchGroupCount[key]; ok {
		if n <= 1 {
			delete(p.batchGroupCount, key)
			delete(p.failedBatchGroups, key)
		} else {
			p.batchGroupCount[key] = n - 1
		}
	}
	p.bgMu.Unlock()
}

func (p *Pool) enqueue(group string, m common.QueuedMessage) {
	p.mu.Lock()
	gq, ok := p.groupQs[group]
	if !ok {
		gq = &groupQueue{}
		p.groupQs[group] = gq
	}
	if m.Message.HighPriority {
		gq.highPriority = append(gq.highPriority, m)
	} else {
		gq.regular = append(gq.regular, m)
	}
	p.mu.Unlock()
	p.queueSize.Add(1)
}

// tryDrainGroup starts a serial drainer for an ordered message group if
// none is running. Only ordered-mode messages (NEXT_ON_ERROR /
// BLOCK_ON_ERROR) reach here — IMMEDIATE-mode messages dispatch
// concurrently via runImmediate. The drainer processes one message per
// group at a time to preserve FIFO order, bounded across groups by `sem`.
func (p *Pool) tryDrainGroup(ctx context.Context, group string) {
	p.mu.Lock()
	gq := p.groupQs[group]
	if gq == nil || gq.working || gq.empty() {
		p.mu.Unlock()
		return
	}
	gq.working = true
	p.mu.Unlock()

	go p.drainGroup(ctx, group)
}

// drainGroup is the per-message-group worker goroutine spawned by
// tryDrainGroup. Drains one message at a time from gq.msgs (preserving
// FIFO order within the group), gated by the pool-wide `sem` semaphore.
//
// Exit conditions:
//   - the group buffer is empty (working flag flipped back to false).
//   - ctx is cancelled while waiting for a semaphore slot.
//
// Note: ctx cancellation between processOne calls does NOT stop the loop
// — only the semaphore-acquire select is ctx-aware. This is intentional;
// a cancellation mid-process is handled inside processOne / mediator.
func (p *Pool) drainGroup(ctx context.Context, group string) {
	for {
		p.mu.Lock()
		gq := p.groupQs[group]
		if gq == nil || gq.empty() {
			if gq != nil {
				gq.working = false
			}
			p.mu.Unlock()
			return
		}
		msg, _ := gq.pop()
		p.mu.Unlock()
		// Pop happens under p.mu before any await, so queueSize stays
		// consistent with what's actually buffered in groupQs.
		p.queueSize.Add(^uint32(0)) // atomic decrement

		// Batch+group FIFO cascade re-check (Rust pool.rs drain loop): the
		// group may have failed after this message was enqueued. If so, NACK
		// it instead of dispatching, preserving order.
		if key, ok := p.batchKey(msg); ok && p.bgFailed(key) {
			p.bgDecrementAndCleanup(key)
			p.nackMsg(ctx, msg, ptrU32(10), "batch+group failed")
			continue
		}

		// Acquire a concurrency slot. Snapshot the channel locally so a
		// resize between acquire and release doesn't cross channels.
		// Wakeup conditions:
		//   <-ctx.Done()         — shutdown; abandon this message.
		//   sem <- struct{}{}    — slot acquired; proceed.
		sem := p.loadSem()
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}

		// Release the slot per iteration even if processOne panics past its own
		// recover — a bare deferred <-sem would accumulate across the loop, so
		// scope it to a closure.
		func() {
			defer func() { <-sem }()
			p.processOne(ctx, msg, true)
		}()
	}
}

// processOne runs the full per-message pipeline: dedup, rate limit,
// circuit breaker, mediate, and ack/nack/defer by outcome. cascade controls
// whether a transient failure marks this message's batch+group failed so
// later ordered messages cascade-NACK — true for the ordered serial drain,
// false for IMMEDIATE-mode workers (which are independent of each other).
func (p *Pool) processOne(ctx context.Context, qm common.QueuedMessage, cascade bool) {
	p.activeWorkers.Add(1)
	defer p.activeWorkers.Add(^uint32(0)) // atomic decrement

	// Panic isolation (Rust Drop parity): resolution (ack/nack/defer) happens
	// after Mediate in the switch below, so a panic mid-mediation would strand
	// the message AND crash the process (an unrecovered panic in a goroutine
	// takes down the program). Recover, NACK for prompt redelivery, and keep
	// the worker alive. Runs after tracker.Remove / bgDecrement (registered
	// later, LIFO) so tracking is cleaned first; the panic window precedes
	// resolution, so this cannot double-resolve.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in processOne; NACKing for redelivery",
				"msg", qm.Message.ID, "panic", r)
			p.nackMsg(ctx, qm, ptrU32(10), "panic during processing")
		}
	}()

	// Batch+group FIFO cascade: this delivery was counted at enqueue, so
	// release its slot on every exit path. Mirrors Rust's post-process
	// decrement_and_cleanup_batch_group.
	bgKey, hasBatchGroup := p.batchKey(qm)
	if hasBatchGroup {
		defer p.bgDecrementAndCleanup(bgKey)
	}

	// Record in-flight (and short-circuit on duplicate redelivery).
	var imRef *common.InFlightMessage
	if p.tracker != nil {
		im := common.NewInFlightMessage(&qm.Message, qm.BrokerMessageID, qm.QueueIdentifier, qm.BatchID, qm.ReceiptHandle)
		existing, isDuplicate := p.tracker.Insert(im)
		if isDuplicate {
			// Broker redelivered while we're still processing. Swap the
			// receipt handle on the original tracker entry and return —
			// the original goroutine still owns the work.
			slog.Debug("duplicate redelivery; swapped receipt handle",
				"msg", existing.MessageID, "queue", qm.QueueIdentifier)
			return
		}
		imRef = im
		defer p.tracker.Remove(im.MessageID, im.BrokerMessageID)
	}
	_ = imRef // referenced via defer

	// Rate limit (per-pool token bucket). Record a rate-limited event
	// when the limiter actually held us back (current tokens exhausted).
	if p.limiter.IsLimited() {
		p.metrics.RecordRateLimited()
	}
	if err := p.limiter.Wait(ctx); err != nil {
		// Context cancelled mid-wait — defer the message and exit.
		p.deferMsg(ctx, qm, ptrU32(5), "rate-limit wait cancelled")
		return
	}

	start := time.Now()
	outcome := p.mediator.Mediate(ctx, &qm.Message)
	durationMs := uint64(time.Since(start).Milliseconds())

	switch outcome.Result {
	case common.MediationSuccess:
		p.metrics.RecordSuccess(durationMs)
		p.ackMsg(ctx, qm)

	case common.MediationErrorConfig:
		// The mediator already recorded the breaker success (4xx = reachable).
		// 4xx — ACK to avoid infinite retries. Do NOT trip the breaker.
		// (The destination is "healthy" in the sense that it responded.)
		// Rust counts this against total_failure (it was a non-success
		// terminal outcome), so we do the same.
		p.metrics.RecordFailure(durationMs)
		p.ackMsg(ctx, qm)

	case common.MediationErrorProcess:
		// Transient: message will be redelivered, so don't penalise
		// the all-time failure counter. Matches Rust record_transient.
		p.metrics.RecordTransient(durationMs)
		// FIFO cascade (ordered only): mark the batch+group failed so the
		// remaining ordered messages NACK.
		if cascade && hasBatchGroup {
			p.bgMarkFailed(bgKey)
		}
		p.nackMsg(ctx, qm, ptrU32(uint32(outcome.DelaySeconds)), "process error")

	case common.MediationErrorConnection:
		p.metrics.RecordFailure(durationMs)
		// FIFO cascade (ordered only): mark the batch+group failed so the
		// remaining ordered messages NACK.
		if cascade && hasBatchGroup {
			p.bgMarkFailed(bgKey)
		}
		p.nackMsg(ctx, qm, ptrU32(uint32(outcome.DelaySeconds)), "connection error")

	case common.MediationRateLimited:
		// 429 — defer with Retry-After; NOT a breaker failure.
		p.metrics.RecordRateLimited()
		p.deferMsg(ctx, qm, ptrU32(uint32(outcome.DelaySeconds)), "rate limited")

	case common.MediationCircuitOpen:
		// Breaker open (decided by the mediator): no delivery was attempted.
		// For ordered messages mark the batch+group failed to preserve FIFO,
		// then DEFER until the breaker reset timeout elapses (carried in the
		// outcome). 1:1 with the prior in-pool circuit-open path.
		if cascade && hasBatchGroup {
			p.bgMarkFailed(bgKey)
		}
		p.deferMsg(ctx, qm, ptrU32(uint32(outcome.DelaySeconds)), "circuit breaker open")
	}
}

func ptrU32(v uint32) *uint32 { return &v }
