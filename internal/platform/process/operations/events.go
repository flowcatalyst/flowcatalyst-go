package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	ProcessCreatedType  = "platform:admin:process:created"
	ProcessUpdatedType  = "platform:admin:process:updated"
	ProcessArchivedType = "platform:admin:process:archived"
	ProcessDeletedType  = "platform:admin:process:deleted"
	ProcessesSyncedType = "platform:admin:processes:synced"
	Source              = "platform:admin"
)

func subjectFor(id string) string { return "platform.process." + id }
func groupFor(id string) string   { return "platform:process:" + id }

type ProcessCreated struct {
	Metadata  usecase.EventMetadata
	ProcessID string
	Code      string
	Name      string
}

func (e ProcessCreated) EventID() string       { return e.Metadata.EventID }
func (e ProcessCreated) EventType() string     { return ProcessCreatedType }
func (e ProcessCreated) SpecVersion() string   { return "1.0" }
func (e ProcessCreated) Source() string        { return Source }
func (e ProcessCreated) Subject() string       { return subjectFor(e.ProcessID) }
func (e ProcessCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ProcessCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ProcessCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ProcessCreated) CausationID() string   { return e.Metadata.CausationID }
func (e ProcessCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ProcessCreated) MessageGroup() string  { return groupFor(e.ProcessID) }
func (e ProcessCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ProcessID string `json:"processId"`
		Code      string `json:"code"`
		Name      string `json:"name"`
	}{e.ProcessID, e.Code, e.Name})
}

type ProcessUpdated struct {
	Metadata  usecase.EventMetadata
	ProcessID string
	Name      string
}

func (e ProcessUpdated) EventID() string       { return e.Metadata.EventID }
func (e ProcessUpdated) EventType() string     { return ProcessUpdatedType }
func (e ProcessUpdated) SpecVersion() string   { return "1.0" }
func (e ProcessUpdated) Source() string        { return Source }
func (e ProcessUpdated) Subject() string       { return subjectFor(e.ProcessID) }
func (e ProcessUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ProcessUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ProcessUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ProcessUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e ProcessUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ProcessUpdated) MessageGroup() string  { return groupFor(e.ProcessID) }
func (e ProcessUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ProcessID string `json:"processId"`
		Name      string `json:"name"`
	}{e.ProcessID, e.Name})
}

type ProcessArchived struct {
	Metadata  usecase.EventMetadata
	ProcessID string
	Code      string
}

func (e ProcessArchived) EventID() string       { return e.Metadata.EventID }
func (e ProcessArchived) EventType() string     { return ProcessArchivedType }
func (e ProcessArchived) SpecVersion() string   { return "1.0" }
func (e ProcessArchived) Source() string        { return Source }
func (e ProcessArchived) Subject() string       { return subjectFor(e.ProcessID) }
func (e ProcessArchived) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ProcessArchived) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ProcessArchived) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ProcessArchived) CausationID() string   { return e.Metadata.CausationID }
func (e ProcessArchived) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ProcessArchived) MessageGroup() string  { return groupFor(e.ProcessID) }
func (e ProcessArchived) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ProcessID string `json:"processId"`
		Code      string `json:"code"`
	}{e.ProcessID, e.Code})
}

type ProcessDeleted struct {
	Metadata  usecase.EventMetadata
	ProcessID string
	Code      string
}

func (e ProcessDeleted) EventID() string       { return e.Metadata.EventID }
func (e ProcessDeleted) EventType() string     { return ProcessDeletedType }
func (e ProcessDeleted) SpecVersion() string   { return "1.0" }
func (e ProcessDeleted) Source() string        { return Source }
func (e ProcessDeleted) Subject() string       { return subjectFor(e.ProcessID) }
func (e ProcessDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ProcessDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ProcessDeleted) CorrelationID() string { return e.Metadata.CausationID }
func (e ProcessDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e ProcessDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ProcessDeleted) MessageGroup() string  { return groupFor(e.ProcessID) }
func (e ProcessDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ProcessID string `json:"processId"`
		Code      string `json:"code"`
	}{e.ProcessID, e.Code})
}

// ProcessesSynced is the rollup emitted by the SDK app-scoped process sync
// (SyncProcesses). Carries the create/update/delete counts plus the synced
// codes. Mirrors the Rust ProcessesSynced event.
type ProcessesSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	Created         uint32
	Updated         uint32
	Deleted         uint32
	SyncedCodes     []string
}

func (e ProcessesSynced) EventID() string       { return e.Metadata.EventID }
func (e ProcessesSynced) EventType() string     { return ProcessesSyncedType }
func (e ProcessesSynced) SpecVersion() string   { return "1.0" }
func (e ProcessesSynced) Source() string        { return Source }
func (e ProcessesSynced) Subject() string       { return "platform.processes." + e.ApplicationCode }
func (e ProcessesSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e ProcessesSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e ProcessesSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e ProcessesSynced) CausationID() string   { return e.Metadata.CausationID }
func (e ProcessesSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e ProcessesSynced) MessageGroup() string  { return "platform:processes" }
func (e ProcessesSynced) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         uint32   `json:"created"`
		Updated         uint32   `json:"updated"`
		Deleted         uint32   `json:"deleted"`
		SyncedCodes     []string `json:"syncedCodes"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Deleted, e.SyncedCodes})
}
