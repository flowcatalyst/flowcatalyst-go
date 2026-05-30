package operations

import (
	"context"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/connection"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/subscription"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// SyncEventTypeBindingInput is one event-type binding in a synced subscription.
type SyncEventTypeBindingInput struct {
	EventTypeCode string
	Filter        *string
}

// SyncSubscriptionInput is one subscription definition in an SDK sync payload.
//
// Mode is accepted for wire compatibility but intentionally NOT applied:
// the Rust SyncSubscriptionsUseCase never sets the subscription's dispatch
// mode (synced subscriptions take the entity default), so applying it here
// would diverge the router's per-subscription dispatch behaviour. See the
// create/update branches below.
type SyncSubscriptionInput struct {
	Code             string
	Name             string
	Description      *string
	Target           string
	ConnectionID     *string
	EventTypes       []SyncEventTypeBindingInput
	DispatchPoolCode *string
	Mode             *string
	MaxRetries       *int32
	TimeoutSeconds   *int32
	DataOnly         bool
}

// SyncSubscriptionsCommand syncs one application's API-sourced subscriptions.
type SyncSubscriptionsCommand struct {
	ApplicationCode string
	Subscriptions   []SyncSubscriptionInput
	RemoveUnlisted  bool
}

// SyncSubscriptions bulk-upserts an application's subscription catalogue
// within a single transaction. Mirrors the Rust SyncSubscriptionsUseCase
// exactly:
//
//   - Validates app code; each subscription needs code, name, target, and at
//     least one event-type binding.
//   - When connectionId is provided it must resolve (404 CONNECTION_NOT_FOUND).
//   - Matches existing rows by code, scoped to the application. Only API- and
//     CODE-sourced rows are updated/removed; UI-authored rows are untouched.
//     New rows are created with source=API.
//   - dispatchPoolCode is resolved to (id, code) via the global pool lookup;
//     an unresolvable code is silently left unset (matches Rust).
//   - maxRetries / timeoutSeconds are only overwritten when present.
//   - RemoveUnlisted hard-deletes API/CODE rows absent from the payload.
//
// Emits per-row [SubscriptionCreated]/[SubscriptionUpdated]/[SubscriptionDeleted]
// events plus one [SubscriptionsSynced] rollup, atomic via [commit.Sync].
func SyncSubscriptions(
	ctx context.Context,
	subRepo *subscription.Repository,
	connRepo *connection.Repository,
	poolRepo *dispatchpool.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncSubscriptionsCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[SubscriptionsSynced], error) {
	var zero commit.Committed[SubscriptionsSynced]

	if strings.TrimSpace(cmd.ApplicationCode) == "" {
		return zero, usecase.Validation("APPLICATION_CODE_REQUIRED", "Application code is required")
	}
	for _, in := range cmd.Subscriptions {
		if strings.TrimSpace(in.Code) == "" {
			return zero, usecase.Validation("CODE_REQUIRED", "Subscription code is required")
		}
		if strings.TrimSpace(in.Name) == "" {
			return zero, usecase.Validation("NAME_REQUIRED", "Subscription name is required")
		}
		if strings.TrimSpace(in.Target) == "" {
			return zero, usecase.Validation("TARGET_REQUIRED", "Target endpoint URL is required")
		}
		if len(in.EventTypes) == 0 {
			return zero, usecase.Validation("EVENT_TYPES_REQUIRED", "At least one event type is required")
		}
	}

	// Validate connections exist (only when connectionId is provided).
	for _, in := range cmd.Subscriptions {
		if in.ConnectionID == nil {
			continue
		}
		c, err := connRepo.FindByID(ctx, *in.ConnectionID)
		if err != nil {
			return zero, usecase.Internal("REPO", "find_by_id(connection) failed", err)
		}
		if c == nil {
			return zero, usecase.NotFound("CONNECTION_NOT_FOUND", "Connection '"+*in.ConnectionID+"' not found")
		}
	}

	existing, err := subRepo.FindByApplicationCode(ctx, cmd.ApplicationCode)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_application_code failed", err)
	}
	existingByCode := make(map[string]*subscription.Subscription, len(existing))
	for i := range existing {
		existingByCode[existing[i].Code] = &existing[i]
	}

	var (
		saves       []commit.SyncSave[subscription.Subscription]
		deletes     []commit.SyncDelete[subscription.Subscription]
		syncedCodes = make([]string, 0, len(cmd.Subscriptions))
		syncedSet   = make(map[string]struct{}, len(cmd.Subscriptions))
		created     uint32
		updated     uint32
		deleted     uint32
	)

	for _, in := range cmd.Subscriptions {
		syncedCodes = append(syncedCodes, in.Code)
		syncedSet[in.Code] = struct{}{}

		bindings := make([]subscription.EventTypeBinding, 0, len(in.EventTypes))
		for _, et := range in.EventTypes {
			b := subscription.NewEventTypeBinding(et.EventTypeCode)
			b.Filter = et.Filter
			bindings = append(bindings, b)
		}

		if cur, ok := existingByCode[in.Code]; ok {
			if cur.Source != subscription.SourceAPI && cur.Source != subscription.SourceCode {
				continue // never touch UI-authored rows
			}
			cur.Name = in.Name
			cur.Description = in.Description
			cur.Endpoint = in.Target
			cur.ConnectionID = in.ConnectionID
			cur.EventTypes = bindings
			cur.DataOnly = in.DataOnly
			if in.MaxRetries != nil {
				cur.MaxRetries = *in.MaxRetries
			}
			if in.TimeoutSeconds != nil {
				cur.TimeoutSeconds = *in.TimeoutSeconds
			}
			resolveDispatchPool(ctx, poolRepo, in.DispatchPoolCode, &cur.DispatchPoolID, &cur.DispatchPoolCode)
			saves = append(saves, commit.SyncSave[subscription.Subscription]{
				Aggregate: cur,
				Event: SubscriptionUpdated{
					Metadata:       usecase.NewEventMetadata(ec, SubscriptionUpdatedType, Source, subjectFor(cur.ID)),
					SubscriptionID: cur.ID,
					Name:           cur.Name,
				},
			})
			updated++
			continue
		}

		sub := subscription.New(in.Code, in.Name, in.Target)
		sub.ConnectionID = in.ConnectionID
		appCode := cmd.ApplicationCode
		sub.ApplicationCode = &appCode
		sub.Source = subscription.SourceAPI
		sub.Description = in.Description
		sub.EventTypes = bindings
		sub.DataOnly = in.DataOnly
		pid := ec.PrincipalID
		sub.CreatedBy = &pid
		if in.MaxRetries != nil {
			sub.MaxRetries = *in.MaxRetries
		}
		if in.TimeoutSeconds != nil {
			sub.TimeoutSeconds = *in.TimeoutSeconds
		}
		resolveDispatchPool(ctx, poolRepo, in.DispatchPoolCode, &sub.DispatchPoolID, &sub.DispatchPoolCode)
		saves = append(saves, commit.SyncSave[subscription.Subscription]{
			Aggregate: sub,
			Event: SubscriptionCreated{
				Metadata:       usecase.NewEventMetadata(ec, SubscriptionCreatedType, Source, subjectFor(sub.ID)),
				SubscriptionID: sub.ID,
				Code:           sub.Code,
				Name:           sub.Name,
			},
		})
		created++
	}

	if cmd.RemoveUnlisted {
		for i := range existing {
			cur := &existing[i]
			if cur.Source != subscription.SourceAPI && cur.Source != subscription.SourceCode {
				continue
			}
			if _, present := syncedSet[cur.Code]; present {
				continue
			}
			deletes = append(deletes, commit.SyncDelete[subscription.Subscription]{
				Aggregate: cur,
				Event: SubscriptionDeleted{
					Metadata:       usecase.NewEventMetadata(ec, SubscriptionDeletedType, Source, subjectFor(cur.ID)),
					SubscriptionID: cur.ID,
					Code:           cur.Code,
				},
			})
			deleted++
		}
	}

	rollup := SubscriptionsSynced{
		Metadata:        usecase.NewEventMetadata(ec, SubscriptionsSyncedType, Source, "platform.subscriptions."+cmd.ApplicationCode),
		ApplicationCode: cmd.ApplicationCode,
		Created:         created,
		Updated:         updated,
		Deleted:         deleted,
		SyncedCodes:     syncedCodes,
	}
	return commit.Sync(ctx, uow, subRepo, saves, deletes, rollup, cmd)
}

// resolveDispatchPool resolves a pool code to (id, code) via the global pool
// lookup and writes them through the supplied pointers. An empty/nil code or
// an unresolvable code leaves the targets untouched (matches Rust, which
// silently ignores a missing pool).
func resolveDispatchPool(ctx context.Context, poolRepo *dispatchpool.Repository, code *string, idOut, codeOut **string) {
	if code == nil || strings.TrimSpace(*code) == "" {
		return
	}
	pool, err := poolRepo.FindByCode(ctx, *code, nil)
	if err != nil || pool == nil {
		return
	}
	id := pool.ID
	c := pool.Code
	*idOut = &id
	*codeOut = &c
}
