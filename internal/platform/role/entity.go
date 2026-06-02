// Package role is the port of fc-platform/src/role. Defines RBAC role
// shapes with permission sets (incl. wildcard pattern matching).
package role

import (
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Source identifies where a role came from.
type Source string

const (
	SourceCode     Source = "CODE"
	SourceDatabase Source = "DATABASE"
	SourceSDK      Source = "SDK"
)

// ParseSource is the lenient parser. Unknown → DATABASE.
func ParseSource(s string) Source {
	switch s {
	case string(SourceCode):
		return SourceCode
	case string(SourceSDK):
		return SourceSDK
	default:
		return SourceDatabase
	}
}

// Permission is the per-permission catalog entry.
type Permission struct {
	Permission  string  `json:"permission"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Category    *string `json:"category,omitempty"`
}

// Role is the aggregate root. Mirrors AuthRole in Rust.
type Role struct {
	ID              string    `json:"id"`
	ApplicationID   *string   `json:"applicationId,omitempty"`
	Name            string    `json:"name"` // e.g. "platform:admin"
	DisplayName     string    `json:"displayName"`
	Description     *string   `json:"description,omitempty"`
	ApplicationCode string    `json:"applicationCode"`
	Permissions     []string  `json:"permissions"` // de-duplicated, sorted
	Source          Source    `json:"source"`
	ClientManaged   bool      `json:"clientManaged"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (r Role) IDStr() string { return r.ID }

// New constructs a Role with name = "{applicationCode}:{roleName}".
func New(applicationCode, roleName, displayName string) *Role {
	now := time.Now().UTC()
	return &Role{
		ID:              tsid.Generate(tsid.Role),
		Name:            applicationCode + ":" + roleName,
		DisplayName:     displayName,
		ApplicationCode: applicationCode,
		Permissions:     []string{},
		Source:          SourceDatabase,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// HasPermission reports whether the role grants the supplied permission,
// honoring 4-segment wildcard patterns (`a:b:c:d` where any segment may be `*`).
func (r *Role) HasPermission(p string) bool {
	for _, granted := range r.Permissions {
		if granted == p {
			return true
		}
		if matchesWildcard(granted, p) {
			return true
		}
	}
	return false
}

// GrantPermission adds a permission (de-duplicated) and bumps UpdatedAt.
func (r *Role) GrantPermission(p string) {
	for _, g := range r.Permissions {
		if g == p {
			return
		}
	}
	r.Permissions = append(r.Permissions, p)
	r.UpdatedAt = time.Now().UTC()
}

// RevokePermission removes a permission and bumps UpdatedAt.
func (r *Role) RevokePermission(p string) {
	out := r.Permissions[:0]
	for _, g := range r.Permissions {
		if g != p {
			out = append(out, g)
		}
	}
	r.Permissions = out
	r.UpdatedAt = time.Now().UTC()
}

// matchesWildcard checks 4-segment wildcard match: pattern segments
// may be `*` to match any value.
func matchesWildcard(pattern, value string) bool {
	pp := strings.Split(pattern, ":")
	vv := strings.Split(value, ":")
	if len(pp) != len(vv) {
		return false
	}
	for i, p := range pp {
		if p != "*" && p != vv[i] {
			return false
		}
	}
	return true
}
