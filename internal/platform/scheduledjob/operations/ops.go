// Package operations holds all 7 scheduled_job admin use cases plus
// fire_now. Sync (bulk SDK upsert) is deferred to a focused follow-up
// alongside the other subdomain sync ops.
//
// All ops follow the same pattern; they're kept in one file to keep the
// pattern visible.
package operations

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httperror"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ── Create ────────────────────────────────────────────────────────────────

type CreateCommand struct {
	Code                string          `json:"code"`
	Name                string          `json:"name"`
	Crons               []string        `json:"crons"`
	Timezone            string          `json:"timezone,omitempty"`
	ClientID            *string         `json:"clientId,omitempty"`
	Description         *string         `json:"description,omitempty"`
	Payload             json.RawMessage `json:"payload,omitempty"`
	Concurrent          bool            `json:"concurrent"`
	TracksCompletion    bool            `json:"tracksCompletion"`
	TimeoutSeconds      *int32          `json:"timeoutSeconds,omitempty"`
	DeliveryMaxAttempts *int32          `json:"deliveryMaxAttempts,omitempty"`
	TargetURL           *string         `json:"targetUrl,omitempty"`
}

func CreateScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd CreateCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobCreated], error) {
	var zero commit.Committed[ScheduledJobCreated]

	code := strings.ToLower(strings.TrimSpace(cmd.Code))
	if code == "" {
		return zero, usecase.Validation("CODE_REQUIRED", "code is required")
	}
	if !codePattern.MatchString(code) {
		return zero, usecase.Validation("INVALID_CODE_FORMAT",
			"code must start with a lowercase letter and contain only lowercase alphanumeric and hyphens")
	}
	if strings.TrimSpace(cmd.Name) == "" {
		return zero, usecase.Validation("NAME_REQUIRED", "name is required")
	}
	if len(cmd.Crons) == 0 {
		return zero, usecase.Validation("CRONS_REQUIRED", "at least one cron expression is required")
	}
	for _, c := range cmd.Crons {
		if strings.TrimSpace(c) == "" {
			return zero, usecase.Validation("INVALID_CRON", "cron expressions cannot be empty")
		}
		if err := scheduledjob.ValidateCronShape(c); err != nil {
			return zero, usecase.Validation("CRON_INVALID_SHAPE", err.Error())
		}
	}

	existing, err := repo.FindByCode(ctx, code, cmd.ClientID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if existing != nil {
		return zero, usecase.Conflict("CODE_EXISTS", "Scheduled job with code '"+code+"' already exists")
	}
	j := scheduledjob.New(code, strings.TrimSpace(cmd.Name), cmd.Crons)
	j.ClientID = cmd.ClientID
	j.Description = cmd.Description
	if cmd.Timezone != "" {
		j.Timezone = cmd.Timezone
	}
	j.Payload = cmd.Payload
	j.Concurrent = cmd.Concurrent
	j.TracksCompletion = cmd.TracksCompletion
	j.TimeoutSeconds = cmd.TimeoutSeconds
	if cmd.DeliveryMaxAttempts != nil {
		j.DeliveryMaxAttempts = *cmd.DeliveryMaxAttempts
	}
	j.TargetURL = cmd.TargetURL
	j.CreatedBy = &ec.PrincipalID

	event := ScheduledJobCreated{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobCreatedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID,
		Code:           j.Code,
	}}
	return commit.Save(ctx, uow, j, repo, event, cmd)
}

// ── Update ────────────────────────────────────────────────────────────────

type UpdateCommand struct {
	ID                  string          `json:"id"`
	Name                *string         `json:"name,omitempty"`
	Description         *string         `json:"description,omitempty"`
	Crons               []string        `json:"crons,omitempty"`
	Timezone            *string         `json:"timezone,omitempty"`
	Payload             json.RawMessage `json:"payload,omitempty"`
	Concurrent          *bool           `json:"concurrent,omitempty"`
	TracksCompletion    *bool           `json:"tracksCompletion,omitempty"`
	TimeoutSeconds      *int32          `json:"timeoutSeconds,omitempty"`
	DeliveryMaxAttempts *int32          `json:"deliveryMaxAttempts,omitempty"`
	TargetURL           *string         `json:"targetUrl,omitempty"`
}

func UpdateScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd UpdateCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobUpdated], error) {
	var zero commit.Committed[ScheduledJobUpdated]

	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	if cmd.Name != nil && strings.TrimSpace(*cmd.Name) == "" {
		return zero, usecase.Validation("NAME_REQUIRED", "name cannot be empty")
	}

	j, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return zero, httperror.NotFound("ScheduledJob", cmd.ID)
	}
	if cmd.Name != nil {
		j.Name = strings.TrimSpace(*cmd.Name)
	}
	if cmd.Description != nil {
		j.Description = cmd.Description
	}
	if cmd.Crons != nil {
		if len(cmd.Crons) == 0 {
			return zero, usecase.Validation("CRONS_REQUIRED", "at least one cron expression is required")
		}
		for _, c := range cmd.Crons {
			if strings.TrimSpace(c) == "" {
				return zero, usecase.Validation("INVALID_CRON", "cron expressions cannot be empty")
			}
			if err := scheduledjob.ValidateCronShape(c); err != nil {
				return zero, usecase.Validation("CRON_INVALID_SHAPE", err.Error())
			}
		}
		j.Crons = cmd.Crons
	}
	if cmd.Timezone != nil {
		j.Timezone = *cmd.Timezone
	}
	if cmd.Payload != nil {
		j.Payload = cmd.Payload
	}
	if cmd.Concurrent != nil {
		j.Concurrent = *cmd.Concurrent
	}
	if cmd.TracksCompletion != nil {
		j.TracksCompletion = *cmd.TracksCompletion
	}
	if cmd.TimeoutSeconds != nil {
		j.TimeoutSeconds = cmd.TimeoutSeconds
	}
	if cmd.DeliveryMaxAttempts != nil {
		j.DeliveryMaxAttempts = *cmd.DeliveryMaxAttempts
	}
	if cmd.TargetURL != nil {
		j.TargetURL = cmd.TargetURL
	}
	j.UpdatedBy = &ec.PrincipalID
	j.Version++

	event := ScheduledJobUpdated{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobUpdatedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID,
		Code:           j.Code,
	}}
	return commit.Save(ctx, uow, j, repo, event, cmd)
}

// ── Pause / Resume / Archive ──────────────────────────────────────────────

// transition is the shared body for the three status-flip ops. Returns
// the updated job (caller wraps the typed event).
func transition(ctx context.Context, repo *scheduledjob.Repository, id string, ec usecase.ExecutionContext, apply func(*scheduledjob.ScheduledJob)) (*scheduledjob.ScheduledJob, error) {
	j, err := repo.FindByID(ctx, id)
	if err != nil {
		return nil, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return nil, httperror.NotFound("ScheduledJob", id)
	}
	apply(j)
	j.UpdatedBy = &ec.PrincipalID
	return j, nil
}

type PauseCommand struct {
	ID string `json:"id"`
}

func PauseScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd PauseCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobPaused], error) {
	var zero commit.Committed[ScheduledJobPaused]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	j, err := transition(ctx, repo, cmd.ID, ec, func(j *scheduledjob.ScheduledJob) { j.Pause() })
	if err != nil {
		return zero, err
	}
	event := ScheduledJobPaused{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobPausedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID, Code: j.Code,
	}}
	return commit.Save(ctx, uow, j, repo, event, cmd)
}

type ResumeCommand struct {
	ID string `json:"id"`
}

func ResumeScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd ResumeCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobResumed], error) {
	var zero commit.Committed[ScheduledJobResumed]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	j, err := transition(ctx, repo, cmd.ID, ec, func(j *scheduledjob.ScheduledJob) { j.Resume() })
	if err != nil {
		return zero, err
	}
	event := ScheduledJobResumed{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobResumedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID, Code: j.Code,
	}}
	return commit.Save(ctx, uow, j, repo, event, cmd)
}

