// Package dispatchpool is the port of fc-platform/src/dispatch_pool.
// Encapsulates rate-limit + concurrency settings used by the message router.
package dispatchpool

import (
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Status is the lifecycle state of a pool.
type Status string

const (
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusArchived  Status = "ARCHIVED"
)

// ParseStatus is the lenient parser. Unknown → ACTIVE.
func ParseStatus(s string) Status {
	switch s {
	case string(StatusSuspended):
		return StatusSuspended
	case string(StatusArchived):
		return StatusArchived
	default:
		return StatusActive
	}
}

// DispatchPool is the aggregate root.
type DispatchPool struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	// RateLimit is messages per minute. nil → no rate limit, concurrency-only.
	RateLimit        *int32    `json:"rateLimit,omitempty"`
	Concurrency      int32     `json:"concurrency"`
	ClientID         *string   `json:"clientId,omitempty"`
	ClientIdentifier *string   `json:"clientIdentifier,omitempty"`
	Status           Status    `json:"status"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (p DispatchPool) IDStr() string { return p.ID }

// New constructs a DispatchPool with defaults (concurrency=10, status=ACTIVE).
func New(code, name string) *DispatchPool {
	now := time.Now().UTC()
	return &DispatchPool{
		ID:          tsid.Generate(tsid.DispatchPool),
		Code:        code,
		Name:        name,
		Concurrency: 10,
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// Suspend flips status to SUSPENDED.
func (p *DispatchPool) Suspend() {
	p.Status = StatusSuspended
	p.UpdatedAt = time.Now().UTC()
}

// Activate flips status to ACTIVE.
func (p *DispatchPool) Activate() {
	p.Status = StatusActive
	p.UpdatedAt = time.Now().UTC()
}

// Archive flips status to ARCHIVED.
func (p *DispatchPool) Archive() {
	p.Status = StatusArchived
	p.UpdatedAt = time.Now().UTC()
}
