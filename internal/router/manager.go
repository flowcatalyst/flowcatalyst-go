package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/queue"
)

// defaultPoolCode is the fallback pool for messages whose pool_code is empty
// or names a pool that isn't configured. Mirrors Java/Rust DEFAULT_POOL_CODE.
const defaultPoolCode = "DEFAULT-POOL"

// defaultPoolConcurrency matches Java/Rust DEFAULT_POOL_CONCURRENCY (20),
// used when the config doesn't define an explicit DEFAULT-POOL.
const defaultPoolConcurrency uint32 = 20

// consumerRestartDelay is the pause before re-spawning a stalled consumer —
// avoids a thundering-herd of reconnects when several stall at once. 1:1 with
// the Rust LifecycleConfig.consumer_restart_delay (5s).
const consumerRestartDelay = 5 * time.Second

// consumerRestartCriticalAfter escalates a repeatedly-stalling consumer's
// warning to CRITICAL after this many restart attempts. 1:1 with Rust.
const consumerRestartCriticalAfter = 10

// Manager owns the running consumers and pools and the routing between them.
//
// Topology (1:1 with the Rust QueueManager): N consumers (one per queue)
// each run a poll loop that hands batches to route(); route() assigns a
// batch id, drops external-requeue duplicates, then groups each message to
// the pool named by its pool_code (DEFAULT-POOL fallback) and submits it.
// Pools are passive — they do not own a queue. A pool processes messages
// routed from many queues, and ack/nack targets each message's SOURCE
// consumer (resolved by QueueIdentifier via resolveConsumer).
type Manager struct {
	mediator Mediator
	tracker  *InFlightTracker
	warnings atomic.Pointer[WarningService] // optional; set via SetWarnings. nil → no-op.

	mu        sync.Mutex
	pools     map[string]*Pool              // pool code → passive pool
	consumers map[string]*runningConsumer   // queue name → consumer + poll loop
	queues    map[string]common.QueueConfig // queue name → cfg (for publishers)
	wg        sync.WaitGroup

	// restartAttempts tracks consecutive restart attempts per stalled consumer
	// so a repeatedly-failing consumer escalates to a CRITICAL warning, and a
	// recovered one is cleared. Touched only by RestartStalledConsumers, which
	// the lifecycle watchdog calls from a single goroutine — no lock needed.
	restartAttempts map[string]int

	batchCounter atomic.Uint64

	pubMu      sync.Mutex
	publishers map[string]queue.Publisher // queue name → publisher (lazy)
}

type runningConsumer struct {
	consumer queue.Consumer
	cancel   context.CancelFunc
	queueCfg common.QueueConfig
	// lastPoll is the unix-nano of the most recent completed poll; a poll
	// loop wedged inside consumer.Poll leaves it stale, which the
	// consumer-restart watchdog (RestartStalledConsumers) detects.
	lastPoll atomic.Int64
}

// NewManager builds a manager. The mediator (which now owns the per-endpoint
// circuit breakers) is shared by all pools. tracker may be nil; if so, pools
// run without in-flight tracking.
func NewManager(mediator Mediator, tracker *InFlightTracker) *Manager {
	return &Manager{
		mediator:        mediator,
		tracker:         tracker,
		pools:           make(map[string]*Pool),
		consumers:       make(map[string]*runningConsumer),
		queues:          make(map[string]common.QueueConfig),
		publishers:      make(map[string]queue.Publisher),
		restartAttempts: make(map[string]int),
	}
}

// SetWarnings wires a WarningService so routing/capacity conditions surface on
// /warnings and into health. Opt-in; set once at startup before Start.
func (m *Manager) SetWarnings(ws *WarningService) { m.warnings.Store(ws) }

// resolveConsumer maps a message's origin queue to its consumer so a pool can
// ack/nack on the right queue. Returns nil if the queue was deregistered.
func (m *Manager) resolveConsumer(queueID string) queue.Consumer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rc, ok := m.consumers[queueID]; ok {
		return rc.consumer
	}
	return nil
}

