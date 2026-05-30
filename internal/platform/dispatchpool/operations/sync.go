package operations

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// poolCodePattern matches the Rust pool_code_pattern: must start with a
// lowercase letter, then lowercase alphanumerics, hyphens, underscores.
var poolCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// SyncDispatchPoolInput is one pool definition in an SDK sync payload.
// RateLimit nil means concurrency-only (no rate limiter).
type SyncDispatchPoolInput struct {
	Code        string
	Name        string
	Description *string
	RateLimit   *int32
	Concurrency int32
}

// SyncDispatchPoolsCommand syncs dispatch pools from an application SDK.
// Pools are GLOBAL (matched by code, not application-scoped); ApplicationCode
// is carried for audit/event provenance. This endpoint is admin-tier only.
type SyncDispatchPoolsCommand struct {
	ApplicationCode string
	Pools           []SyncDispatchPoolInput
	RemoveUnlisted  bool
}

// SyncDispatchPools bulk-upserts dispatch pools within a single transaction.
// Mirrors the Rust SyncDispatchPoolsUseCase exactly:
//
//   - Validates each pool code against poolCodePattern; name required;
//     rateLimit (when set) ≥ 1; concurrency ≥ 1.
//   - Matches existing pools by code over ALL pools (pools are global).
//   - RemoveUnlisted ARCHIVES (soft, not hard-delete) pools absent from the
//     payload that aren't already archived.
//
// Emits per-row [DispatchPoolCreated]/[DispatchPoolUpdated]/[DispatchPoolArchived]
// events plus one [DispatchPoolsSynced] rollup, atomic via [commit.Sync].
func SyncDispatchPools(
	ctx context.Context,
	repo *dispatchpool.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncDispatchPoolsCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[DispatchPoolsSynced], error) {
	var zero commit.Committed[DispatchPoolsSynced]

	if strings.TrimSpace(cmd.ApplicationCode) == "" {
		return zero, usecase.Validation("APPLICATION_CODE_REQUIRED", "Application code is required")
	}
	for _, in := range cmd.Pools {
		if strings.TrimSpace(in.Code) == "" || !poolCodePattern.MatchString(in.Code) {
			return zero, usecase.Validation("INVALID_POOL_CODE", fmt.Sprintf(
				"Pool code '%s' is invalid. Must start with lowercase letter, contain only lowercase alphanumeric, hyphens, underscores.", in.Code))
		}
		if strings.TrimSpace(in.Name) == "" {
			return zero, usecase.Validation("NAME_REQUIRED", "Pool name is required")
		}
		if in.RateLimit != nil && *in.RateLimit < 1 {
			return zero, usecase.Validation("INVALID_RATE_LIMIT", "Rate limit, when set, must be at least 1")
		}
		if in.Concurrency < 1 {
			return zero, usecase.Validation("INVALID_CONCURRENCY", "Concurrency must be at least 1")
		}
	}

	existing, err := repo.FindWithFilters(ctx, nil, nil) // all pools (global)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_all failed", err)
	}
	existingByCode := make(map[string]*dispatchpool.DispatchPool, len(existing))
	for i := range existing {
		existingByCode[existing[i].Code] = &existing[i]
	}

	var (
		saves       []commit.SyncSave[dispatchpool.DispatchPool]
		syncedCodes = make([]string, 0, len(cmd.Pools))
		syncedSet   = make(map[string]struct{}, len(cmd.Pools))
		created     uint32
		updated     uint32
		deleted     uint32
	)

	for _, in := range cmd.Pools {
		syncedCodes = append(syncedCodes, in.Code)
		syncedSet[in.Code] = struct{}{}

		if cur, ok := existingByCode[in.Code]; ok {
			cur.Name = in.Name
			cur.Description = in.Description
			cur.RateLimit = in.RateLimit
			cur.Concurrency = in.Concurrency
			saves = append(saves, commit.SyncSave[dispatchpool.DispatchPool]{
				Aggregate: cur,
				Event: DispatchPoolUpdated{
					Metadata: usecase.NewEventMetadata(ec, DispatchPoolUpdatedType, Source, subjectFor(cur.ID)),
					PoolID:   cur.ID,
					Name:     cur.Name,
				},
			})
			updated++
			continue
		}

		p := dispatchpool.New(in.Code, in.Name)
		p.Description = in.Description
		p.RateLimit = in.RateLimit
		p.Concurrency = in.Concurrency
		saves = append(saves, commit.SyncSave[dispatchpool.DispatchPool]{
			Aggregate: p,
			Event: DispatchPoolCreated{
				Metadata: usecase.NewEventMetadata(ec, DispatchPoolCreatedType, Source, subjectFor(p.ID)),
				PoolID:   p.ID,
				Code:     p.Code,
				Name:     p.Name,
			},
		})
		created++
	}

	if cmd.RemoveUnlisted {
		for i := range existing {
			cur := &existing[i]
			if _, present := syncedSet[cur.Code]; present {
				continue
			}
			if cur.Status == dispatchpool.StatusArchived {
				continue
			}
			cur.Archive()
			saves = append(saves, commit.SyncSave[dispatchpool.DispatchPool]{
				Aggregate: cur,
				Event: DispatchPoolArchived{
					Metadata: usecase.NewEventMetadata(ec, DispatchPoolArchivedType, Source, subjectFor(cur.ID)),
					PoolID:   cur.ID,
					Code:     cur.Code,
				},
			})
			deleted++
		}
	}

	rollup := DispatchPoolsSynced{
		Metadata:        usecase.NewEventMetadata(ec, DispatchPoolsSyncedType, Source, "platform.dispatchpools."+cmd.ApplicationCode),
		ApplicationCode: cmd.ApplicationCode,
		Created:         created,
		Updated:         updated,
		Deleted:         deleted,
		SyncedCodes:     syncedCodes,
	}
	return commit.Sync(ctx, uow, repo, saves, nil, rollup, cmd)
}
