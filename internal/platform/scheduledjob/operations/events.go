package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	ScheduledJobCreatedType       = "platform:admin:scheduled-job:created"
	ScheduledJobUpdatedType       = "platform:admin:scheduled-job:updated"
	ScheduledJobPausedType        = "platform:admin:scheduled-job:paused"
	ScheduledJobResumedType       = "platform:admin:scheduled-job:resumed"
	ScheduledJobArchivedType      = "platform:admin:scheduled-job:archived"
	ScheduledJobDeletedType       = "platform:admin:scheduled-job:deleted"
	ScheduledJobFiredManuallyType = "platform:admin:scheduled-job:fired-manually"
	ScheduledJobsSyncedType       = "platform:admin:scheduledjobs:synced"
	Source                        = "platform:admin"
)

func subjectFor(id string) string { return "platform.scheduledjob." + id }
func groupFor(id string) string   { return "platform:scheduledjob:" + id }

// commonEvent is the shape for the four "ID + Code" events. Embedded by
// the typed events below; they wrap with the right EventType.
type commonEvent struct {
	Metadata       usecase.EventMetadata
	ScheduledJobID string
	Code           string
}

func (e commonEvent) EventID() string       { return e.Metadata.EventID }
func (e commonEvent) SpecVersion() string   { return "1.0" }
func (e commonEvent) Source() string        { return Source }
func (e commonEvent) Subject() string       { return subjectFor(e.ScheduledJobID) }
func (e commonEvent) Time() time.Time       { return e.Metadata.OccurredAt }
func (e commonEvent) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e commonEvent) CorrelationID() string { return e.Metadata.CorrelationID }
func (e commonEvent) CausationID() string   { return e.Metadata.CausationID }
func (e commonEvent) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e commonEvent) MessageGroup() string  { return groupFor(e.ScheduledJobID) }
func (e commonEvent) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID   string `json:"scheduledJobId"`
		Code string `json:"code"`
	}{e.ScheduledJobID, e.Code})
}

// ScheduledJobCreated is emitted on create.
type ScheduledJobCreated struct{ commonEvent }

func (ScheduledJobCreated) EventType() string { return ScheduledJobCreatedType }

// ScheduledJobUpdated is emitted on update.
type ScheduledJobUpdated struct{ commonEvent }

func (ScheduledJobUpdated) EventType() string { return ScheduledJobUpdatedType }

// ScheduledJobPaused is emitted on pause.
type ScheduledJobPaused struct{ commonEvent }

func (ScheduledJobPaused) EventType() string { return ScheduledJobPausedType }

// ScheduledJobResumed is emitted on resume.
type ScheduledJobResumed struct{ commonEvent }

func (ScheduledJobResumed) EventType() string { return ScheduledJobResumedType }

// ScheduledJobArchived is emitted on archive.
type ScheduledJobArchived struct{ commonEvent }

func (ScheduledJobArchived) EventType() string { return ScheduledJobArchivedType }

// ScheduledJobDeleted is emitted on delete.
type ScheduledJobDeleted struct{ commonEvent }

func (ScheduledJobDeleted) EventType() string { return ScheduledJobDeletedType }

// ScheduledJobFiredManually is emitted when a human admin triggers a
// fire outside the normal cron schedule. Recorded for audit; the
// resulting instance row is written by the scheduler dispatcher (Wave 3g).
type ScheduledJobFiredManually struct {
	commonEvent
	InstanceID string
}

func (ScheduledJobFiredManually) EventType() string { return ScheduledJobFiredManuallyType }
func (e ScheduledJobFiredManually) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID         string `json:"scheduledJobId"`
		Code       string `json:"code"`
		InstanceID string `json:"instanceId"`
	}{e.ScheduledJobID, e.Code, e.InstanceID})
}

// ScheduledJobsSynced is the rollup emitted by the SDK scheduled-job sync
// (SyncScheduledJobs). Unlike the other sync rollups it carries the affected
// job IDs (not just counts), since the SDK contract returns the created /
// updated / archived ID arrays. Mirrors the Rust ScheduledJobsSynced event.
type ScheduledJobsSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	ClientID        *string
	Created         []string
	Updated         []string
	Archived        []string
}

func (e ScheduledJobsSynced) EventID() string     { return e.Metadata.EventID }
func (e ScheduledJobsSynced) EventType() string   { return ScheduledJobsSyncedType }
func (e ScheduledJobsSynced) SpecVersion() string { return "1.0" }
func (e ScheduledJobsSynced) Source() string      { return Source }
func (e ScheduledJobsSynced) Subject() string {
	return "platform.scheduledjobs.synced." + e.ApplicationCode
}
func (e ScheduledJobsSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ScheduledJobsSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ScheduledJobsSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ScheduledJobsSynced) CausationID() string   { return e.Metadata.CausationID }
func (e ScheduledJobsSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ScheduledJobsSynced) MessageGroup() string  { return "platform:scheduledjobs:synced" }
func (e ScheduledJobsSynced) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         []string `json:"created"`
		Updated         []string `json:"updated"`
		Archived        []string `json:"archived"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Archived})
}