// NackInFlight returns a stuck in-flight message to its source queue for
// redelivery, resolving the source consumer by queue identifier. Backs the
// StallDetector's force-NACK path (see StallConfig.ForceNackStalled). Errors if
// the source queue was deregistered. Mirrors the Rust force-nack-stalled path.
func (m *Manager) NackInFlight(ctx context.Context, queueID, receiptHandle string, delaySeconds uint32) error {
	c := m.resolveConsumer(queueID)
	if c == nil {
		return fmt.Errorf("no consumer for queue %q", queueID)
	}
	return c.Nack(ctx, receiptHandle, &delaySeconds)
}

// Consumers returns every running consumer (for the QueueHealthMonitor /
// metrics to call Metrics/Counters on).
func (m *Manager) Consumers() []queue.Consumer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]queue.Consumer, 0, len(m.consumers))
	for _, rc := range m.consumers {
		out = append(out, rc.consumer)
	}
	return out
}

// PoolStats returns one snapshot per running pool (map iteration order).
func (m *Manager) PoolStats() []PoolStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PoolStats, 0, len(m.pools))
	for _, p := range m.pools {
		out = append(out, p.Stats())
	}
	return out
}

// PoolCodes returns the codes of all currently registered pools.
func (m *Manager) PoolCodes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pools))
	for code := range m.pools {
		out = append(out, code)
	}
	return out
}

// Pool returns the running pool with the given code, or nil if absent.
func (m *Manager) Pool(code string) *Pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pools[code]
}

// QueueMetrics returns per-consumer broker attributes + counters. Calls
// Consumer.Metrics(ctx) which may do a broker round-trip.
func (m *Manager) QueueMetrics(ctx context.Context) []queue.Metrics {
	consumers := m.Consumers()
	out := make([]queue.Metrics, 0, len(consumers))
	for _, c := range consumers {
		mtr, err := c.Metrics(ctx)
		if err != nil {
			slog.Warn("queue metrics fetch failed", "queue", c.Identifier(), "err", err)
			continue
		}
		if mtr != nil {
			out = append(out, *mtr)
		}
	}
	return out
}

// QueueCounters returns process-local counters only (no broker round-trip).
func (m *Manager) QueueCounters() []queue.Metrics {
	consumers := m.Consumers()
	out := make([]queue.Metrics, 0, len(consumers))
	for _, c := range consumers {
		if mtr := c.Counters(); mtr != nil {
			out = append(out, *mtr)
		}
	}
	return out
}

// Publisher returns (and lazily caches) a publisher for manual/test message
// injection. The routing model publishes to a QUEUE (the message's pool_code
// then routes it), so `key` is resolved to a queue: a queue named `key` if
// one exists, else any registered queue (deterministic). Errors when no
// queue is registered.
func (m *Manager) Publisher(ctx context.Context, key string) (queue.Publisher, error) {
	qc, ok := m.queueForPublish(key)
	if !ok {
		return nil, fmt.Errorf("publisher: no queue registered for %q", key)
	}

	m.pubMu.Lock()
	if p, ok := m.publishers[qc.Name]; ok {
		m.pubMu.Unlock()
		return p, nil
	}
	m.pubMu.Unlock()

	pub, err := queue.NewPublisher(ctx, qc)
	if err != nil {
		return nil, fmt.Errorf("publisher: build for %q: %w", qc.Name, err)
	}
	m.pubMu.Lock()
	if existing, ok := m.publishers[qc.Name]; ok {
		m.pubMu.Unlock()
		return existing, nil
	}
	m.publishers[qc.Name] = pub
	m.pubMu.Unlock()
	return pub, nil
}

func (m *Manager) queueForPublish(key string) (common.QueueConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if qc, ok := m.queues[key]; ok {
		return qc, true
	}
	if len(m.queues) == 0 {
		return common.QueueConfig{}, false
	}
	names := make([]string, 0, len(m.queues))
	for n := range m.queues {
		names = append(names, n)
	}
	sort.Strings(names)
	return m.queues[names[0]], true
}

// UpdatePool applies a runtime config update to an existing pool. See the
// PUT /monitoring/pools/{poolCode} handler. Concurrency==0 leaves it
// unchanged; setRateLimit toggles whether rateLimitPerMinute is applied.
func (m *Manager) UpdatePool(code string, concurrency uint32, rateLimitPerMinute *uint32, setRateLimit bool) bool {
	pool := m.Pool(code)
	if pool == nil {
		return false
	}
	if concurrency != 0 {
		if !pool.UpdateConcurrency(concurrency) {
			return false
		}
	}
	if setRateLimit {
		pool.UpdateRateLimit(rateLimitPerMinute)
	}
	return true
}

