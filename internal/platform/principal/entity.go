// Package principal is the port of fc-platform/src/principal.
// Unified user/service-account aggregate.
//
// Phase 3c scope: core ops (create, update, delete, activate, deactivate,
// reset_password). The following ops are explicitly deferred to a focused
// follow-up because they involve junction-table writes (iam_principal_roles,
// iam_client_access_grants, iam_principal_application_access):
//   - assign_roles
//   - grant_client_access / revoke_client_access
//   - assign_application_access
//   - sync (bulk SDK upsert)
//
// When those land, this entity already carries the fields (Roles,
// AssignedClients, AccessibleApplicationIDs) so the storage layer extends
// in place.
package principal

import (
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/internal/tsid"
)

// Type is the principal kind.
type Type string

const (
	TypeUser    Type = "USER"
	TypeService Type = "SERVICE"
)

// ParseType is the lenient parser. Unknown → USER.
func ParseType(s string) Type {
	if s == string(TypeService) {
		return TypeService
	}
	return TypeUser
}

// UserScope determines client access level.
type UserScope string

const (
	ScopeAnchor  UserScope = "ANCHOR"
	ScopePartner UserScope = "PARTNER"
	ScopeClient  UserScope = "CLIENT"
)

// ParseScope is the lenient parser. Unknown → CLIENT (most restrictive).
func ParseScope(s string) UserScope {
	switch s {
	case string(ScopeAnchor):
		return ScopeAnchor
	case string(ScopePartner):
		return ScopePartner
	default:
		return ScopeClient
	}
}

// IsAnchor reports whether the scope can access all clients.
func (s UserScope) IsAnchor() bool { return s == ScopeAnchor }

// CanAccessClient evaluates per-scope client access.
func (s UserScope) CanAccessClient(clientID string, homeClientID *string, assignedClients []string) bool {
	switch s {
	case ScopeAnchor:
		return true
	case ScopePartner:
		for _, c := range assignedClients {
			if c == clientID {
				return true
			}
		}
		return false
	default: // CLIENT
		return homeClientID != nil && *homeClientID == clientID
	}
}

// UserIdentity carries human-user identity fields.
type UserIdentity struct {
	Email         string     `json:"email"`
	EmailVerified bool       `json:"emailVerified"`
	FirstName     *string    `json:"firstName,omitempty"`
	LastName      *string    `json:"lastName,omitempty"`
	PictureURL    *string    `json:"pictureUrl,omitempty"`
	Phone         *string    `json:"phone,omitempty"`
	ExternalID    *string    `json:"externalId,omitempty"`
	Provider      *string    `json:"provider,omitempty"`
	PasswordHash  *string    `json:"passwordHash,omitempty"`
	LastLoginAt   *time.Time `json:"lastLoginAt,omitempty"`
}

// NewUserIdentity builds an empty identity with the supplied email.
func NewUserIdentity(email string) *UserIdentity {
	return &UserIdentity{Email: email}
}

// DisplayName picks the best human-readable label.
func (u UserIdentity) DisplayName() string {
	if u.FirstName != nil && u.LastName != nil {
		return *u.FirstName + " " + *u.LastName
	}
	if u.FirstName != nil {
		return *u.FirstName
	}
	if u.LastName != nil {
		return *u.LastName
	}
	return u.Email
}

// ExternalIdentity references the OIDC provider for federated users.
type ExternalIdentity struct {
	ProviderID string `json:"providerId"`
	ExternalID string `json:"externalId"`
}

// Principal is the aggregate root. Unified for users and service accounts.
type Principal struct {
	ID                       string                          `json:"id"`
	Type                     Type                            `json:"type"`
	Scope                    UserScope                       `json:"scope"`
	ClientID                 *string                         `json:"clientId,omitempty"`
	ApplicationID            *string                         `json:"applicationId,omitempty"`
	Name                     string                          `json:"name"`
	Active                   bool                            `json:"active"`
	UserIdentity             *UserIdentity                   `json:"userIdentity,omitempty"`
	ServiceAccountID         *string                         `json:"serviceAccountId,omitempty"`
	Roles                    []serviceaccount.RoleAssignment `json:"roles"`
	AssignedClients          []string                        `json:"assignedClients"`
	ClientIdentifierMap      map[string]string               `json:"clientIdentifierMap,omitempty"`
	AccessibleApplicationIDs []string                        `json:"accessibleApplicationIds"`
	ExternalIdentity         *ExternalIdentity               `json:"externalIdentity,omitempty"`
	CreatedAt                time.Time                       `json:"createdAt"`
	UpdatedAt                time.Time                       `json:"updatedAt"`
}

// IDStr satisfies usecase.HasID.
func (p Principal) IDStr() string { return p.ID }

// IsUser reports whether this principal is a USER (vs SERVICE).
func (p Principal) IsUser() bool { return p.Type == TypeUser }

// IsService reports whether this principal is a SERVICE account.
func (p Principal) IsService() bool { return p.Type == TypeService }

// NewUser constructs a USER-type Principal with the supplied email/scope.
func NewUser(email string, scope UserScope) *Principal {
	now := time.Now().UTC()
	identity := NewUserIdentity(email)
	return &Principal{
		ID:                       tsid.Generate(tsid.Principal),
		Type:                     TypeUser,
		Scope:                    scope,
		Name:                     identity.DisplayName(),
		Active:                   true,
		UserIdentity:             identity,
		Roles:                    []serviceaccount.RoleAssignment{},
		AssignedClients:          []string{},
		AccessibleApplicationIDs: []string{},
		CreatedAt:                now,
		UpdatedAt:                now,
	}
}

// NewService constructs a SERVICE-type Principal.
func NewService(serviceAccountID, name string) *Principal {
	now := time.Now().UTC()
	return &Principal{
		ID:                       tsid.Generate(tsid.Principal),
		Type:                     TypeService,
		Scope:                    ScopeAnchor,
		Name:                     name,
		Active:                   true,
		ServiceAccountID:         &serviceAccountID,
		Roles:                    []serviceaccount.RoleAssignment{},
		AssignedClients:          []string{},
		AccessibleApplicationIDs: []string{},
		CreatedAt:                now,
		UpdatedAt:                now,
	}
}

// Activate flips Active=true.
func (p *Principal) Activate() {
	p.Active = true
	p.UpdatedAt = time.Now().UTC()
}

// Deactivate flips Active=false.
func (p *Principal) Deactivate() {
	p.Active = false
	p.UpdatedAt = time.Now().UTC()
}

// SetPasswordHash updates the password hash on the user identity.
func (p *Principal) SetPasswordHash(hash string) {
	if p.UserIdentity == nil {
		p.UserIdentity = NewUserIdentity("")
	}
	p.UserIdentity.PasswordHash = &hash
	p.UpdatedAt = time.Now().UTC()
}
