package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PausedConnectionCache caches the set of subscription IDs whose target
// connections are PAUSED. The poller filters jobs whose subscription
// matches; those jobs sit in PENDING until the connection is reactivated.
type PausedConnectionCache struct {
	pool *pgxpool.Pool
	ttl  time.Duration

	mu          sync.RWMutex
	paused      map[string]struct{}
	lastRefresh time.Time
}

// NewPausedConnectionCache wires the cache.
func NewPausedConnectionCache(pool *pgxpool.Pool, ttl time.Duration) *PausedConnectionCache {
	return &PausedConnectionCache{
		pool:        pool,
		ttl:         ttl,
		paused:      make(map[string]struct{}),
		lastRefresh: time.Now().Add(-2 * ttl), // force initial refresh
	}
}

// PausedSubscriptionIDs returns the cached set, refreshing if stale.
func (c *PausedConnectionCache) PausedSubscriptionIDs(ctx context.Context) (map[string]struct{}, error) {
	c.mu.RLock()
	if time.Since(c.lastRefresh) < c.ttl {
		out := make(map[string]struct{}, len(c.paused))
		for k := range c.paused {
			out[k] = struct{}{}
		}
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]struct{}, len(c.paused))
	for k := range c.paused {
		out[k] = struct{}{}
	}
	return out, nil
}

func (c *PausedConnectionCache) refresh(ctx context.Context) error {
	rows, err := c.pool.Query(ctx,
		`SELECT s.id FROM msg_subscriptions s
		   JOIN msg_connections c ON c.id = s.connection_id
		  WHERE c.status = 'PAUSED'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	paused := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		paused[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.paused = paused
	c.lastRefresh = time.Now()
	c.mu.Unlock()
	slog.Debug("paused connection cache refreshed", "paused_subscriptions", len(paused))
	return nil
}

// PendingJobPoller polls msg_dispatch_jobs for PENDING jobs ready to
// dispatch (next_retry_at <= NOW or null), filters them through the
// pause + block-on-error checks, and submits to the MessageGroupDispatcher.
type PendingJobPoller struct {
	cfg         Config
	pool        *pgxpool.Pool
	dispatcher  *MessageGroupDispatcher
	pausedCache *PausedConnectionCache
	// IsLeader gates claiming: when non-nil and false, the poller idles.
	// The per-group FIFO dispatcher is in-process only, so within-group
	// ordering requires a single active scheduler — concurrent SKIP-LOCKED
	// claims across replicas would dispatch a group's jobs out of order.
	// nil = always run (standby disabled). Set by Scheduler.Run.
	IsLeader func() bool
}

// NewPendingJobPoller wires the poller.
func NewPendingJobPoller(cfg Config, pool *pgxpool.Pool, dispatcher *MessageGroupDispatcher, pausedCache *PausedConnectionCache) *PendingJobPoller {
	return &PendingJobPoller{cfg: cfg, pool: pool, dispatcher: dispatcher, pausedCache: pausedCache}
}

// Run drives the poller until ctx is cancelled.
func (p *PendingJobPoller) Run(ctx context.Context) {
	tick := time.NewTicker(p.cfg.PollInterval)
	defer tick.Stop()
	slog.Info("dispatch job poller starting", "interval", p.cfg.PollInterval, "batch_size", p.cfg.BatchSize)
	for {
		select {
		case <-ctx.Done():
			slog.Info("dispatch job poller stopped")
			return
		case <-tick.C:
			if p.IsLeader != nil && !p.IsLeader() {
				continue // only the leader claims
			}
			if err := p.pollOnce(ctx); err != nil {
				slog.Warn("poll error", "err", err)
			}
		}
	}
}

// pollOnce claims a batch of jobs and submits them to the dispatcher.
func (p *PendingJobPoller) pollOnce(ctx context.Context) error {
	paused, err := p.pausedCache.PausedSubscriptionIDs(ctx)
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim PENDING jobs ready for dispatch. SKIP LOCKED so multiple
	// scheduler instances don't contend.
	// Retry timing is owned by the dispatcher's backoff loop, not by a
	// scheduled-for column on the row — matches the Rust poller in
	// crates/fc-platform/src/scheduler/poller.rs. The embedded schema
	// has neither `next_retry_at` (only added in migration 011's
	// no-op-on-embedded CREATE TABLE IF NOT EXISTS) nor a scheduled-
	// filtered claim path.
	rows, err := tx.Query(ctx,
		`SELECT id, subscription_id, message_group, mode, attempt_count, target_url
		   FROM msg_dispatch_jobs
		  WHERE status = 'PENDING'
		  ORDER BY message_group ASC NULLS LAST, sequence ASC, created_at ASC
		  LIMIT $1
		  FOR UPDATE SKIP LOCKED`,
		p.cfg.BatchSize)
	if err != nil {
		return err
	}
	type claim struct {
		id, subID, group, mode, target string
		attempt                        int32
	}
	var claims []claim
	for rows.Next() {
		var c claim
		var msgGroup *string
		var subID *string
		if err := rows.Scan(&c.id, &subID, &msgGroup, &c.mode, &c.attempt, &c.target); err != nil {
			rows.Close()
			return err
		}
		if subID != nil {
			c.subID = *subID
		}
		if msgGroup != nil {
			c.group = *msgGroup
		}
		claims = append(claims, c)
	}
	rows.Close()
	if len(claims) == 0 {
		return nil
	}

	// Filter and gather IDs to mark QUEUED. Dispatch is deliberately
	// deferred until the claim tx has committed: publishing while the tx was
	// still open meant (a) a commit failure after a publish re-claimed the
	// already-published job on the next poll (duplicate dispatch), and (b) a
	// publish failure's QUEUED→PENDING revert no-oped because the QUEUED
	// status it guards on hadn't committed yet (row stuck until stale
	// recovery).
	var queued []string
	var tokens []DispatchJobToken
	skipped := 0
	for _, c := range claims {
		if c.subID != "" {
			if _, isPaused := paused[c.subID]; isPaused {
				skipped++
				continue // leave PENDING; will be picked up after unpause
			}
		}
		queued = append(queued, c.id)
		tokens = append(tokens, DispatchJobToken{
			JobID:        c.id,
			MessageGroup: c.group,
			TargetURL:    c.target,
		})
	}

	if len(queued) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE msg_dispatch_jobs SET status = 'QUEUED', updated_at = NOW()
			  WHERE id = ANY($1)`, queued); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// QUEUED is durable — now hand the jobs to the dispatcher. A publish
	// failure reverts QUEUED→PENDING (dispatcher.dispatch), which is
	// guaranteed to see the committed status; a crash between commit and
	// Submit leaves rows QUEUED for stale recovery — the same failure mode
	// as a crash mid-publish, handled by the existing recovery loop.
	for _, t := range tokens {
		p.dispatcher.Submit(ctx, t)
	}

	if len(queued) > 0 || skipped > 0 {
		slog.Debug("poll tick", "queued", len(queued), "skipped_paused", skipped)
	}
	return nil
}

// DispatchJobToken is the value the poller hands the dispatcher. It
// carries just enough to publish to the queue without re-reading the
// job row.
type DispatchJobToken struct {
	JobID        string
	MessageGroup string
	TargetURL    string
}