// route handles one poll batch from a consumer (1:1 with Rust route_batch).
// It assigns the batch id, ACK-drops external-requeue duplicates, then routes
// each message to the pool named by its pool_code (DEFAULT-POOL fallback) and
// submits it. ack/nack of the eventual outcome is the pool's job, against the
// message's source consumer.
//
// Note: Rust's Phase-0 "pending-delete" (re-ACK of a message that was
// processed but whose ACK failed) is handled at the consumer level here, not
// in route(). Broker-redelivery dedup is handled at process time in
// pool.processOne (functionally equivalent to Rust's route-time check).
func (m *Manager) route(ctx context.Context, msgs []common.QueuedMessage, source queue.Consumer) {
	if len(msgs) == 0 {
		return
	}
	batchID := strconv.FormatUint(m.batchCounter.Add(1), 10)

	for i := range msgs {
		msg := msgs[i]
		msg.BatchID = batchID

		// R3: broker redelivery of an in-flight message — the SAME broker
		// MessageId is already being processed or retried in-pipeline (e.g. an
		// SQS visibility-timeout redelivery of a message the pool is backing
		// off on). Swap the freshest receipt handle onto the in-flight entry
		// (so the eventual ACK/DeleteMessage uses a valid handle) and DROP this
		// copy. There is nothing to release — SQS Nack is a no-op — so dropping
		// it is correct; the original copy still owns the work.
		if m.tracker != nil && m.tracker.SwapReceiptIfInFlight(msg.BrokerMessageID, msg.ReceiptHandle) {
			slog.Debug("broker redelivery of in-flight message; swapped receipt handle, dropped copy",
				"msg", msg.Message.ID, "queue", source.Identifier())
			continue
		}

		// R4: external-requeue dedup — the same application message ID is
		// already in flight under a DIFFERENT broker id (an external process
		// requeued a message stuck in QUEUED). ACK this copy to remove it.
		if m.tracker != nil && m.tracker.IsExternalRequeue(msg.Message.ID, msg.BrokerMessageID) {
			slog.Info("external requeue detected; ACKing duplicate", "msg", msg.Message.ID, "queue", source.Identifier())
			if err := source.Ack(ctx, msg.ReceiptHandle); err != nil {
				slog.Warn("ack (external requeue) failed", "msg", msg.Message.ID, "err", err)
			}
			continue
		}

		pool := m.poolForMessage(msg)
		if pool == nil {
			// No pool at all (not even DEFAULT-POOL configured) — NACK so the
			// message is redelivered once a pool exists.
			slog.Warn("no pool available for message; nacking", "msg", msg.Message.ID, "pool_code", msg.Message.PoolCode)
			if err := source.Nack(ctx, msg.ReceiptHandle, ptrU32(5)); err != nil {
				slog.Warn("nack (no pool) failed", "msg", msg.Message.ID, "err", err)
			}
			continue
		}
		pool.submit(ctx, msg)
	}
}

// poolForMessage resolves the destination pool for a message: the pool named
// by pool_code, or DEFAULT-POOL when pool_code is empty or unknown (with a
// routing warning for the unknown case). Returns nil only if even
// DEFAULT-POOL is absent.
func (m *Manager) poolForMessage(msg common.QueuedMessage) *Pool {
	code := msg.Message.PoolCode
	m.mu.Lock()
	defer m.mu.Unlock()
	if code != "" {
		if p, ok := m.pools[code]; ok {
			return p
		}
		// Unknown pool code → DEFAULT-POOL, surfaced as a Routing warning
		// (1:1 with the Rust router's unknown-pool_code warning).
		slog.Warn("no pool found for pool_code; routing to DEFAULT-POOL",
			"msg", msg.Message.ID, "pool_code", code, "default_pool", defaultPoolCode)
		if w := m.warnings.Load(); w != nil {
			w.Add(WarningCategoryRouting, WarningWarning,
				fmt.Sprintf("no pool for pool_code %q; routed to %s", code, defaultPoolCode), "router")
		}
	}
	return m.pools[defaultPoolCode]
}

