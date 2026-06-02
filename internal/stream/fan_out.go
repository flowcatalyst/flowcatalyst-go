package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// FanOut matches each new event against the active subscriptions and
// inserts a dispatch job per match. Mirrors
// crates/fc-stream/src/event_fan_out.rs.
//
// Subscription set is loaded with a small projection query and cached
// locally; the cache TTL controls how stale subscription edits can be
// before fanout picks them up. Cache stays in this package — Rust does
// the same to keep this loop independent of fc-platform.
//
// At-least-once semantics: dispatch-job inserts and `fanned_out_at`
// stamps land in one transaction. FOR UPDATE SKIP LOCKED on the claim
// makes it safe to run multiple stream nodes against the same DB.
type FanOut struct {
	pool            *pgxpool.Pool
	subscriptionTTL time.Duration

	cacheMu       sync.Mutex
	subs          []cachedSubscription
	lastCacheLoad time.Time
}

// FanOutConfig tunes the subscription cache.
type FanOutConfig struct {
	// SubscriptionTTL controls how long the cached subscription set is
	// reused before being refetched. Default 5s, matches Rust.
	SubscriptionTTL time.Duration
}

// DefaultFanOutConfig returns the Rust defaults.
func DefaultFanOutConfig() FanOutConfig {
	return FanOutConfig{SubscriptionTTL: 5 * time.Second}
}

// NewFanOut wires the fan-out processor.
func NewFanOut(pool *pgxpool.Pool) *FanOut {
	return NewFanOutWithConfig(pool, DefaultFanOutConfig())
}

// NewFanOutWithConfig wires the fan-out processor with an explicit config.
func NewFanOutWithConfig(pool *pgxpool.Pool, cfg FanOutConfig) *FanOut {
	if cfg.SubscriptionTTL <= 0 {
		cfg.SubscriptionTTL = 5 * time.Second
	}
	return &FanOut{pool: pool, subscriptionTTL: cfg.SubscriptionTTL}
}

// Projector returns the configured Projector ready to Run.
func (f *FanOut) Projector(cfg ProjectorConfig) *Projector {
	return &Projector{
		Name: "event_fan_out",
		Pool: f.pool,
		Cfg:  cfg,
		Step: f.step,
	}
}

