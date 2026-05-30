package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	DispatchPoolCreatedType   = "platform:admin:dispatch-pool:created"
	DispatchPoolUpdatedType   = "platform:admin:dispatch-pool:updated"
	DispatchPoolArchivedType  = "platform:admin:dispatch-pool:archived"
	DispatchPoolDeletedType   = "platform:admin:dispatch-pool:deleted"
	DispatchPoolSuspendedType = "platform:admin:dispatch-pool:suspended"
	DispatchPoolActivatedType = "platform:admin:dispatch-pool:activated"
	DispatchPoolsSyncedType   = "platform:admin:dispatch-pools:synced"
	Source                    = "platform:admin"
)

func subjectFor(id string) string { return "platform.dispatchpool." + id }
func groupFor(id string) string   { return "platform:dispatchpool:" + id }

type DispatchPoolCreated struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Code     string
	Name     string
}

func (e DispatchPoolCreated) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolCreated) EventType() string     { return DispatchPoolCreatedType }
func (e DispatchPoolCreated) SpecVersion() string   { return "1.0" }
func (e DispatchPoolCreated) Source() string        { return Source }
func (e DispatchPoolCreated) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolCreated) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolCreated) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Code   string `json:"code"`
		Name   string `json:"name"`
	}{e.PoolID, e.Code, e.Name})
}

type DispatchPoolUpdated struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Name     string
}

func (e DispatchPoolUpdated) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolUpdated) EventType() string     { return DispatchPoolUpdatedType }
func (e DispatchPoolUpdated) SpecVersion() string   { return "1.0" }
func (e DispatchPoolUpdated) Source() string        { return Source }
func (e DispatchPoolUpdated) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolUpdated) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Name   string `json:"name"`
	}{e.PoolID, e.Name})
}

type DispatchPoolArchived struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Code     string
}

func (e DispatchPoolArchived) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolArchived) EventType() string     { return DispatchPoolArchivedType }
func (e DispatchPoolArchived) SpecVersion() string   { return "1.0" }
func (e DispatchPoolArchived) Source() string        { return Source }
func (e DispatchPoolArchived) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolArchived) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolArchived) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolArchived) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolArchived) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolArchived) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolArchived) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolArchived) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Code   string `json:"code"`
	}{e.PoolID, e.Code})
}

type DispatchPoolDeleted struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Code     string
}

func (e DispatchPoolDeleted) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolDeleted) EventType() string     { return DispatchPoolDeletedType }
func (e DispatchPoolDeleted) SpecVersion() string   { return "1.0" }
func (e DispatchPoolDeleted) Source() string        { return Source }
func (e DispatchPoolDeleted) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolDeleted) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Code   string `json:"code"`
	}{e.PoolID, e.Code})
}

type DispatchPoolSuspended struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Code     string
}

func (e DispatchPoolSuspended) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolSuspended) EventType() string     { return DispatchPoolSuspendedType }
func (e DispatchPoolSuspended) SpecVersion() string   { return "1.0" }
func (e DispatchPoolSuspended) Source() string        { return Source }
func (e DispatchPoolSuspended) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolSuspended) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolSuspended) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolSuspended) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolSuspended) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolSuspended) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolSuspended) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolSuspended) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Code   string `json:"code"`
	}{e.PoolID, e.Code})
}

type DispatchPoolActivated struct {
	Metadata usecase.EventMetadata
	PoolID   string
	Code     string
}

func (e DispatchPoolActivated) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolActivated) EventType() string     { return DispatchPoolActivatedType }
func (e DispatchPoolActivated) SpecVersion() string   { return "1.0" }
func (e DispatchPoolActivated) Source() string        { return Source }
func (e DispatchPoolActivated) Subject() string       { return subjectFor(e.PoolID) }
func (e DispatchPoolActivated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolActivated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolActivated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolActivated) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolActivated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolActivated) MessageGroup() string  { return groupFor(e.PoolID) }
func (e DispatchPoolActivated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		PoolID string `json:"poolId"`
		Code   string `json:"code"`
	}{e.PoolID, e.Code})
}

// DispatchPoolsSynced is the rollup emitted by the SDK dispatch-pool sync
// (SyncDispatchPools). Dispatch pools are global (matched by code), so the
// ApplicationCode is carried for audit/event provenance only. Mirrors the
// Rust DispatchPoolsSynced event.
type DispatchPoolsSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	Created         uint32
	Updated         uint32
	Deleted         uint32
	SyncedCodes     []string
}

func (e DispatchPoolsSynced) EventID() string       { return e.Metadata.EventID }
func (e DispatchPoolsSynced) EventType() string     { return DispatchPoolsSyncedType }
func (e DispatchPoolsSynced) SpecVersion() string   { return "1.0" }
func (e DispatchPoolsSynced) Source() string        { return Source }
func (e DispatchPoolsSynced) Subject() string       { return "platform.dispatchpools." + e.ApplicationCode }
func (e DispatchPoolsSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e DispatchPoolsSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e DispatchPoolsSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e DispatchPoolsSynced) CausationID() string   { return e.Metadata.CausationID }
func (e DispatchPoolsSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e DispatchPoolsSynced) MessageGroup() string  { return "platform:dispatchpools" }
func (e DispatchPoolsSynced) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         uint32   `json:"created"`
		Updated         uint32   `json:"updated"`
		Deleted         uint32   `json:"deleted"`
		SyncedCodes     []string `json:"syncedCodes"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Deleted, e.SyncedCodes})
}
