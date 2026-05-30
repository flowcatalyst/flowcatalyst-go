// dto.go contains the wire-format types for the scheduled_job API.
package api

import (
	"encoding/json"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/jsontime"
)

// CreateScheduledJobRequest is the wire body for POST /api/scheduled-jobs.
type CreateScheduledJobRequest struct {
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

func (r CreateScheduledJobRequest) toCommand() operations.CreateCommand {
	return operations.CreateCommand{
		Code:                r.Code,
		Name:                r.Name,
		Crons:               r.Crons,
		Timezone:            r.Timezone,
		ClientID:            r.ClientID,
		Description:         r.Description,
		Payload:             r.Payload,
		Concurrent:          r.Concurrent,
		TracksCompletion:    r.TracksCompletion,
		TimeoutSeconds:      r.TimeoutSeconds,
		DeliveryMaxAttempts: r.DeliveryMaxAttempts,
		TargetURL:           r.TargetURL,
	}
}

// UpdateScheduledJobRequest is the wire body for PUT /api/scheduled-jobs/{id}.
type UpdateScheduledJobRequest struct {
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

func (r UpdateScheduledJobRequest) toCommand(id string) operations.UpdateCommand {
	return operations.UpdateCommand{
		ID:                  id,
		Name:                r.Name,
		Description:         r.Description,
		Crons:               r.Crons,
		Timezone:            r.Timezone,
		Payload:             r.Payload,
		Concurrent:          r.Concurrent,
		TracksCompletion:    r.TracksCompletion,
		TimeoutSeconds:      r.TimeoutSeconds,
		DeliveryMaxAttempts: r.DeliveryMaxAttempts,
		TargetURL:           r.TargetURL,
	}
}

// ScheduledJobResponse mirrors scheduledjob.ScheduledJob.
type ScheduledJobResponse struct {
	ID                  string           `json:"id"`
	ClientID            *string          `json:"clientId,omitempty"`
	Code                string           `json:"code"`
	Name                string           `json:"name"`
	Description         *string          `json:"description,omitempty"`
	Status              string           `json:"status"`
	Crons               []string         `json:"crons"`
	Timezone            string           `json:"timezone"`
	Payload             json.RawMessage  `json:"payload,omitempty"`
	Concurrent          bool             `json:"concurrent"`
	TracksCompletion    bool             `json:"tracksCompletion"`
	TimeoutSeconds      *int32           `json:"timeoutSeconds,omitempty"`
	DeliveryMaxAttempts int32            `json:"deliveryMaxAttempts"`
	TargetURL           *string          `json:"targetUrl,omitempty"`
	LastFiredAt         *httpcompat.Time `json:"lastFiredAt,omitempty"`
	CreatedAt           httpcompat.Time  `json:"createdAt"`
	UpdatedAt           httpcompat.Time  `json:"updatedAt"`
	CreatedBy           *string          `json:"createdBy,omitempty"`
	UpdatedBy           *string          `json:"updatedBy,omitempty"`
	Version             int32            `json:"version"`
	// HasActiveInstance is true when any non-terminal instance
	// (QUEUED/IN_FLIGHT/DELIVERED) currently exists for this job — drives the
	// dashboard "currently running" badge. Mirrors the Rust field.
	HasActiveInstance bool `json:"hasActiveInstance"`
}

func fromEntity(j *scheduledjob.ScheduledJob) ScheduledJobResponse {
	crons := j.Crons
	if crons == nil {
		crons = []string{}
	}
	var lastFired *httpcompat.Time
	if j.LastFiredAt != nil {
		v := jsontime.New(*j.LastFiredAt)
		lastFired = &v
	}
	return ScheduledJobResponse{
		ID:                  j.ID,
		ClientID:            j.ClientID,
		Code:                j.Code,
		Name:                j.Name,
		Description:         j.Description,
		Status:              string(j.Status),
		Crons:               crons,
		Timezone:            j.Timezone,
		Payload:             j.Payload,
		Concurrent:          j.Concurrent,
		TracksCompletion:    j.TracksCompletion,
		TimeoutSeconds:      j.TimeoutSeconds,
		DeliveryMaxAttempts: j.DeliveryMaxAttempts,
		TargetURL:           j.TargetURL,
		LastFiredAt:         lastFired,
		CreatedAt:           jsontime.New(j.CreatedAt),
		UpdatedAt:           jsontime.New(j.UpdatedAt),
		CreatedBy:           j.CreatedBy,
		UpdatedBy:           j.UpdatedBy,
		Version:             j.Version,
	}
}

// FireNowRequest is the optional body for POST /api/scheduled-jobs/{id}/fire.
// A pointer Body makes it optional in huma, so a bodyless fire still works;
// the correlationId (when supplied) is stamped on the instance + carried in
// the firing webhook. Mirrors the Rust FireRequest.
type FireNowRequest struct {
	CorrelationID *string `json:"correlationId,omitempty"`
}

// FireNowResponse is the wire shape for POST /api/scheduled-jobs/{id}/fire.
type FireNowResponse struct {
	ScheduledJobID string `json:"scheduledJobId"`
	InstanceID     string `json:"instanceId"`
}

// ScheduledJobInstanceResponse mirrors scheduledjob.ScheduledJobInstance.
type ScheduledJobInstanceResponse struct {
	ID               string          `json:"id"`
	ScheduledJobID   string          `json:"scheduledJobId"`
	ClientID         *string         `json:"clientId,omitempty"`
	JobCode          string          `json:"jobCode"`
	TriggerKind      string          `json:"triggerKind"`
	ScheduledFor     *httpcompat.Time `json:"scheduledFor,omitempty"`
	FiredAt          httpcompat.Time `json:"firedAt"`
	DeliveredAt      *httpcompat.Time `json:"deliveredAt,omitempty"`
	CompletedAt      *httpcompat.Time `json:"completedAt,omitempty"`
	Status           string          `json:"status"`
	DeliveryAttempts int32           `json:"deliveryAttempts"`
	DeliveryError    *string         `json:"deliveryError,omitempty"`
	CompletionStatus *string         `json:"completionStatus,omitempty"`
	CompletionResult json.RawMessage `json:"completionResult,omitempty"`
	CorrelationID    *string         `json:"correlationId,omitempty"`
	CreatedAt        httpcompat.Time `json:"createdAt"`
}

func instanceToResponse(i *scheduledjob.ScheduledJobInstance) ScheduledJobInstanceResponse {
	var sched *httpcompat.Time
	if i.ScheduledFor != nil {
		t := jsontime.New(*i.ScheduledFor)
		sched = &t
	}
	var delivered *httpcompat.Time
	if i.DeliveredAt != nil {
		t := jsontime.New(*i.DeliveredAt)
		delivered = &t
	}
	var completed *httpcompat.Time
	if i.CompletedAt != nil {
		t := jsontime.New(*i.CompletedAt)
		completed = &t
	}
	return ScheduledJobInstanceResponse{
		ID:               i.ID,
		ScheduledJobID:   i.ScheduledJobID,
		ClientID:         i.ClientID,
		JobCode:          i.JobCode,
		TriggerKind:      string(i.TriggerKind),
		ScheduledFor:     sched,
		FiredAt:          jsontime.New(i.FiredAt),
		DeliveredAt:      delivered,
		CompletedAt:      completed,
		Status:           string(i.Status),
		DeliveryAttempts: i.DeliveryAttempts,
		DeliveryError:    i.DeliveryError,
		CompletionStatus: i.CompletionStatus,
		CompletionResult: i.CompletionResult,
		CorrelationID:    i.CorrelationID,
		CreatedAt:        jsontime.New(i.CreatedAt),
	}
}

// ScheduledJobInstanceLogResponse mirrors scheduledjob.ScheduledJobInstanceLog.
type ScheduledJobInstanceLogResponse struct {
	ID             string          `json:"id"`
	InstanceID     string          `json:"instanceId"`
	ScheduledJobID *string         `json:"scheduledJobId,omitempty"`
	ClientID       *string         `json:"clientId,omitempty"`
	Level          string          `json:"level"`
	Message        string          `json:"message"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      httpcompat.Time `json:"createdAt"`
}

func instanceLogToResponse(l *scheduledjob.ScheduledJobInstanceLog) ScheduledJobInstanceLogResponse {
	return ScheduledJobInstanceLogResponse{
		ID:             l.ID,
		InstanceID:     l.InstanceID,
		ScheduledJobID: l.ScheduledJobID,
		ClientID:       l.ClientID,
		Level:          l.Level,
		Message:        l.Message,
		Metadata:       l.Metadata,
		CreatedAt:      jsontime.New(l.CreatedAt),
	}
}

// WriteInstanceLogRequest is the body for POST .../log.
type WriteInstanceLogRequest struct {
	Level    string          `json:"level" doc:"DEBUG | INFO | WARN | ERROR"`
	Message  string          `json:"message"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// CompleteInstanceRequest is the body for POST .../complete.
type CompleteInstanceRequest struct {
	Status           string          `json:"status,omitempty" doc:"Defaults to COMPLETED"`
	CompletionStatus string          `json:"completionStatus,omitempty"`
	CompletionResult json.RawMessage `json:"completionResult,omitempty"`
}
