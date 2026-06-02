// dto.go contains the wire-format types for the event-type API. Kept
// separate from operations.CreateCommand / UpdateCommand so the wire
// shape can evolve independently of the use case input shape. The
// mapping functions here are the explicit translation layer.
package api

import (
	"encoding/json"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/httpcompat"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/shared/jsontime"
)

// CreateEventTypeRequest is the wire body for POST /api/event-types.
type CreateEventTypeRequest struct {
	Code        string          `json:"code" doc:"Event type code in application:subdomain:aggregate:event format" example:"platform:iam:user:created"`
	Name        string          `json:"name" doc:"Human-readable event type name"`
	Description *string         `json:"description,omitempty"`
	ClientID    *string         `json:"clientId,omitempty" doc:"Optional client scope; absent means anchor-level"`
	Schema      json.RawMessage `json:"schema,omitempty" doc:"Optional JSON Schema for the initial spec version"`
}

func (r CreateEventTypeRequest) toCommand() operations.CreateCommand {
	return operations.CreateCommand{
		Code:        r.Code,
		Name:        r.Name,
		Description: r.Description,
		ClientID:    r.ClientID,
		Schema:      r.Schema,
	}
}

// UpdateEventTypeRequest is the wire body for PUT /api/event-types/{id}.
// The path id is authoritative — body.id is ignored by the handler.
type UpdateEventTypeRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

func (r UpdateEventTypeRequest) toCommand(id string) operations.UpdateCommand {
	return operations.UpdateCommand{ID: id, Name: r.Name, Description: r.Description}
}

// AddSchemaRequest is the wire body for POST /api/event-types/{id}/schemas.
type AddSchemaRequest struct {
	Version string          `json:"version" doc:"Schema version (typically semver)" example:"1.0"`
	Schema  json.RawMessage `json:"schema" doc:"JSON Schema document"`
}

func (r AddSchemaRequest) toCommand(id string) operations.AddSchemaCommand {
	return operations.AddSchemaCommand{EventTypeID: id, Version: r.Version, Schema: r.Schema}
}

// EventTypeResponse is the wire shape for GET /api/event-types/{id}
// and POST /api/event-types/{id}/schemas. Mirrors eventtype.EventType
// with explicit JSON tags so the wire format is stable independent of
// entity-field renames.
type EventTypeResponse struct {
	ID           string                `json:"id"`
	Code         string                `json:"code"`
	Name         string                `json:"name"`
	Application  string                `json:"application"`
	Subdomain    string                `json:"subdomain"`
	Aggregate    string                `json:"aggregate"`
	EventName    string                `json:"eventName"`
	Description  *string               `json:"description,omitempty"`
	Status       string                `json:"status"`
	Source       string                `json:"source"`
	ClientID     *string               `json:"clientId,omitempty"`
	CreatedBy    *string               `json:"createdBy,omitempty"`
	CreatedAt    httpcompat.Time       `json:"createdAt"`
	UpdatedAt    httpcompat.Time       `json:"updatedAt"`
	SpecVersions []specVersionResponse `json:"specVersions"`
}

type specVersionResponse struct {
	Version   string          `json:"version"`
	Schema    json.RawMessage `json:"schema"`
	Status    string          `json:"status"`
	CreatedAt httpcompat.Time `json:"createdAt"`
}

func fromEntity(et *eventtype.EventType) EventTypeResponse {
	resp := EventTypeResponse{
		ID:          et.ID,
		Code:        et.Code,
		Name:        et.Name,
		Application: et.Application,
		Subdomain:   et.Subdomain,
		Aggregate:   et.Aggregate,
		EventName:   et.EventName,
		Description: et.Description,
		Status:      string(et.Status),
		Source:      string(et.Source),
		ClientID:    et.ClientID,
		CreatedBy:   et.CreatedBy,
		CreatedAt:   jsontime.New(et.CreatedAt),
		UpdatedAt:   jsontime.New(et.UpdatedAt),
	}
	resp.SpecVersions = make([]specVersionResponse, 0, len(et.SpecVersions))
	for _, sv := range et.SpecVersions {
		resp.SpecVersions = append(resp.SpecVersions, specVersionResponse{
			Version:   sv.Version,
			Schema:    sv.SchemaContent,
			Status:    string(sv.Status),
			CreatedAt: jsontime.New(sv.CreatedAt),
		})
	}
	return resp
}

// EventTypeListResponse is the wire shape for GET /api/event-types.
type EventTypeListResponse struct {
	Items []EventTypeResponse `json:"items"`
}
