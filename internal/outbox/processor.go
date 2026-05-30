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
	groups       *GroupStateManager
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
		groups:      NewGroupStateManager(),
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

	// Partition the claim: grouped items keep strict per-group FIFO +
	// block-on-error (serial via the distributor, OB7-bounded); ungrouped items
	// are batched by ItemType into a single HTTP call each (OB4 throughput —
	// there's no ordering to preserve for them).
	byType := make(map[common.OutboxItemType][]Item)
	for _, item := range items {
		item := item
		if item.MessageGroup != nil && *item.MessageGroup != "" {
			// State machine: skip a Paused/Blocked group — release its claimed
			// items back to PENDING (re-claimed once the group is resumed/
			// unblocked) instead of dispatching past a block.
			if !p.groups.IsActive(*item.MessageGroup) {
				p.release(ctx, item)
				continue
			}
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
			continue
		}
		byType[item.ItemType] = append(byType[item.ItemType], item)
	}
	for _, batch := range byType {
		batch := batch
		p.inFlight.Add(int64(len(batch)))
		go p.dispatchBatch(ctx, batch)
	}
}

// dispatchBatch sends a batch of ungrouped, same-ItemType items in one HTTP
// call (OB4) and records each item's outcome — MarkSuccess in bulk, MarkFailed
// per item (same retryable + max-retries requeue rule as dispatch).
func (p *Processor) dispatchBatch(ctx context.Context, batch []Item) {
	defer p.inFlight.Add(-int64(len(batch)))
	outcomes := p.dispatcher.SendBatch(ctx, batch)
	maxRetries := p.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	var succeeded []string
	for _, item := range batch {
		out, ok := outcomes[item.ID]
		if !ok {
			out = DispatchOutcome{Status: common.OutboxInternalError, Message: "no per-item result"}
		}
		if out.Status == common.OutboxSuccess {
			succeeded = append(succeeded, item.ID)
			p.totalSucceed.Add(1)
			continue
		}
		requeue := out.Status.IsRetryable() && item.AttemptCount+1 < maxRetries
		if err := p.repo.MarkFailed(ctx, []string{item.ID}, out.Status, out.Message, requeue); err != nil {
			slog.Warn("outbox mark failed", "id", item.ID, "err", err)
		}
		p.totalFailed.Add(1)
	}
	if len(succeeded) > 0 {
		if err := p.repo.MarkSuccess(ctx, succeeded); err != nil {
			slog.Warn("outbox mark success failed (batch)", "count", len(succeeded), "err", err)
		}
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
	// State machine: a PERMANENT failure (non-retryable or retry-exhausted) of a
	// grouped item blocks its group until an operator unblocks (retry) or skips
	// (abandon) the poison item — so the group never silently advances past it.
	if !requeue && p.cfg.BlockOnError && item.MessageGroup != nil && *item.MessageGroup != "" {
		p.groups.Block(*item.MessageGroup, item.ID, out.Message)
		slog.Warn("outbox message group blocked", "group", *item.MessageGroup, "id", item.ID, "error", out.Message)
	}
	return false
}

// ── Operational state machine controls (Rust message_group_processor parity) ──

// PauseGroup stops dispatching a message group; its items are released to
// PENDING each cycle until ResumeGroup. No-op when the group is Blocked.
func (p *Processor) PauseGroup(group string) { p.groups.Pause(group) }

// ResumeGroup resumes a Paused group.
func (p *Processor) ResumeGroup(group string) { p.groups.Resume(group) }

// UnblockGroup clears a Blocked group and RE-QUEUES the poison item (a fresh
// retry), so the whole group runs again in order. Returns false if not Blocked.
func (p *Processor) UnblockGroup(ctx context.Context, group string) bool {
	itemID, ok := p.groups.ClearBlock(group)
	if !ok {
		return false
	}
	if itemID != "" {
		if err := p.repo.Requeue(ctx, []string{itemID}); err != nil {
			slog.Warn("outbox unblock requeue failed", "group", group, "id", itemID, "err", err)
		}
	}
	slog.Info("outbox group unblocked (poison re-queued)", "group", group, "id", itemID)
	return true
}

// SkipGroup clears a Blocked group WITHOUT re-queuing the poison item — it stays
// terminally failed and the group advances past it. Returns false if not Blocked.
func (p *Processor) SkipGroup(group string) bool {
	itemID, ok := p.groups.ClearBlock(group)
	if ok {
		slog.Info("outbox group skipped blocking item", "group", group, "id", itemID)
	}
	return ok
}

// GroupStates returns the non-default (Paused/Blocked) message-group states.
func (p *Processor) GroupStates() []GroupInfo { return p.groups.Snapshot() }

// BlockedGroups returns only the Blocked message groups.
func (p *Processor) BlockedGroups() []GroupInfo { return p.groups.Blocked() }

// InFlight returns the count of items currently in dispatch.
func (p *Processor) InFlight() int64 { return p.inFlight.Load() }

// Totals returns (success, failure) counters since process start.
func (p *Processor) Totals() (uint64, uint64) {
	return p.totalSucceed.Load(), p.totalFailed.Load()
}
