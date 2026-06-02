package operations

import (
	"context"
	"regexp"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/common"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

var (
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	urlPattern  = regexp.MustCompile(`^https?://.+`)
)

// CreateCommand is the input DTO.
type CreateCommand struct {
	Code             string                          `json:"code"`
	Name             string                          `json:"name"`
	Endpoint         string                          `json:"endpoint"`
	Description      *string                         `json:"description,omitempty"`
	ClientID         *string                         `json:"clientId,omitempty"`
	ConnectionID     *string                         `json:"connectionId,omitempty"`
	DispatchPoolID   *string                         `json:"dispatchPoolId,omitempty"`
	ServiceAccountID *string                         `json:"serviceAccountId,omitempty"`
	EventTypes       []subscription.EventTypeBinding `json:"eventTypes,omitempty"`
	CustomConfig     []subscription.ConfigEntry      `json:"customConfig,omitempty"`
	Mode             string                          `json:"mode,omitempty"`
	TimeoutSeconds   *int32                          `json:"timeoutSeconds,omitempty"`
	MaxRetries       *int32                          `json:"maxRetries,omitempty"`
	DelaySeconds     *int32                          `json:"delaySeconds,omitempty"`
	MaxAgeSeconds    *int32                          `json:"maxAgeSeconds,omitempty"`
	DataOnly         *bool                           `json:"dataOnly,omitempty"`
}

// CreateSubscription validates cmd, enforces code uniqueness within the
// client scope, persists the subscription, and emits [SubscriptionCreated].
func CreateSubscription(
	ctx context.Context,
	repo *subscription.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd CreateCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[SubscriptionCreated], error) {
	var zero commit.Committed[SubscriptionCreated]

	code := strings.ToLower(strings.TrimSpace(cmd.Code))
	if code == "" {
		return zero, usecase.Validation("CODE_REQUIRED", "code is required")
	}
	if !codePattern.MatchString(code) {
		return zero, usecase.Validation("INVALID_CODE_FORMAT",
			"code must start with a lowercase letter and contain only lowercase alphanumeric and hyphens")
	}
	if strings.TrimSpace(cmd.Name) == "" {
		return zero, usecase.Validation("NAME_REQUIRED", "name is required")
	}
	if !urlPattern.MatchString(cmd.Endpoint) {
		return zero, usecase.Validation("INVALID_ENDPOINT", "endpoint must be a http(s) URL")
	}
	if len(cmd.EventTypes) == 0 {
		return zero, usecase.Validation("EVENT_TYPES_REQUIRED", "at least one event type binding is required")
	}

	existing, err := repo.FindByCode(ctx, code, cmd.ClientID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_code failed", err)
	}
	if existing != nil {
		return zero, usecase.Conflict(
			"CODE_EXISTS",
			"Subscription with code '"+code+"' already exists")
	}

	s := subscription.New(code, strings.TrimSpace(cmd.Name), cmd.Endpoint)
	s.Description = cmd.Description
	s.ClientID = cmd.ClientID
	s.ConnectionID = cmd.ConnectionID
	s.DispatchPoolID = cmd.DispatchPoolID
	s.ServiceAccountID = cmd.ServiceAccountID
	s.EventTypes = cmd.EventTypes
	if cmd.CustomConfig != nil {
		s.CustomConfig = cmd.CustomConfig
	}
	if cmd.Mode != "" {
		s.Mode = common.ParseDispatchMode(cmd.Mode)
	}
	if cmd.TimeoutSeconds != nil {
		s.TimeoutSeconds = *cmd.TimeoutSeconds
	}
	if cmd.MaxRetries != nil {
		s.MaxRetries = *cmd.MaxRetries
	}
	if cmd.DelaySeconds != nil {
		s.DelaySeconds = *cmd.DelaySeconds
	}
	if cmd.MaxAgeSeconds != nil {
		s.MaxAgeSeconds = *cmd.MaxAgeSeconds
	}
	if cmd.DataOnly != nil {
		s.DataOnly = *cmd.DataOnly
	}
	s.CreatedBy = &ec.PrincipalID

	event := SubscriptionCreated{
		Metadata:       usecase.NewEventMetadata(ec, SubscriptionCreatedType, Source, subjectFor(s.ID)),
		SubscriptionID: s.ID,
		Code:           s.Code,
		Name:           s.Name,
	}
	return commit.Save(ctx, uow, s, repo, event, cmd)
}
