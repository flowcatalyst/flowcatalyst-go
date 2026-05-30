package operations

import (
	"context"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/principal"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/serviceaccount"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// SdkSyncSource is the AssignmentSource value tagging role assignments that
// came from an SDK principal sync. Distinguishes them from ADMIN_ASSIGNED /
// IDP_SYNC rows so a sync only ever replaces its own set. Mirrors the Rust
// "SDK_SYNC" source string.
const SdkSyncSource = "SDK_SYNC"

// SyncPrincipalInput is one principal definition in an SDK sync payload.
// Roles are role short-names (the SDK prefixes them with the applicationCode).
type SyncPrincipalInput struct {
	Email  string
	Name   string
	Roles  []string
	Active bool
}

// SyncPrincipalsCommand syncs user principals from an application SDK.
type SyncPrincipalsCommand struct {
	ApplicationCode string
	Principals      []SyncPrincipalInput
	RemoveUnlisted  bool
}

// SyncPrincipals upserts user principals from an application SDK within a
// single transaction. Mirrors the Rust SyncPrincipalsUseCase exactly:
//
//   - Matches by lowercased email. An existing principal keeps its non-SDK_SYNC
//     role assignments and has its SDK_SYNC assignments replaced with the
//     incoming set; name and active are updated. A new principal is created as
//     a CLIENT-scoped USER with the incoming SDK_SYNC roles.
//   - RemoveUnlisted does NOT delete principals: it strips SDK_SYNC role
//     assignments from any USER principal whose email is absent from the
//     payload (counted as "deactivated" in the rollup, matching Rust).
//
// Emits per-row [UserCreated]/[UserUpdated] events plus one [PrincipalsSynced]
// rollup, all atomic with the writes via [commit.Sync].
func SyncPrincipals(
	ctx context.Context,
	principals *principal.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncPrincipalsCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[PrincipalsSynced], error) {
	var zero commit.Committed[PrincipalsSynced]

	if strings.TrimSpace(cmd.ApplicationCode) == "" {
		return zero, usecase.Validation("APPLICATION_CODE_REQUIRED", "Application code is required")
	}
	if len(cmd.Principals) == 0 {
		return zero, usecase.Validation("PRINCIPALS_REQUIRED", "At least one principal must be provided")
	}

	now := time.Now().UTC()
	sdkSource := SdkSyncSource

	var (
		saves        []commit.SyncSave[principal.Principal]
		syncedEmails = make([]string, 0, len(cmd.Principals))
		syncedSet    = make(map[string]struct{}, len(cmd.Principals))
		created      uint32
		updated      uint32
		deactivated  uint32
	)

	for _, in := range cmd.Principals {
		email := strings.ToLower(in.Email)
		syncedEmails = append(syncedEmails, email)
		syncedSet[email] = struct{}{}

		roleAssignments := make([]serviceaccount.RoleAssignment, 0, len(in.Roles))
		for _, r := range in.Roles {
			roleAssignments = append(roleAssignments, serviceaccount.RoleAssignment{
				Role:             strings.ToLower(r),
				AssignmentSource: &sdkSource,
				AssignedAt:       now,
			})
		}

		existing, err := principals.FindByEmail(ctx, email)
		if err != nil {
			return zero, usecase.Internal("REPO", "find_by_email failed", err)
		}
		if existing != nil {
			// Merge: keep non-SDK_SYNC assignments, replace the SDK_SYNC set.
			merged := make([]serviceaccount.RoleAssignment, 0, len(existing.Roles)+len(roleAssignments))
			for _, ra := range existing.Roles {
				if ra.AssignmentSource != nil && *ra.AssignmentSource == SdkSyncSource {
					continue
				}
				merged = append(merged, ra)
			}
			merged = append(merged, roleAssignments...)
			existing.Roles = merged
			existing.Name = in.Name
			existing.Active = in.Active
			existing.UpdatedAt = now
			saves = append(saves, commit.SyncSave[principal.Principal]{
				Aggregate: existing,
				Event: UserUpdated{
					Metadata: usecase.NewEventMetadata(ec, UserUpdatedType, Source, subjectFor(existing.ID)),
					UserID:   existing.ID,
					Name:     existing.Name,
				},
			})
			updated++
			continue
		}

		p := principal.NewUser(email, principal.ScopeClient)
		p.Name = in.Name
		p.Active = in.Active
		p.Roles = roleAssignments
		saves = append(saves, commit.SyncSave[principal.Principal]{
			Aggregate: p,
			Event: UserCreated{
				Metadata: usecase.NewEventMetadata(ec, UserCreatedType, Source, subjectFor(p.ID)),
				UserID:   p.ID,
				Email:    email,
			},
		})
		created++
	}

	if cmd.RemoveUnlisted {
		all, err := principals.FindAll(ctx)
		if err != nil {
			return zero, usecase.Internal("REPO", "find_all failed", err)
		}
		for i := range all {
			pr := &all[i]
			if !pr.IsUser() || pr.UserIdentity == nil {
				continue
			}
			if _, present := syncedSet[strings.ToLower(pr.UserIdentity.Email)]; present {
				continue
			}
			hasSdkRoles := false
			for _, ra := range pr.Roles {
				if ra.AssignmentSource != nil && *ra.AssignmentSource == SdkSyncSource {
					hasSdkRoles = true
					break
				}
			}
			if !hasSdkRoles {
				continue
			}
			kept := make([]serviceaccount.RoleAssignment, 0, len(pr.Roles))
			for _, ra := range pr.Roles {
				if ra.AssignmentSource != nil && *ra.AssignmentSource == SdkSyncSource {
					continue
				}
				kept = append(kept, ra)
			}
			pr.Roles = kept
			pr.UpdatedAt = now
			saves = append(saves, commit.SyncSave[principal.Principal]{
				Aggregate: pr,
				Event: UserUpdated{
					Metadata: usecase.NewEventMetadata(ec, UserUpdatedType, Source, subjectFor(pr.ID)),
					UserID:   pr.ID,
					Name:     pr.Name,
				},
			})
			deactivated++
		}
	}

	rollup := PrincipalsSynced{
		Metadata:        usecase.NewEventMetadata(ec, PrincipalsSyncedType, Source, "platform.principals."+cmd.ApplicationCode),
		ApplicationCode: cmd.ApplicationCode,
		Created:         created,
		Updated:         updated,
		Deactivated:     deactivated,
		SyncedEmails:    syncedEmails,
	}
	return commit.Sync(ctx, uow, principals, saves, nil, rollup, cmd)
}
