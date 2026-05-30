package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// poller scans ACTIVE jobs and inserts a QUEUED instance for the latest due
// cron slot per job. Mirrors the Rust poller.
type poller struct {
	cfg       Config
	jobs      *scheduledjob.Repository
	instances *scheduledjob.InstanceRepository
	isLeader  func() bool
}

func (p *poller) run(ctx context.Context) {
	t := time.NewTicker(p.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("scheduled-job poller stopped")
			return
		case <-t.C:
			if !p.isLeader() {
				continue // only the leader fires (no SKIP LOCKED claim here)
			}
			if err := p.tick(ctx); err != nil {
				slog.Warn("scheduled-job poller tick error", "err", err)
			}
		}
	}
}

// tick scans ACTIVE jobs, computes the latest due slot per job, and inserts a
// QUEUED instance for it. Mirrors the Rust process_job: window is
// (last_fired ?? created_at, now]; only the latest slot fires (skip-missed).
func (p *poller) tick(ctx context.Context) error {
	jobs, err := p.jobs.FindActive(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range jobs {
		j := &jobs[i]
		after := j.CreatedAt
		if j.LastFiredAt != nil {
			after = *j.LastFiredAt
		}
		slot, ok := scheduledjob.LatestSlotInWindow(j.Crons, j.Timezone, after, now)
		if !ok {
			continue
		}
		// NOTE: the job's Concurrent flag is not yet gated at fire time (the
		// poller inserts unconditionally, matching the Rust poller body we
		// mirrored); enforcing non-concurrent runs via HasActiveInstance is a
		// tracked follow-up.
		inst := &scheduledjob.ScheduledJobInstance{
			ID:               tsid.Generate(tsid.ScheduledJobInstance),
			ScheduledJobID:   j.ID,
			ClientID:         j.ClientID,
			JobCode:          j.Code,
			TriggerKind:      scheduledjob.TriggerCron,
			ScheduledFor:     &slot,
			FiredAt:          now,
			Status:           scheduledjob.InstanceStatusQueued,
			DeliveryAttempts: 0,
			CreatedAt:        now,
		}
		if err := p.instances.Insert(ctx, inst); err != nil {
			slog.Warn("scheduled-job poller: insert instance failed", "job_id", j.ID, "err", err)
			continue
		}
		// Advance last_fired_at monotonically so the next tick's window opens
		// strictly after this slot. Done after the insert, matching Rust.
		if err := p.jobs.MarkFired(ctx, j.ID, slot); err != nil {
			slog.Warn("scheduled-job poller: mark_fired failed", "job_id", j.ID, "err", err)
		}
	}
	return nil
}
