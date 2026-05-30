package outbox

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
)

// Config tunes the outbox processor.
type Config struct {
	PollInterval time.Duration
	BatchSize    int
	MaxInFlight  int64
	HTTPTimeout  time.Duration
	PlatformURL  string
	AuthToken    string
	// MaxRetries caps re-queues of a retryable failure (OB6): once an item
	// has been attempted MaxRetries times it keeps its failure status instead
	// of returning to PENDING, so it stops hot-looping. Mirrors the Rust
	// MessageGroupProcessorConfig.max_retries (default 3).
	MaxRetries int
	// RecoveryInterval / RecoveryThreshold drive crash recovery (OB2): every
	// RecoveryInterval, rows stuck IN_PROGRESS longer than RecoveryThreshold
	// (claimed by a since-crashed processor) are reset to PENDING.
	RecoveryInterval  time.Duration
	RecoveryThreshold time.Duration
	// MaxConcurrentGroups caps how many distinct message groups dispatch
	// concurrently (OB7). <= 0 = unbounded. Mirrors Rust max_concurrent_groups.
	MaxConcurrentGroups int
	// BlockOnError stops a message group as soon as one of its items fails,
	// releasing the rest to re-run in order behind it (OB4 ordering guarantee).
	// Default true, matching Rust block_on_error. Ungrouped items are unaffected.
	BlockOnError bool
}

// DefaultConfig matches the Rust outbox defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:        1 * time.Second,
		BatchSize:           100,
		MaxInFlight:         1000,
		HTTPTimeout:         30 * time.Second,
		MaxRetries:          3,
		RecoveryInterval:    60 * time.Second,
		RecoveryThreshold:   5 * time.Minute,
		MaxConcurrentGroups: 10,
		BlockOnError:        true,
	}
}

// Processor wires the outbox pipeline:
//
//	repo.ClaimPending → groupDistributor → httpDispatcher → repo.MarkSuccess/Failed
//
// Mirrors fc-outbox/src/enhanced_processor.rs.
type Processor struct {
	cfg          Config
	repo         Repository
	dispatcher   *HTTPDispatcher
	distributor  *GroupDistributor
	inFlight     atomic.Int64
	totalSucceed atomic.Uint64
	totalFailed  atomic.Uint64

	// IsLeader gates polling; nil means always-leader (single instance /
	// standby disabled). When standby is enabled only the leader polls — the
	// Mongo backend has no atomic claim, so a single active poller avoids
	// double-claims. Mirrors the Rust outbox leadership gate.
	IsLeader func() bool
}

// NewProcessor wires a processor.
func NewProcessor(cfg Config, repo Repository) *Processor {
	d := NewHTTPDispatcher(cfg.PlatformURL, cfg.AuthToken, cfg.HTTPTimeout)
	return &Processor{
		cfg:         cfg,
		repo:        repo,
		dispatcher:  d,
		distributor: NewGroupDistributor(cfg.MaxConcurrentGroups, cfg.BlockOnError),
	}
}

// Run drives the processor until ctx is cancelled. Two tickers: the poll
// loop (claim + dispatch) and the crash-recovery loop (reset stuck rows).
func (p *Processor) Run(ctx context.Context) {
	tick := time.NewTicker(p.cfg.PollInterval)
	defer tick.Stop()
	recoveryInterval := p.cfg.RecoveryInterval
	if recoveryInterval <= 0 {
		recoveryInterval = 60 * time.Second
	}
	recoveryTick := time.NewTicker(recoveryInterval)
	defer recoveryTick.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("outbox processor stopped")
			return
		case <-tick.C:
			if p.IsLeader != nil && !p.IsLeader() {
				continue // only the leader polls
			}
			if p.inFlight.Load() >= p.cfg.MaxInFlight {
				continue // backpressure
			}
			p.tick(ctx)
		case <-recoveryTick.C:
			if p.IsLeader != nil && !p.IsLeader() {
				continue
			}
			threshold := p.cfg.RecoveryThreshold
			if threshold <= 0 {
				threshold = 5 * time.Minute
			}
			if n, err := p.repo.RecoverStuck(ctx, threshold); err != nil {
				slog.Warn("outbox recover stuck failed", "err", err)
			} else if n > 0 {
				slog.Info("outbox recovered stuck items", "count", n)
			}
		}
	}
}

func (p *Processor) tick(ctx context.Context) {
	items, err := p.repo.ClaimPending(ctx, p.cfg.BatchSize)
	if err != nil {
		slog.Warn("outbox claim failed", "err", err)
		return
	}
	if len(items) == 0 {
		return
	}

	// Route through the group distributor: items with the same message_group
	// execute serially (FIFO) and, with BlockOnError, stop the group on a
	// failure (releasing the rest to re-run in order behind it); items without
	// a group or in different groups execute concurrently (bounded by OB7).
	for _, item := range items {
		item := item
		p.inFlight.Add(1)
		p.distributor.Submit(item,
			func() bool {
				defer p.inFlight.Add(-1)
				return p.dispatch(ctx, item)
			},
			func() {
				defer p.inFlight.Add(-1)
				p.release(ctx, item)
			})
	}
}

// release returns an undispatched, group-blocked item to PENDING (no failure
// penalty) so the next poll re-claims it in order behind the failed item.
func (p *Processor) release(ctx context.Context, item Item) {
	if err := p.repo.Release(ctx, []string{item.ID}); err != nil {
		slog.Warn("outbox release failed", "id", item.ID, "err", err)
	}
}

// dispatch sends one item and records its outcome. Returns true on success,
// false on any failure (so a message group blocks on it when BlockOnError).
func (p *Processor) dispatch(ctx context.Context, item Item) bool {
	out := p.dispatcher.Send(ctx, item)
	if out.Status == common.OutboxSuccess {
		if err := p.repo.MarkSuccess(ctx, []string{item.ID}); err != nil {
			slog.Warn("outbox mark success failed", "id", item.ID, "err", err)
			return false
		}
		p.totalSucceed.Add(1)
		return true
	}
	// Failed. Re-queue (back to PENDING) only when the status is retryable AND
	// the item hasn't hit the max-retries cap (OB6): item.AttemptCount is the
	// retry_count before this attempt, so this is attempt #(AttemptCount+1);
	// once that reaches MaxRetries we stop re-queuing and the row keeps its
	// failure code (not re-claimed). Non-retryable statuses never re-queue.
	maxRetries := p.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	requeue := out.Status.IsRetryable() && item.AttemptCount+1 < maxRetries
	if err := p.repo.MarkFailed(ctx, []string{item.ID}, out.Status, out.Message, requeue); err != nil {
		slog.Warn("outbox mark failed", "id", item.ID, "err", err)
	}
	p.totalFailed.Add(1)
	return false
}

// InFlight returns the count of items currently in dispatch.
func (p *Processor) InFlight() int64 { return p.inFlight.Load() }

// Totals returns (success, failure) counters since process start.
func (p *Processor) Totals() (uint64, uint64) {
	return p.totalSucceed.Load(), p.totalFailed.Load()
}
