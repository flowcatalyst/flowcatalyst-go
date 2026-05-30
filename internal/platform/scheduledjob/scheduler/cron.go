// Package scheduler is the scheduled-job cron + dispatch engine for
// ScheduledJob aggregates. Separate from internal/platform/scheduler, which
// schedules dispatch jobs.
//
// Mirrors fc-platform/src/scheduled_job/scheduler/. Two cooperating loops:
//
//   - poller — every PollInterval, scans ACTIVE jobs, computes the LATEST
//     cron slot in (last_fired, now] per job (skip-missed semantics), inserts
//     a QUEUED instance row, and advances last_fired_at monotonically.
//   - dispatcher — every DispatchInterval, claims QUEUED instances, marks them
//     IN_FLIGHT, POSTs the firing webhook, and applies the 202 contract:
//     202 → DELIVERED (terminal unless the job tracks completion); any other
//     response or a transport error → retry (back to QUEUED) until
//     delivery_max_attempts, then DELIVERY_FAILED.
//
// Both loops are leader-gated (a single replica fires each slot; the loops
// have no SELECT … FOR UPDATE SKIP LOCKED claim, matching the Rust
// single-active-replica design). All writes are direct infrastructure work
// (no UoW — instances are the firing-history projection, not an aggregate).
package scheduler

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
)

// Config controls the cron + dispatch loops. Mirrors the Rust
// ScheduledJobSchedulerConfig.
type Config struct {
	PollInterval      time.Duration // poller wake-up (default 30s)
	DispatchInterval  time.Duration // dispatcher wake-up (default 5s)
	DispatchBatchSize int64         // max QUEUED instances per dispatch tick (default 32)
	HTTPTimeout       time.Duration // per-webhook HTTP timeout (default 10s)
}

// DefaultConfig returns the Rust defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval:      30 * time.Second,
		DispatchInterval:  5 * time.Second,
		DispatchBatchSize: 32,
		HTTPTimeout:       10 * time.Second,
	}
}

// ConfigFromEnv builds a Config from the FC_SCHEDULED_JOB_* env vars,
// falling back to DefaultConfig. Matches the Rust env keys exactly:
//
//	FC_SCHEDULED_JOB_POLL_SECONDS
//	FC_SCHEDULED_JOB_DISPATCH_SECONDS
//	FC_SCHEDULED_JOB_DISPATCH_BATCH
//	FC_SCHEDULED_JOB_HTTP_TIMEOUT_SECONDS
func ConfigFromEnv() Config {
	c := DefaultConfig()
	if n, ok := envUint("FC_SCHEDULED_JOB_POLL_SECONDS"); ok {
		c.PollInterval = time.Duration(n) * time.Second
	}
	if n, ok := envUint("FC_SCHEDULED_JOB_DISPATCH_SECONDS"); ok {
		c.DispatchInterval = time.Duration(n) * time.Second
	}
	if n, ok := envUint("FC_SCHEDULED_JOB_DISPATCH_BATCH"); ok {
		c.DispatchBatchSize = int64(n)
	}
	if n, ok := envUint("FC_SCHEDULED_JOB_HTTP_TIMEOUT_SECONDS"); ok {
		c.HTTPTimeout = time.Duration(n) * time.Second
	}
	return c
}

func envUint(key string) (uint64, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Service owns the poller + dispatcher loops.
type Service struct {
	cfg        Config
	poller     *poller
	dispatcher *dispatcher
}

// NewService wires the cron + dispatch loops. isLeader gates both loops; pass
// nil for always-leader (single-instance / standby disabled).
func NewService(
	cfg Config,
	jobs *scheduledjob.Repository,
	instances *scheduledjob.InstanceRepository,
	isLeader func() bool,
) *Service {
	if isLeader == nil {
		isLeader = func() bool { return true }
	}
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	return &Service{
		cfg:        cfg,
		poller:     &poller{cfg: cfg, jobs: jobs, instances: instances, isLeader: isLeader},
		dispatcher: &dispatcher{cfg: cfg, jobs: jobs, instances: instances, http: httpClient, isLeader: isLeader},
	}
}

// Run spawns the poller + dispatcher loops and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	slog.Info("scheduled-job scheduler starting",
		"poll", s.cfg.PollInterval, "dispatch", s.cfg.DispatchInterval, "batch", s.cfg.DispatchBatchSize)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.poller.run(ctx) }()
	go func() { defer wg.Done(); s.dispatcher.run(ctx) }()
	wg.Wait()
	slog.Info("scheduled-job scheduler stopped")
}