// runConsumer is the per-consumer poll loop (1:1 with Rust
// spawn_consumer_poll_task). It pauses when all pools are at capacity to
// avoid a hot poll-defer loop, polls up to 10, routes the batch, and paces
// itself by batch fullness.
func (m *Manager) runConsumer(ctx context.Context, rc *runningConsumer) {
	defer m.wg.Done()
	const maxPoll = 10
	wasFull := false
	for {
		if ctx.Err() != nil {
			return
		}
		// Backpressure: if every pool is full, wait rather than poll. Surface the
		// transition into full as a PoolCapacity warning (once per full period,
		// not every tick, to avoid flooding /warnings).
		if !m.hasPoolCapacity() {
			if !wasFull {
				wasFull = true
				if w := m.warnings.Load(); w != nil {
					w.Add(WarningCategoryPoolCapacity, WarningWarning,
						fmt.Sprintf("all pools at capacity; pausing %s", rc.consumer.Identifier()), "router")
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		wasFull = false

		msgs, err := rc.consumer.Poll(ctx, maxPoll)
		rc.lastPoll.Store(time.Now().UnixNano()) // progress heartbeat for the restart watchdog
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("consumer poll error", "queue", rc.consumer.Identifier(), "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		if len(msgs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		m.route(ctx, msgs, rc.consumer)

		// Full batch → re-poll immediately (more likely waiting). Partial →
		// brief pause (queue draining). Mirrors Rust's pacing.
		if len(msgs) < maxPoll {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

// hasPoolCapacity reports whether at least one pool has room in its
// pre-dispatch buffer. With no pools, returns false (nothing to route to).
func (m *Manager) hasPoolCapacity() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pools) == 0 {
		return false
	}
	for _, p := range m.pools {
		capacity := p.Concurrency() * queueCapacityMultiplier
		if capacity < minQueueCapacity {
			capacity = minQueueCapacity
		}
		if p.QueueSize() < capacity {
			return true
		}
	}
	return false
}

// Reconfigure applies a new RouterConfig: reconciles pools (by code) and
// consumers (by queue name), starting/stopping/updating as needed. A
// DEFAULT-POOL is always ensured. Hot-reloadable.
func (m *Manager) Reconfigure(ctx context.Context, cfg common.RouterConfig) error {
	wantPools := make(map[string]common.PoolConfig, len(cfg.ProcessingPools)+1)
	for _, p := range cfg.ProcessingPools {
		wantPools[p.Code] = p
	}
	if _, ok := wantPools[defaultPoolCode]; !ok {
		wantPools[defaultPoolCode] = common.PoolConfig{Code: defaultPoolCode, Concurrency: defaultPoolConcurrency}
	}
	wantQueues := make(map[string]common.QueueConfig, len(cfg.Queues))
	for _, q := range cfg.Queues {
		wantQueues[q.Name] = q
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Pools: stop removed, update existing, start new. (Pools are passive —
	// stopping just flips the flag so in-flight submits NACK.)
	for code, p := range m.pools {
		if _, ok := wantPools[code]; !ok {
			slog.Info("manager: stopping pool", "code", code)
			p.Stop()
			delete(m.pools, code)
		}
	}
	for code, pc := range wantPools {
		if p, ok := m.pools[code]; ok {
			rate := uint32(0)
			if pc.RateLimitPerMinute != nil {
				rate = *pc.RateLimitPerMinute
			}
			p.SetRateLimit(rate)
			if pc.Concurrency != 0 {
				p.UpdateConcurrency(pc.Concurrency)
			}
			continue
		}
		m.pools[code] = NewPool(pc, m.mediator, m.tracker, m.resolveConsumer)
	}

	// Consumers: stop removed/changed, start new. A queue config change
	// (URI/connections/visibility) restarts that consumer.
	for name, rc := range m.consumers {
		if wq, ok := wantQueues[name]; !ok || wq != rc.queueCfg {
			slog.Info("manager: stopping consumer", "queue", name)
			rc.cancel()
			rc.consumer.Stop()
			delete(m.consumers, name)
			delete(m.queues, name)
		}
	}
	for name, qc := range wantQueues {
		if _, ok := m.consumers[name]; ok {
			continue
		}
		consumer, err := queue.NewConsumer(ctx, qc)
		if err != nil {
			return fmt.Errorf("build consumer for queue %s: %w", name, err)
		}
		cctx, cancel := context.WithCancel(ctx)
		rc := &runningConsumer{consumer: consumer, cancel: cancel, queueCfg: qc}
		rc.lastPoll.Store(time.Now().UnixNano())
		m.consumers[name] = rc
		m.queues[name] = qc
		m.wg.Add(1)
		go m.runConsumer(cctx, rc)
	}
	return nil
}

// Shutdown cancels all consumer poll loops, stops the pools, and waits for
// the poll loops to exit.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	for _, rc := range m.consumers {
		rc.cancel()
		rc.consumer.Stop()
	}
	for _, p := range m.pools {
		p.Stop()
	}
	m.consumers = nil
	m.pools = nil
	m.mu.Unlock()

	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RestartStalledConsumers re-spawns any consumer whose poll loop has not
// completed a poll within threshold — a wedged loop (stuck inside
// consumer.Poll) leaves its lastPoll stale. The stalled consumer is
// cancelled and its connection rebuilt with a fresh poll loop. Returns the
// number restarted. Mirrors the Rust LifecycleManager consumer auto-restart.
func (m *Manager) RestartStalledConsumers(ctx context.Context, threshold time.Duration) int {
	if threshold <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-threshold).UnixNano()

	type candidate struct {
		name string
		qc   common.QueueConfig
		old  *runningConsumer
	}
	m.mu.Lock()
	var stalled []candidate
	for name, rc := range m.consumers {
		if lp := rc.lastPoll.Load(); lp != 0 && lp < cutoff {
			stalled = append(stalled, candidate{name: name, qc: rc.queueCfg, old: rc})
		}
	}
	m.mu.Unlock()

	// Clear restart-attempt counters for consumers that have recovered (are no
	// longer stalled), so a transient stall doesn't escalate a later, unrelated
	// one. 1:1 with the Rust lifecycle's healthy-consumer cleanup.
	stalledSet := make(map[string]struct{}, len(stalled))
	for _, c := range stalled {
		stalledSet[c.name] = struct{}{}
	}
	for name := range m.restartAttempts {
		if _, ok := stalledSet[name]; !ok {
			delete(m.restartAttempts, name)
		}
	}

	if len(stalled) == 0 {
		return 0
	}

	restarted := 0
	for _, c := range stalled {
		attempts := m.restartAttempts[c.name]
		// Escalate to CRITICAL once a consumer keeps stalling across many
		// restarts (1:1 with Rust: Critical after 10 attempts).
		severity := WarningWarning
		if attempts >= consumerRestartCriticalAfter {
			severity = WarningCritical
		}
		if w := m.warnings.Load(); w != nil {
			w.Add(WarningCategoryConsumerHealth, severity,
				fmt.Sprintf("Consumer %s is stalled, restart attempt %d", c.name, attempts+1),
				"router")
		}
		slog.Warn("stalled consumer detected, attempting restart",
			"queue", c.name, "attempt", attempts+1, "stalled_threshold", threshold)

		// Brief pause before reconnecting — avoids a thundering herd when
		// several consumers stall together (1:1 with Rust consumer_restart_delay).
		// Abort cleanly on shutdown.
		select {
		case <-ctx.Done():
			return restarted
		case <-time.After(consumerRestartDelay):
		}

		c.old.cancel()
		c.old.consumer.Stop()

		consumer, err := queue.NewConsumer(ctx, c.qc)
		if err != nil {
			slog.Error("failed to rebuild stalled consumer", "queue", c.name, "err", err)
			continue
		}
		cctx, cancel := context.WithCancel(ctx)
		rc := &runningConsumer{consumer: consumer, cancel: cancel, queueCfg: c.qc}
		rc.lastPoll.Store(time.Now().UnixNano())

		m.mu.Lock()
		// Only replace if the entry is still the one we found stalled — a
		// concurrent Reconfigure may have already swapped or removed it.
		if cur, ok := m.consumers[c.name]; ok && cur == c.old {
			m.consumers[c.name] = rc
			m.mu.Unlock()
			m.wg.Add(1)
			go m.runConsumer(cctx, rc)
			m.restartAttempts[c.name]++
			restarted++
		} else {
			m.mu.Unlock()
			cancel()
			consumer.Stop()
		}
	}
	return restarted
}

// PoolCount returns the count of running pools (for /health or /metrics).
func (m *Manager) PoolCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pools)
}
