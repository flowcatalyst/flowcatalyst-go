// Package subscription is the port of fc-platform/src/subscription.
// Binds event types to delivery targets (connections / endpoints).
package subscription

import (
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Status is the subscription lifecycle state.
type Status string

const (
	StatusActive Status = "ACTIVE"
	StatusPaused Status = "PAUSED"
)

// ParseStatus is the lenient parser. Unknown → ACTIVE.
func ParseStatus(s string) Status {
	if s == string(StatusPaused) {
		return StatusPaused
	}
	return StatusActive
}

// Source identifies where the subscription was authored.
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

// EventTypeBinding maps an event-type pattern (with wildcards) to this
// subscription. Stored in msg_subscription_event_types.
type EventTypeBinding struct {
	EventTypeID   *string `json:"eventTypeId,omitempty"`
	EventTypeCode string  `json:"eventTypeCode"`
	SpecVersion   *string `json:"specVersion,omitempty"`
	Filter        *string `json:"filter,omitempty"`
}

// NewEventTypeBinding builds an EventTypeBinding.
func NewEventTypeBinding(code string) EventTypeBinding {
	return EventTypeBinding{EventTypeCode: code}
}

// Matches reports whether the binding's pattern matches the given event-type code.
// Patterns are colon-separated; `*` matches any single segment.
func (b EventTypeBinding) Matches(eventTypeCode string) bool {
	patternParts := strings.Split(b.EventTypeCode, ":")
	eventParts := strings.Split(eventTypeCode, ":")
	if len(patternParts) != len(eventParts) {
		return false
	}
	for i, p := range patternParts {
		if p != "*" && p != eventParts[i] {
			return false
		}
	}
	return true
}

// ConfigEntry is a key/value pair stored in msg_subscription_custom_configs.
type ConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Subscription is the aggregate root.
type Subscription struct {
	ID               string              `json:"id"`
	Code             string              `json:"code"`
	ApplicationCode  *string             `json:"applicationCode,omitempty"`
	Name             string              `json:"name"`
	Description      *string             `json:"description,omitempty"`
	ClientID         *string             `json:"clientId,omitempty"`
	ClientIdentifier *string             `json:"clientIdentifier,omitempty"`
	ClientScoped     bool                `json:"clientScoped"`
	EventTypes       []EventTypeBinding  `json:"eventTypes"`
	ConnectionID     *string             `json:"connectionId,omitempty"`
	Endpoint         string              `json:"endpoint"`
	Queue            *string             `json:"queue,omitempty"`
	CustomConfig     []ConfigEntry       `json:"customConfig"`
	Source           Source              `json:"source"`
	Status           Status              `json:"status"`
	MaxAgeSeconds    int32               `json:"maxAgeSeconds"`
	DispatchPoolID   *string             `json:"dispatchPoolId,omitempty"`
	DispatchPoolCode *string             `json:"dispatchPoolCode,omitempty"`
	DelaySeconds     int32               `json:"delaySeconds"`
	Sequence         int32               `json:"sequence"`
	Mode             common.DispatchMode `json:"mode"`
	TimeoutSeconds   int32               `json:"timeoutSeconds"`
	MaxRetries       int32               `json:"maxRetries"`
	ServiceAccountID *string             `json:"serviceAccountId,omitempty"`
	DataOnly         bool                `json:"dataOnly"`
	CreatedBy        *string             `json:"createdBy,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (s Subscription) IDStr() string { return s.ID }

// New constructs a Subscription with platform defaults.
func New(code, name, endpoint string) *Subscription {
	now := time.Now().UTC()
	return &Subscription{
		ID:             tsid.Generate(tsid.Subscription),
		Code:           code,
		Name:           name,
		Endpoint:       endpoint,
		EventTypes:     []EventTypeBinding{},
		CustomConfig:   []ConfigEntry{},
		Source:         SourceUI,
		Status:         StatusActive,
		MaxAgeSeconds:  86400,
		DelaySeconds:   0,
		Sequence:       99,
		Mode:           common.DispatchImmediate,
		TimeoutSeconds: 30,
		MaxRetries:     3,
		DataOnly:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Pause flips status to PAUSED.
func (s *Subscription) Pause() {
	s.Status = StatusPaused
	s.UpdatedAt = time.Now().UTC()
}

// Resume flips status to ACTIVE.
func (s *Subscription) Resume() {
	s.Status = StatusActive
	s.UpdatedAt = time.Now().UTC()
}

// IsActive reports whether the subscription is currently active.
func (s *Subscription) IsActive() bool { return s.Status == StatusActive }

// MatchesEventType reports whether any binding matches.
func (s *Subscription) MatchesEventType(eventTypeCode string) bool {
	for _, b := range s.EventTypes {
		if b.Matches(eventTypeCode) {
			return true
		}
	}
	return false
}

// MatchesClient reports whether the subscription accepts events from this client.
func (s *Subscription) MatchesClient(eventClientID *string) bool {
	if s.ClientID == nil {
		return true
	}
	if eventClientID == nil {
		return false
	}
	return *s.ClientID == *eventClientID
}