func (f *FanOut) step(ctx context.Context, batchSize int) (int, error) {
	subs, err := f.subscriptions(ctx)
	if err != nil {
		return 0, fmt.Errorf("load subscriptions: %w", err)
	}

	// Fast path: no active subscriptions. Stamp events as fanned-out
	// without opening a long transaction (mirrors Rust's
	// `claim_events_no_subs`).
	if len(subs) == 0 {
		tag, err := f.pool.Exec(ctx,
			`WITH batch AS (
			    SELECT id, created_at
			      FROM msg_events
			     WHERE fanned_out_at IS NULL
			     ORDER BY created_at
			     LIMIT $1
			 )
			 UPDATE msg_events e
			    SET fanned_out_at = NOW()
			   FROM batch b
			  WHERE e.id = b.id AND e.created_at = b.created_at`, batchSize)
		if err != nil {
			return 0, fmt.Errorf("stamp no-subs: %w", err)
		}
		return int(tag.RowsAffected()), nil
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := claimUnfannedEvents(ctx, tx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	if len(claimed) == 0 {
		return 0, nil
	}

	jobs := buildJobs(claimed, subs)
	if len(jobs) > 0 {
		if err := insertJobsInTx(ctx, tx, jobs); err != nil {
			return 0, fmt.Errorf("insert jobs: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(claimed), nil
}

// ── Event claim ──────────────────────────────────────────────────────────

// claimedEvent carries just the columns fanout needs from msg_events.
type claimedEvent struct {
	ID            string
	EventType     string
	Source        string
	Subject       *string
	Data          json.RawMessage
	CorrelationID *string
	MessageGroup  *string
	ClientID      *string
	CreatedAt     time.Time
}

// claimUnfannedEvents stamps `fanned_out_at` and returns the claimed
// rows in one shot — mirrors Rust's CTE in `claim_events`.
func claimUnfannedEvents(ctx context.Context, tx pgx.Tx, batchSize int) ([]claimedEvent, error) {
	rows, err := tx.Query(ctx,
		`WITH batch AS (
		    SELECT id, created_at
		      FROM msg_events
		     WHERE fanned_out_at IS NULL
		     ORDER BY created_at
		     LIMIT $1
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE msg_events e
		    SET fanned_out_at = NOW()
		   FROM batch b
		  WHERE e.id = b.id AND e.created_at = b.created_at
		 RETURNING e.id, e.type, e.source, e.subject, e.data,
		           e.correlation_id, e.message_group, e.client_id, e.created_at`,
		batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []claimedEvent
	for rows.Next() {
		var e claimedEvent
		var data []byte
		if err := rows.Scan(&e.ID, &e.EventType, &e.Source, &e.Subject, &data,
			&e.CorrelationID, &e.MessageGroup, &e.ClientID, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(data) > 0 {
			e.Data = data
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Subscription cache ───────────────────────────────────────────────────

// cachedSubscription is the minimal field set fanout needs. Loaded by
// `loadActiveSubscriptions` and refreshed every SubscriptionTTL.
type cachedSubscription struct {
	ID                string
	ClientID          *string
	Target            string
	Mode              common.DispatchMode
	DataOnly          bool
	DispatchPoolID    *string
	ServiceAccountID  *string
	MaxRetries        int32
	TimeoutSeconds    int32
	Sequence          int32
	EventTypePatterns []string
}

func (s *cachedSubscription) matchesEventType(code string) bool {
	for _, p := range s.EventTypePatterns {
		if patternMatches(p, code) {
			return true
		}
	}
	return false
}

func (s *cachedSubscription) matchesClient(eventClient *string) bool {
	if s.ClientID == nil {
		return true
	}
	if eventClient == nil {
		return false
	}
	return *s.ClientID == *eventClient
}

// patternMatches is the Rust-side `:`-separated wildcard match. Segment
// count must agree; `*` matches a single segment.
func patternMatches(pattern, code string) bool {
	pp := strings.Split(pattern, ":")
	cp := strings.Split(code, ":")
	if len(pp) != len(cp) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != cp[i] {
			return false
		}
	}
	return true
}

// subscriptions returns the current cache, refreshing if stale.
func (f *FanOut) subscriptions(ctx context.Context) ([]cachedSubscription, error) {
	f.cacheMu.Lock()
	defer f.cacheMu.Unlock()
	if time.Since(f.lastCacheLoad) < f.subscriptionTTL {
		return f.subs, nil
	}
	subs, err := loadActiveSubscriptions(ctx, f.pool)
	if err != nil {
		// Keep the stale cache rather than failing the cycle.
		if !f.lastCacheLoad.IsZero() {
			return f.subs, nil
		}
		return nil, err
	}
	f.subs = subs
	f.lastCacheLoad = time.Now()
	return f.subs, nil
}

func loadActiveSubscriptions(ctx context.Context, pool *pgxpool.Pool) ([]cachedSubscription, error) {
	rows, err := pool.Query(ctx,
		`SELECT s.id, s.client_id, s.target, s.mode, s.data_only,
		        s.dispatch_pool_id, s.service_account_id, s.max_retries,
		        s.timeout_seconds, s.sequence, e.event_type_code
		   FROM msg_subscriptions s
		   LEFT JOIN msg_subscription_event_types e ON e.subscription_id = s.id
		  WHERE s.status = 'ACTIVE'
		  ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*cachedSubscription{}
	var order []string
	for rows.Next() {
		var (
			id, target, mode                       string
			clientID, dispatchPoolID, saID, etCode *string
			dataOnly                               bool
			maxRetries, timeoutSeconds, sequence   int32
		)
		if err := rows.Scan(&id, &clientID, &target, &mode, &dataOnly,
			&dispatchPoolID, &saID, &maxRetries, &timeoutSeconds,
			&sequence, &etCode); err != nil {
			return nil, err
		}
		entry, ok := byID[id]
		if !ok {
			entry = &cachedSubscription{
				ID:               id,
				ClientID:         clientID,
				Target:           target,
				Mode:             common.ParseDispatchMode(mode),
				DataOnly:         dataOnly,
				DispatchPoolID:   dispatchPoolID,
				ServiceAccountID: saID,
				MaxRetries:       maxRetries,
				TimeoutSeconds:   timeoutSeconds,
				Sequence:         sequence,
			}
			byID[id] = entry
			order = append(order, id)
		}
		if etCode != nil {
			entry.EventTypePatterns = append(entry.EventTypePatterns, *etCode)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]cachedSubscription, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// ── Dispatch job assembly + insert ───────────────────────────────────────

// newJob is the subset of msg_dispatch_jobs columns fanout sets. Other
// columns take the table default (kind='EVENT', retry_strategy='exponential',
// etc.). Mirrors Rust's `NewJobRow`.
type newJob struct {
	ID             string
	Code           string
	Source         string
	Subject        *string
	EventID        string
	CorrelationID  *string
	TargetURL      string
	Payload        string
	DataOnly       bool
	ServiceAcctID  *string
	ClientID       *string
	SubscriptionID string
	Mode           string
	DispatchPoolID *string
	MessageGroup   *string
	Sequence       int32
	TimeoutSeconds int32
	Status         string
	MaxRetries     int32
	IdempotencyKey string
	CreatedAt      time.Time
}

func buildJobs(events []claimedEvent, subs []cachedSubscription) []newJob {
	var jobs []newJob
	for _, e := range events {
		for i := range subs {
			s := &subs[i]
			if !s.matchesEventType(e.EventType) {
				continue
			}
			if !s.matchesClient(e.ClientID) {
				continue
			}
			payload := "null"
			if len(e.Data) > 0 {
				payload = string(e.Data)
			}
			jobs = append(jobs, newJob{
				// 13-char untyped TSID — `msg_dispatch_jobs.id` is
				// VARCHAR(13). Using a typed prefix (`djb_...`) overflows
				// the column; the Rust source has the same latent bug.
				ID:             tsid.GenerateUntyped(),
				Code:           e.EventType,
				Source:         e.Source,
				Subject:        e.Subject,
				EventID:        e.ID,
				CorrelationID:  e.CorrelationID,
				TargetURL:      s.Target,
				Payload:        payload,
				DataOnly:       s.DataOnly,
				ServiceAcctID:  s.ServiceAccountID,
				ClientID:       e.ClientID,
				SubscriptionID: s.ID,
				Mode:           dispatchModeStr(s.Mode),
				DispatchPoolID: s.DispatchPoolID,
				MessageGroup:   e.MessageGroup,
				Sequence:       s.Sequence,
				TimeoutSeconds: s.TimeoutSeconds,
				Status:         string(common.DispatchPending),
				MaxRetries:     s.MaxRetries,
				IdempotencyKey: fmt.Sprintf("%s:%s", e.ID, s.ID),
				CreatedAt:      e.CreatedAt,
			})
		}
	}
	return jobs
}

func dispatchModeStr(m common.DispatchMode) string {
	switch m {
	case common.DispatchBlockOnError:
		return "BLOCK_ON_ERROR"
	case common.DispatchNextOnError:
		return "NEXT_ON_ERROR"
	default:
		return "IMMEDIATE"
	}
}

// insertJobsInTx writes the fanout-produced jobs in the same transaction
// that stamped fanned_out_at. Uses pgx.Batch — same shape as the
// dispatchjob repository's InsertBatch, but scoped to the columns
// fanout actually sets (everything else takes the table default).
func insertJobsInTx(ctx context.Context, tx pgx.Tx, jobs []newJob) error {
	if len(jobs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, j := range jobs {
		batch.Queue(
			`INSERT INTO msg_dispatch_jobs (
			    id, code, source, subject, event_id, correlation_id,
			    target_url, protocol, payload, data_only, service_account_id,
			    client_id, subscription_id, mode, dispatch_pool_id, message_group,
			    sequence, timeout_seconds, status, max_retries, idempotency_key,
			    created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'HTTP_WEBHOOK', $8, $9,
			         $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			         $21, $21)
			 ON CONFLICT (id, created_at) DO NOTHING`,
			j.ID, j.Code, j.Source, j.Subject, j.EventID, j.CorrelationID,
			j.TargetURL, j.Payload, j.DataOnly, j.ServiceAcctID,
			j.ClientID, j.SubscriptionID, j.Mode, j.DispatchPoolID,
			j.MessageGroup, j.Sequence, j.TimeoutSeconds, j.Status,
			j.MaxRetries, j.IdempotencyKey, j.CreatedAt)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range jobs {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}
