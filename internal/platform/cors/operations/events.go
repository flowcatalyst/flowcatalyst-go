package operations

import (
	"encoding/json"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

const (
	CorsOriginAddedType   = "platform:admin:cors:origin-added"
	CorsOriginDeletedType = "platform:admin:cors:origin-deleted"
	CorsSource            = "platform:admin"
)

func subjectFor(id string) string { return "platform.cors." + id }
func groupFor(id string) string   { return "platform:cors:" + id }

// CorsOriginAdded is emitted when an origin is added.
type CorsOriginAdded struct {
	Metadata usecase.EventMetadata
	OriginID string
	Origin   string
}

func (e CorsOriginAdded) EventID() string       { return e.Metadata.EventID }
func (e CorsOriginAdded) EventType() string     { return CorsOriginAddedType }
func (e CorsOriginAdded) SpecVersion() string   { return "1.0" }
func (e CorsOriginAdded) Source() string        { return CorsSource }
func (e CorsOriginAdded) Subject() string       { return subjectFor(e.OriginID) }
func (e CorsOriginAdded) Time() time.Time       { return e.Metadata.OccurredAt }
func (e CorsOriginAdded) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e CorsOriginAdded) CorrelationID() string { return e.Metadata.CorrelationID }
func (e CorsOriginAdded) CausationID() string   { return e.Metadata.CausationID }
func (e CorsOriginAdded) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e CorsOriginAdded) MessageGroup() string  { return groupFor(e.OriginID) }
func (e CorsOriginAdded) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		OriginID string `json:"originId"`
		Origin   string `json:"origin"`
	}{e.OriginID, e.Origin})
}

// CorsOriginDeleted is emitted when an origin is deleted.
type CorsOriginDeleted struct {
	Metadata usecase.EventMetadata
	OriginID string
	Origin   string
}

func (e CorsOriginDeleted) EventID() string       { return e.Metadata.EventID }
func (e CorsOriginDeleted) EventType() string     { return CorsOriginDeletedType }
func (e CorsOriginDeleted) SpecVersion() string   { return "1.0" }
func (e CorsOriginDeleted) Source() string        { return CorsSource }
func (e CorsOriginDeleted) Subject() string       { return subjectFor(e.OriginID) }
func (e CorsOriginDeleted) Time() time.Time       { return e.Metadata.OccurredAt }
func (e CorsOriginDeleted) PrincipalID() string   { return e.Metadata.PrincipalID }
func (e CorsOriginDeleted) CorrelationID() string { return e.Metadata.CorrelationID }
func (e CorsOriginDeleted) CausationID() string   { return e.Metadata.CausationID }
func (e CorsOriginDeleted) ExecutionID() string   { return e.Metadata.ExecutionID }
func (e CorsOriginDeleted) MessageGroup() string  { return groupFor(e.OriginID) }
func (e CorsOriginDeleted) ToDataJSON() ([]byte, error) {
	return json.Marshal(struct {
		OriginID string `json:"originId"`
		Origin   string `json:"origin"`
	}{e.OriginID, e.Origin})
}
