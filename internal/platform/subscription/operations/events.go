package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	SubscriptionCreatedType = "platform:admin:subscription:created"
	SubscriptionUpdatedType = "platform:admin:subscription:updated"
	SubscriptionDeletedType = "platform:admin:subscription:deleted"
	SubscriptionPausedType  = "platform:admin:subscription:paused"
	SubscriptionResumedType = "platform:admin:subscription:resumed"
	SubscriptionsSyncedType = "platform:admin:subscription:synced"
	Source                  = "platform:admin"
)

func subjectFor(id string) string { return "platform.subscription." + id }
func groupFor(id string) string   { return "platform:subscription:" + id }

// SubscriptionCreated is emitted on create.
type SubscriptionCreated struct {
	Metadata       usecase.EventMetadata
	SubscriptionID string
	Code           string
	Name           string
}

func (e SubscriptionCreated) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionCreated) EventType() string     { return SubscriptionCreatedType }
func (e SubscriptionCreated) SpecVersion() string   { return "1.0" }
func (e SubscriptionCreated) Source() string        { return Source }
func (e SubscriptionCreated) Subject() string       { return subjectFor(e.SubscriptionID) }
func (e SubscriptionCreated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionCreated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionCreated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionCreated) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionCreated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionCreated) MessageGroup() string  { return groupFor(e.SubscriptionID) }
func (e SubscriptionCreated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		SubscriptionID string `json:"subscriptionId"`
		Code           string `json:"code"`
		Name           string `json:"name"`
	}{e.SubscriptionID, e.Code, e.Name})
}

// SubscriptionUpdated emitted on update.
type SubscriptionUpdated struct {
	Metadata       usecase.EventMetadata
	SubscriptionID string
	Name           string
}

func (e SubscriptionUpdated) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionUpdated) EventType() string     { return SubscriptionUpdatedType }
func (e SubscriptionUpdated) SpecVersion() string   { return "1.0" }
func (e SubscriptionUpdated) Source() string        { return Source }
func (e SubscriptionUpdated) Subject() string       { return subjectFor(e.SubscriptionID) }
func (e SubscriptionUpdated) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionUpdated) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionUpdated) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionUpdated) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionUpdated) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionUpdated) MessageGroup() string  { return groupFor(e.SubscriptionID) }
func (e SubscriptionUpdated) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		SubscriptionID string `json:"subscriptionId"`
		Name           string `json:"name"`
	}{e.SubscriptionID, e.Name})
}

// SubscriptionDeleted emitted on delete.
type SubscriptionDeleted struct {
	Metadata       usecase.EventMetadata
	SubscriptionID string
	Code           string
}

func (e SubscriptionDeleted) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionDeleted) EventType() string     { return SubscriptionDeletedType }
func (e SubscriptionDeleted) SpecVersion() string   { return "1.0" }
func (e SubscriptionDeleted) Source() string        { return Source }
func (e SubscriptionDeleted) Subject() string       { return subjectFor(e.SubscriptionID) }
func (e SubscriptionDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionDeleted) MessageGroup() string  { return groupFor(e.SubscriptionID) }
func (e SubscriptionDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		SubscriptionID string `json:"subscriptionId"`
		Code           string `json:"code"`
	}{e.SubscriptionID, e.Code})
}

// SubscriptionPaused emitted on pause.
type SubscriptionPaused struct {
	Metadata       usecase.EventMetadata
	SubscriptionID string
}

func (e SubscriptionPaused) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionPaused) EventType() string     { return SubscriptionPausedType }
func (e SubscriptionPaused) SpecVersion() string   { return "1.0" }
func (e SubscriptionPaused) Source() string        { return Source }
func (e SubscriptionPaused) Subject() string       { return subjectFor(e.SubscriptionID) }
func (e SubscriptionPaused) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionPaused) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionPaused) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionPaused) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionPaused) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionPaused) MessageGroup() string  { return groupFor(e.SubscriptionID) }
func (e SubscriptionPaused) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		SubscriptionID string `json:"subscriptionId"`
	}{e.SubscriptionID})
}

// SubscriptionResumed emitted on resume.
type SubscriptionResumed struct {
	Metadata       usecase.EventMetadata
	SubscriptionID string
}

func (e SubscriptionResumed) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionResumed) EventType() string     { return SubscriptionResumedType }
func (e SubscriptionResumed) SpecVersion() string   { return "1.0" }
func (e SubscriptionResumed) Source() string        { return Source }
func (e SubscriptionResumed) Subject() string       { return subjectFor(e.SubscriptionID) }
func (e SubscriptionResumed) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionResumed) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionResumed) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionResumed) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionResumed) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionResumed) MessageGroup() string  { return groupFor(e.SubscriptionID) }
func (e SubscriptionResumed) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		SubscriptionID string `json:"subscriptionId"`
	}{e.SubscriptionID})
}

// SubscriptionsSynced is the rollup emitted by the SDK app-scoped
// subscription sync (SyncSubscriptions). Mirrors the Rust SubscriptionsSynced
// event.
type SubscriptionsSynced struct {
	Metadata        usecase.EventMetadata
	ApplicationCode string
	Created         uint32
	Updated         uint32
	Deleted         uint32
	SyncedCodes     []string
}

func (e SubscriptionsSynced) EventID() string       { return e.Metadata.EventID }
func (e SubscriptionsSynced) EventType() string     { return SubscriptionsSyncedType }
func (e SubscriptionsSynced) SpecVersion() string   { return "1.0" }
func (e SubscriptionsSynced) Source() string        { return Source }
func (e SubscriptionsSynced) Subject() string       { return "platform.subscriptions." + e.ApplicationCode }
func (e SubscriptionsSynced) Time() time.Time       { return e.Metadata.OccurredAt }
func (e SubscriptionsSynced) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e SubscriptionsSynced) CorrelationID() string { return e.Metadata.CorrelationID }
func (e SubscriptionsSynced) CausationID() string   { return e.Metadata.CausationID }
func (e SubscriptionsSynced) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e SubscriptionsSynced) MessageGroup() string  { return "platform:subscriptions" }
func (e SubscriptionsSynced) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationCode string   `json:"applicationCode"`
		Created         uint32   `json:"created"`
		Updated         uint32   `json:"updated"`
		Deleted         uint32   `json:"deleted"`
		SyncedCodes     []string `json:"syncedCodes"`
	}{e.ApplicationCode, e.Created, e.Updated, e.Deleted, e.SyncedCodes})
}