type ArchiveCommand struct {
	ID string `json:"id"`
}

func ArchiveScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd ArchiveCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobArchived], error) {
	var zero commit.Committed[ScheduledJobArchived]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	j, err := transition(ctx, repo, cmd.ID, ec, func(j *scheduledjob.ScheduledJob) { j.Archive() })
	if err != nil {
		return zero, err
	}
	event := ScheduledJobArchived{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobArchivedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID, Code: j.Code,
	}}
	return commit.Save(ctx, uow, j, repo, event, cmd)
}

// ── Delete ────────────────────────────────────────────────────────────────

type DeleteCommand struct {
	ID string `json:"id"`
}

func DeleteScheduledJob(
	ctx context.Context,
	repo *scheduledjob.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd DeleteCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobDeleted], error) {
	var zero commit.Committed[ScheduledJobDeleted]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	j, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return zero, httperror.NotFound("ScheduledJob", cmd.ID)
	}
	event := ScheduledJobDeleted{commonEvent: commonEvent{
		Metadata:       usecase.NewEventMetadata(ec, ScheduledJobDeletedType, Source, subjectFor(j.ID)),
		ScheduledJobID: j.ID, Code: j.Code,
	}}
	return commit.Delete(ctx, uow, j, repo, event, cmd)
}

// ── FireNow ───────────────────────────────────────────────────────────────

// FireNowCommand triggers a manual fire. An optional CorrelationID is
// stamped on the instance + carried in the firing webhook (mirrors the Rust
// FireRequest.correlation_id).
type FireNowCommand struct {
	ID            string  `json:"id"`
	CorrelationID *string `json:"correlationId,omitempty"`
}

// FireNow inserts a MANUAL instance row (QUEUED, picked up by the dispatcher
// on its next tick) and emits the ScheduledJobFiredManually audit event.
// Two-phase, mirroring Rust fire_now: the infrastructure insert happens first
// (no UoW — instances are a projection), then the event is emitted; a failed
// insert yields no event.
//
// PAUSED jobs ARE firable manually — that's the point of a manual trigger
// (the poller skips PAUSED; a human can override). Only ARCHIVED is rejected.
func FireNow(
	ctx context.Context,
	repo *scheduledjob.Repository,
	instances *scheduledjob.InstanceRepository,
	uow *usecasepgx.UnitOfWork,
	cmd FireNowCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ScheduledJobFiredManually], error) {
	var zero commit.Committed[ScheduledJobFiredManually]
	if strings.TrimSpace(cmd.ID) == "" {
		return zero, usecase.Validation("ID_REQUIRED", "id is required")
	}
	j, err := repo.FindByID(ctx, cmd.ID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_id failed", err)
	}
	if j == nil {
		return zero, httperror.NotFound("ScheduledJob", cmd.ID)
	}
	if j.Status == scheduledjob.StatusArchived {
		return zero, usecase.Conflict("ARCHIVED", "Archived jobs cannot be fired")
	}

	now := time.Now().UTC()
	instanceID := tsid.Generate(tsid.ScheduledJobInstance)
	inst := &scheduledjob.ScheduledJobInstance{
		ID:               instanceID,
		ScheduledJobID:   j.ID,
		ClientID:         j.ClientID,
		JobCode:          j.Code,
		TriggerKind:      scheduledjob.TriggerManual,
		FiredAt:          now,
		Status:           scheduledjob.InstanceStatusQueued,
		DeliveryAttempts: 0,
		CorrelationID:    cmd.CorrelationID,
		CreatedAt:        now,
	}
	if err := instances.Insert(ctx, inst); err != nil {
		return zero, usecase.Internal("REPO", "insert instance failed", err)
	}

	event := ScheduledJobFiredManually{
		commonEvent: commonEvent{
			Metadata:       usecase.NewEventMetadata(ec, ScheduledJobFiredManuallyType, Source, subjectFor(j.ID)),
			ScheduledJobID: j.ID, Code: j.Code,
		},
		InstanceID: instanceID,
	}
	return commit.Emit(ctx, uow, event, cmd)
}
