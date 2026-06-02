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
}

// groupQueue is the per-message-group buffer: a single strict FIFO. A message
// group is an ordering contract, so there is deliberately NO priority lane —
// letting a "high priority" message jump ahead of an earlier one in the same
// group would defeat in-order delivery. (Message.HighPriority is a queue-level
// concern, not an intra-group one, and does not reorder here.) On a retryable
// failure the drainer re-inserts the message at the FRONT (enqueueFront) so the
// failed message is the next one attempted — never overtaken by a later one.
type groupQueue struct {
	msgs    []common.QueuedMessage
	working bool
}

// pop returns the next message to dispatch (FIFO) and whether the queue is now
// empty. Caller holds p.mu.
func (gq *groupQueue) pop() (common.QueuedMessage, bool) {
	m := gq.msgs[0]
	gq.msgs = gq.msgs[1:]
	return m, len(gq.msgs) == 0
}

// empty reports whether the queue holds no pending messages. Caller
// holds p.mu.
func (gq *groupQueue) empty() bool {
	return len(gq.msgs) == 0
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

// ackTracked / nackMsg resolve a message's source consumer and apply the
// terminal action there — a pool processes messages routed from many queues, so
// the action must target the queue the message arrived on. A missing consumer
// (deregistered queue) is logged and skipped.

// ackTracked ACKs a terminally-resolved message (2xx success, or 4xx which we
// drop to avoid an infinite client-error loop) using the FRESHEST receipt
// handle recorded on its in-flight entry — a broker redelivery may have swapped
// it since dispatch, and the handle captured at dispatch time can be stale by
// the time a long in-pipeline retry finally succeeds. It then clears the entry.
func (p *Pool) ackTracked(ctx context.Context, qm common.QueuedMessage) {
	receipt := qm.ReceiptHandle
	if p.tracker != nil {
		if rh, ok := p.tracker.CurrentReceipt(qm.Message.ID, qm.BrokerMessageID); ok {
			receipt = rh
		}
	}
	if c := p.consumerFor(qm); c != nil {
		if err := c.Ack(ctx, receipt); err != nil {
			slog.Warn("ack failed", "msg", qm.Message.ID, "err", err)
		}
	} else {
		slog.Warn("ack: no consumer for queue", "queue", qm.QueueIdentifier, "msg", qm.Message.ID)
	}
	if p.tracker != nil {
		p.tracker.Remove(qm.Message.ID, qm.BrokerMessageID)
	}
}

// nackMsg releases a message back to its source broker. It is used only for the
// non-retryable control paths (pool stopped, pool at capacity, shutdown before
// dispatch). NB: on SQS, Nack is a deliberate no-op — the message simply stays
// invisible until its visibility timeout lapses and is then redelivered fresh.
// Retryable mediation failures do NOT go here; they are retried in-pipeline.
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

// submit routes one polled message. It runs capacity backpressure, then
// branches on DispatchMode: IMMEDIATE-mode messages (the default,
// RequiresOrdering()==false) dispatch concurrently via runImmediate (one worker
// per message, bounded only by the pool semaphore), while ordered modes enqueue
// into the per-group FIFO buffer and drain serially. Retryable failures are
// retried in-pipeline (see processOne / drainGroup / runImmediate), so ordering
// is preserved by re-inserting a failed message at the FRONT of its group
// rather than by cascade-NACKing the rest of a batch.
func (p *Pool) submit(ctx context.Context, m common.QueuedMessage) {
	// Reject when the pool is stopping.
	if p.stopped.Load() {
		p.nackMsg(ctx, m, ptrU32(10), "pool stopped")
		return
	}
	// Capacity backpressure: NACK (delay 10) when the pre-dispatch buffer is
	// already at capacity = max(concurrency*20, 50).
	capacity := p.concurrency.Load() * queueCapacityMultiplier
	if capacity < minQueueCapacity {
		capacity = minQueueCapacity
	}
	if p.queueSize.Load() >= capacity {
		p.nackMsg(ctx, m, ptrU32(10), "pool at capacity")
		return
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
// acquire a pool semaphore slot, then process it. IMMEDIATE messages have no
// group buffer, so a retryable failure re-dispatches the same message after the
// backoff (one chained goroutine per failing message — sequential, not a leak),
// keeping it in-pipeline rather than releasing it to the broker.
func (p *Pool) runImmediate(ctx context.Context, m common.QueuedMessage) {
	sem := p.loadSem()
	select {
	case <-ctx.Done():
		// Shutdown before we could start. The message was never tracked/attempted
		// (or its entry persists across the retry) — NACK is a no-op on SQS, so
		// the broker redelivers it after the visibility timeout.
		p.queueSize.Add(^uint32(0))
		p.nackMsg(ctx, m, ptrU32(10), "shutdown before dispatch")
		return
	case sem <- struct{}{}:
	}
	p.queueSize.Add(^uint32(0)) // now active, not queued
	result, retryAfter := func() (processResult, time.Duration) {
		defer func() { <-sem }() // release on every exit path (acquired above)
		return p.processOne(ctx, m)
	}()
	if result != processRetry {
		return
	}
	// Retry in-pipeline: wait out the backoff, then re-dispatch. The in-flight
	// tracker entry is kept (so redeliveries are deduped against it), and
	// Attempts grows the backoff and tells processOne not to re-track.
	m.Attempts++
	p.queueSize.Add(1) // re-queued (pre-dispatch) for the duration of the backoff
	go func() {
		select {
		case <-ctx.Done():
			p.queueSize.Add(^uint32(0))
			return
		case <-time.After(retryAfter):
		}
		p.runImmediate(ctx, m)
	}()
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

// enqueue appends a newly-arrived message to the BACK of its group's FIFO.
func (p *Pool) enqueue(group string, m common.QueuedMessage) {
	p.mu.Lock()
	gq, ok := p.groupQs[group]
	if !ok {
		gq = &groupQueue{}
		p.groupQs[group] = gq
	}
	gq.msgs = append(gq.msgs, m)
	p.mu.Unlock()
	p.queueSize.Add(1)
}

// enqueueFront puts a message back at the HEAD of its group's FIFO so that a
// retry is the NEXT message attempted — never overtaken by a later message in
// the same group. Used only by the ordered drainer on a retryable failure.
func (p *Pool) enqueueFront(group string, m common.QueuedMessage) {
	p.mu.Lock()
	gq, ok := p.groupQs[group]
	if !ok {
		gq = &groupQueue{}
		p.groupQs[group] = gq
	}
	gq.msgs = append([]common.QueuedMessage{m}, gq.msgs...)
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
		result, retryAfter := func() (processResult, time.Duration) {
			defer func() { <-sem }()
			return p.processOne(ctx, msg)
		}()

		if result == processRetry {
			// Preserve FIFO: re-insert the failed message at the FRONT of its
			// group so it is the next one attempted, then wait out the backoff
			// before the next attempt (holding no semaphore slot). The single
			// drainer + front re-insert blocks the whole group on this message
			// until it succeeds — the intended ordered-delivery (head-of-line)
			// semantic. The in-flight tracker entry is kept across the retry.
			msg.Attempts++
			p.enqueueFront(group, msg)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryAfter):
			}
		}
	}
}

// processResult is processOne's verdict, consumed by the caller (drainGroup /
// runImmediate) to decide whether to ACK-and-drop, retry in-pipeline, or
// discard a deduplicated copy.
type processResult int

const (
	// processDone — terminally resolved (ACKed on 2xx success or 4xx drop);
	// the in-flight entry has been cleared and the message leaves the pipeline.
	processDone processResult = iota
	// processRetry — retryable failure; the in-flight entry was KEPT and the
	// caller should re-dispatch after the returned backoff (front of the group
	// for ordered, delayed re-spawn for IMMEDIATE). Never released to the broker.
	processRetry
	// processDuplicate — this copy was a broker redelivery of a message already
	// in-flight (process-time backstop for the route-time swap); the receipt
	// handle was swapped onto the original entry and this copy is dropped.
	processDuplicate
)

const (
	// retryMinDelay / retryMaxDelay bound the in-pipeline backoff; panicRetryDelay
	// is the fixed backoff after a recovered panic.
	retryMinDelay   = 100 * time.Millisecond
	retryMaxDelay   = 5 * time.Minute
	panicRetryDelay = 10 * time.Second
)

// retryDelay computes the in-pipeline backoff before the next attempt:
// exponential in the attempt count (starting at retryMinDelay), with any
// server-requested delay (Retry-After on 429, the breaker reset on circuit-open,
// the 5xx retry hint) applied as a floor, capped at retryMaxDelay.
func retryDelay(attempts uint, outcomeDelaySec int) time.Duration {
	shift := attempts
	if shift > 12 { // cap the shift so the bit-shift can't overflow
		shift = 12
	}
	d := retryMinDelay << shift
	if floor := time.Duration(outcomeDelaySec) * time.Second; d < floor {
		d = floor
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// processOne runs the per-message pipeline: track (first dispatch only), rate
// limit, mediate, and resolve by outcome. It does NOT release messages to the
// broker on failure — a retryable outcome keeps the in-flight entry and returns
// processRetry so the caller retries in-pipeline (preserving order for grouped
// messages). Only a terminal 2xx/4xx ACKs and clears the entry.
func (p *Pool) processOne(ctx context.Context, qm common.QueuedMessage) (result processResult, retryAfter time.Duration) {
	p.activeWorkers.Add(1)
	defer p.activeWorkers.Add(^uint32(0)) // atomic decrement

	// Panic isolation: a panic mid-mediation must not crash the process (an
	// unrecovered panic in a goroutine takes down the program) or strand the
	// message. Recover and retry in-pipeline — the in-flight entry is kept, so
	// the redelivery dedup stays intact and the worker survives. Named returns
	// let the deferred recover set the verdict.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in processOne; retrying in-pipeline",
				"msg", qm.Message.ID, "panic", r)
			result = processRetry
			retryAfter = panicRetryDelay
			if p.tracker != nil {
				p.tracker.MarkRetrying(qm.Message.ID, qm.BrokerMessageID)
			}
		}
	}()

	// First dispatch records the message in-flight (and short-circuits a
	// concurrent broker redelivery that slipped past the route-time swap). A
	// retry re-dispatch (Attempts>0) is already tracked — keep the existing
	// entry (which may have had its receipt handle swapped by a redelivery)
	// and skip the insert.
	if p.tracker != nil && qm.Attempts == 0 {
		im := common.NewInFlightMessage(&qm.Message, qm.BrokerMessageID, qm.QueueIdentifier, qm.BatchID, qm.ReceiptHandle)
		if _, isDuplicate := p.tracker.Insert(im); isDuplicate {
			slog.Debug("duplicate redelivery (process-time backstop); dropped copy",
				"msg", qm.Message.ID, "queue", qm.QueueIdentifier)
			return processDuplicate, 0
		}
	}

	// Rate limit (per-pool token bucket). Record a rate-limited event when the
	// limiter actually held us back (current tokens exhausted).
	if p.limiter.IsLimited() {
		p.metrics.RecordRateLimited()
	}
	if err := p.limiter.Wait(ctx); err != nil {
		// Context cancelled mid-wait — keep the entry and retry in-pipeline.
		if p.tracker != nil {
			p.tracker.MarkRetrying(qm.Message.ID, qm.BrokerMessageID)
		}
		return processRetry, retryDelay(qm.Attempts, 5)
	}

	start := time.Now()
	outcome := p.mediator.Mediate(ctx, &qm.Message)
	durationMs := uint64(time.Since(start).Milliseconds())

	switch outcome.Result {
	case common.MediationSuccess:
		p.metrics.RecordSuccess(durationMs)
		p.ackTracked(ctx, qm)
		return processDone, 0

	case common.MediationErrorConfig:
		// The mediator already recorded the breaker success (4xx = reachable).
		// 4xx — ACK to avoid an infinite client-error retry loop. Do NOT trip
		// the breaker. Counted against total_failure (a non-success terminal).
		p.metrics.RecordFailure(durationMs)
		p.ackTracked(ctx, qm)
		return processDone, 0

	case common.MediationErrorProcess:
		// Transient (5xx/timeout): retry in-pipeline. Don't penalise the
		// all-time failure counter.
		p.metrics.RecordTransient(durationMs)
		return p.retry(qm, outcome.DelaySeconds)

	case common.MediationErrorConnection:
		p.metrics.RecordFailure(durationMs)
		return p.retry(qm, outcome.DelaySeconds)

	case common.MediationRateLimited:
		// 429 — retry in-pipeline honouring Retry-After; NOT a breaker failure.
		p.metrics.RecordRateLimited()
		return p.retry(qm, outcome.DelaySeconds)

	case common.MediationCircuitOpen:
		// Breaker open (decided by the mediator): no delivery was attempted.
		// Retry in-pipeline once the breaker reset timeout (carried in the
		// outcome) elapses.
		return p.retry(qm, outcome.DelaySeconds)
	}
	return processDone, 0
}

// retry marks the in-flight entry as retrying (so the stall detector / reaper
// skip it) and returns the processRetry verdict with the computed backoff.
func (p *Pool) retry(qm common.QueuedMessage, outcomeDelaySec int) (processResult, time.Duration) {
	if p.tracker != nil {
		p.tracker.MarkRetrying(qm.Message.ID, qm.BrokerMessageID)
	}
	return processRetry, retryDelay(qm.Attempts, outcomeDelaySec)
}

func ptrU32(v uint32) *uint32 { return &v }
