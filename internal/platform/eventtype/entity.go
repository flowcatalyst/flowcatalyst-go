// Package eventtype is the worked-example port of fc-platform/src/event_type.
// Every other subdomain follows this exact shape (entity.go, repository.go,
// operations/, api.go).
package eventtype

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// EventTypeStatus mirrors the Rust enum (CURRENT | ARCHIVED).
type Status string

const (
	StatusCurrent  Status = "CURRENT"
	StatusArchived Status = "ARCHIVED"
)

// ParseStatus is the lenient parser. Unknown → CURRENT.
func ParseStatus(s string) Status {
	if s == string(StatusArchived) {
		return StatusArchived
	}
	return StatusCurrent
}

// Source identifies where the event type was authored.
type Source string

const (
	SourceCode Source = "CODE"
	SourceAPI  Source = "API"
	SourceUI   Source = "UI"
)

// ParseSource is the lenient parser. Unknown → UI.
func ParseSource(s string) Source {
	switch s {
	case string(SourceCode):
		return SourceCode
	case string(SourceAPI):
		return SourceAPI
	default:
		return SourceUI
	}
}

// SpecVersionStatus is the schema-version state.
type SpecVersionStatus string

const (
	SpecFinalising SpecVersionStatus = "FINALISING"
	SpecCurrent    SpecVersionStatus = "CURRENT"
	SpecDeprecated SpecVersionStatus = "DEPRECATED"
)

// ParseSpecVersionStatus is the lenient parser. Unknown → FINALISING.
func ParseSpecVersionStatus(s string) SpecVersionStatus {
	switch s {
	case string(SpecCurrent):
		return SpecCurrent
	case string(SpecDeprecated):
		return SpecDeprecated
	default:
		return SpecFinalising
	}
}

// SchemaType identifies the payload language of a schema.
type SchemaType string

const (
	SchemaJSON  SchemaType = "JSON_SCHEMA"
	SchemaXSD   SchemaType = "XSD"
	SchemaProto SchemaType = "PROTO"
)

// ParseSchemaType is the lenient parser with aliases (XML_SCHEMA→XSD, PROTOBUF→PROTO).
func ParseSchemaType(s string) SchemaType {
	switch s {
	case "XSD", "XML_SCHEMA":
		return SchemaXSD
	case "PROTO", "PROTOBUF":
		return SchemaProto
	default:
		return SchemaJSON
	}
}

// SpecVersion is a schema version row.
type SpecVersion struct {
	ID            string            `json:"id"`
	EventTypeID   string            `json:"eventTypeId"`
	Version       string            `json:"version"`
	MimeType      string            `json:"mimeType"`
	SchemaContent json.RawMessage   `json:"schemaContent,omitempty"`
	SchemaType    SchemaType        `json:"schemaType"`
	Status        SpecVersionStatus `json:"status"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// NewSpecVersion mints a new schema version.
func NewSpecVersion(eventTypeID, version string, schemaContent json.RawMessage) SpecVersion {
	now := time.Now().UTC()
	return SpecVersion{
		ID:            tsid.Generate(tsid.Schema),
		EventTypeID:   eventTypeID,
		Version:       version,
		MimeType:      "application/schema+json",
		SchemaContent: schemaContent,
		SchemaType:    SchemaJSON,
		Status:        SpecFinalising,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// IsCurrent / IsDeprecated mirror the Rust helpers.
func (s *SpecVersion) IsCurrent() bool    { return s.Status == SpecCurrent }
func (s *SpecVersion) IsDeprecated() bool { return s.Status == SpecDeprecated }

// majorVersion returns the leading numeric segment of a semver-style
// version string (e.g. "1.0" → "1", "2.3.4-alpha" → "2"). Returns
// "" when no major segment can be parsed; callers treat that as "no
// auto-deprecate sibling lookup possible".
func majorVersion(v string) string {
	for i, c := range v {
		if c == '.' || c == '-' || c == '+' {
			return v[:i]
		}
	}
	return v
}

// Major returns the leading numeric major segment of the version
// string. Used by the finalise logic to find sibling CURRENT versions
// that should be auto-deprecated. Exposed so use cases can match
// without re-implementing the parse.
func (s *SpecVersion) Major() string { return majorVersion(s.Version) }

// EventType is the aggregate root.
type EventType struct {
	ID           string        `json:"id"`
	Code         string        `json:"code"`
	Name         string        `json:"name"`
	Description  *string       `json:"description,omitempty"`
	SpecVersions []SpecVersion `json:"specVersions"`
	Status       Status        `json:"status"`
	Source       Source        `json:"source"`
	ClientScoped bool          `json:"clientScoped"`
	Application  string        `json:"application"`
	Subdomain    string        `json:"subdomain"`
	Aggregate    string        `json:"aggregate"`
	EventName    string        `json:"eventName"`
	ClientID     *string       `json:"clientId,omitempty"`
	CreatedBy    *string       `json:"createdBy,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

// IDStr returns the aggregate ID. Method exists because usecase.HasID
// is an interface constraint, and the `ID` field can't satisfy it.
// Value receiver so both EventType and *EventType satisfy HasID — the
// generic call site in operations/*.go passes EventType as the type
// parameter and *EventType as the value.
func (e EventType) IDStr() string { return e.ID }

// New constructs an EventType from a colon-separated code. Returns an
// error if the code is invalid (must be application:subdomain:aggregate:event).
func New(code, name string) (*EventType, error) {
	parts := strings.Split(code, ":")
	if len(parts) != 4 {
		return nil, errors.New("event type code must follow format: application:subdomain:aggregate:event")
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return nil, errors.New("event type code segments cannot be empty")
		}
	}
	now := time.Now().UTC()
	return &EventType{
		ID:           tsid.Generate(tsid.EventType),
		Code:         code,
		Name:         name,
		SpecVersions: []SpecVersion{},
		Status:       StatusCurrent,
		Source:       SourceUI,
		ClientScoped: false,
		Application:  parts[0],
		Subdomain:    parts[1],
		Aggregate:    parts[2],
		EventName:    parts[3],
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Archive flips status to ARCHIVED and bumps UpdatedAt.
func (e *EventType) Archive() {
	e.Status = StatusArchived
	e.UpdatedAt = time.Now().UTC()
}

// AddSchemaVersion appends a schema version and bumps UpdatedAt.
func (e *EventType) AddSchemaVersion(sv SpecVersion) {
	e.SpecVersions = append(e.SpecVersions, sv)
	e.UpdatedAt = time.Now().UTC()
}
