package operations

import (
	"context"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/emaildomainmapping"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// CreateCommand is the input DTO.
type CreateCommand struct {
	EmailDomain          string   `json:"emailDomain"`
	IdentityProviderID   string   `json:"identityProviderId"`
	ScopeType            string   `json:"scopeType"`
	PrimaryClientID      *string  `json:"primaryClientId,omitempty"`
	AdditionalClientIDs  []string `json:"additionalClientIds,omitempty"`
	GrantedClientIDs     []string `json:"grantedClientIds,omitempty"`
	RequiredOIDCTenantID *string  `json:"requiredOidcTenantId,omitempty"`
	AllowedRoleIDs       []string `json:"allowedRoleIds,omitempty"`
	SyncRolesFromIDP     bool     `json:"syncRolesFromIdp"`
	// 2FA enforcement (internal-auth domains only).
	Require2FA            bool     `json:"require2fa"`
	Allowed2FAMethods     []string `json:"allowed2faMethods,omitempty"`
	RememberDeviceEnabled bool     `json:"rememberDeviceEnabled"`
	RememberDeviceDays    int      `json:"rememberDeviceDays,omitempty"`
}

// validate2FA checks the 2FA fields: every method must be known, and at least
// one method must be allowed when 2FA is required.
func validate2FA(require2FA bool, methods []string) error {
	for _, m := range methods {
		if !emaildomainmapping.ValidMFAMethod(m) {
			return usecase.Validation("INVALID_2FA_METHOD",
				"allowed2faMethods entries must be TOTP or EMAIL_PIN")
		}
	}
	if require2FA && len(methods) == 0 {
		return usecase.Validation("2FA_METHOD_REQUIRED",
			"at least one 2FA method must be allowed when require2fa is set")
	}
	return nil
}

// CreateMapping creates a new email-domain → IdP mapping and emits
// EmailDomainMappingCreated.
func CreateMapping(
	ctx context.Context,
	repo *emaildomainmapping.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd CreateCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[EmailDomainMappingCreated], error) {
	var zero commit.Committed[EmailDomainMappingCreated]

	domain := strings.ToLower(strings.TrimSpace(cmd.EmailDomain))
	if domain == "" {
		return zero, usecase.Validation("EMAIL_DOMAIN_REQUIRED", "Email domain is required")
	}
	if !strings.Contains(domain, ".") || strings.ContainsAny(domain, " /@") {
		return zero, usecase.Validation("INVALID_EMAIL_DOMAIN", "Email domain must be a valid DNS name (e.g. example.com)")
	}
	if strings.TrimSpace(cmd.IdentityProviderID) == "" {
		return zero, usecase.Validation("IDP_REQUIRED", "identityProviderId is required")
	}
	switch cmd.ScopeType {
	case "ANCHOR", "PARTNER", "CLIENT":
	default:
		return zero, usecase.Validation("INVALID_SCOPE_TYPE", "scopeType must be ANCHOR, PARTNER, or CLIENT")
	}
	if (cmd.ScopeType == "PARTNER" || cmd.ScopeType == "CLIENT") && cmd.PrimaryClientID == nil {
		return zero, usecase.Validation("PRIMARY_CLIENT_REQUIRED",
			"primaryClientId is required for PARTNER and CLIENT scope")
	}
	if err := validate2FA(cmd.Require2FA, cmd.Allowed2FAMethods); err != nil {
		return zero, err
	}

	existing, err := repo.FindByEmailDomain(ctx, domain)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_email_domain failed", err)
	}
	if existing != nil {
		return zero, usecase.Conflict("DOMAIN_ALREADY_MAPPED",
			"Email domain '"+domain+"' is already mapped")
	}

	e := emaildomainmapping.New(domain, cmd.IdentityProviderID, emaildomainmapping.ParseScopeType(cmd.ScopeType))
	e.PrimaryClientID = cmd.PrimaryClientID
	e.RequiredOIDCTenantID = cmd.RequiredOIDCTenantID
	e.SyncRolesFromIDP = cmd.SyncRolesFromIDP
	e.Require2FA = cmd.Require2FA
	e.RememberDeviceEnabled = cmd.RememberDeviceEnabled
	if cmd.RememberDeviceDays > 0 {
		e.RememberDeviceDays = cmd.RememberDeviceDays
	}
	if cmd.Allowed2FAMethods != nil {
		e.Allowed2FAMethods = cmd.Allowed2FAMethods
	}
	if cmd.AdditionalClientIDs != nil {
		e.AdditionalClientIDs = cmd.AdditionalClientIDs
	}
	if cmd.GrantedClientIDs != nil {
		e.GrantedClientIDs = cmd.GrantedClientIDs
	}
	if cmd.AllowedRoleIDs != nil {
		e.AllowedRoleIDs = cmd.AllowedRoleIDs
	}

	event := EmailDomainMappingCreated{
		Metadata:    usecase.NewEventMetadata(ec, EmailDomainMappingCreatedType, Source, subjectFor(e.ID)),
		MappingID:   e.ID,
		EmailDomain: e.EmailDomain,
	}
	return commit.Save(ctx, uow, e, repo, event, cmd)
}
