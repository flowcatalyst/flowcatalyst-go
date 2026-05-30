package operations

import (
	"context"
	"strings"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/process"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// SyncProcessInput is one process definition in an SDK sync payload. Mirrors
// the Rust SyncProcessInput (camelCase wire shape lives in the sdksync API).
type SyncProcessInput struct {
	Code        string
	Name        string
	Description *string
	Body        string
	DiagramType *string
	Tags        []string
}

// SyncProcessesCommand syncs one application's API-sourced process diagrams.
type SyncProcessesCommand struct {
	ApplicationCode string
	Processes       []SyncProcessInput
	RemoveUnlisted  bool
}

// SyncProcesses bulk-upserts an application's process catalogue within a
// single transaction. Mirrors the Rust SyncProcessesUseCase exactly:
//
//   - Matches existing rows by full code (application:subdomain:process-name).
//   - Only API- and CODE-sourced rows are created/updated/removed; UI-authored
//     rows are never touched. New rows are created with source=API.
//   - diagramType is only overwritten when the incoming value is non-empty
//     (so an SDK that omits it keeps the existing/default "mermaid").
//   - RemoveUnlisted hard-deletes API/CODE-sourced rows absent from the payload.
//
// Emits per-row [ProcessCreated]/[ProcessUpdated]/[ProcessDeleted] events plus
// one [ProcessesSynced] rollup, all atomic with the writes via [commit.Sync].
func SyncProcesses(
	ctx context.Context,
	repo *process.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncProcessesCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ProcessesSynced], error) {
	var zero commit.Committed[ProcessesSynced]

	if strings.TrimSpace(cmd.ApplicationCode) == "" {
		return zero, usecase.Validation("APPLICATION_CODE_REQUIRED", "Application code is required")
	}

	appCode := cmd.ApplicationCode
	existing, err := repo.FindWithFilters(ctx, &appCode, nil, nil)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_by_application failed", err)
	}
	existingByCode := make(map[string]*process.Process, len(existing))
	for i := range existing {
		existingByCode[existing[i].Code] = &existing[i]
	}

	var (
		saves       []commit.SyncSave[process.Process]
		deletes     []commit.SyncDelete[process.Process]
		syncedCodes = make([]string, 0, len(cmd.Processes))
		syncedSet   = make(map[string]struct{}, len(cmd.Processes))
		created     uint32
		updated     uint32
		deleted     uint32
	)

	for _, in := range cmd.Processes {
		syncedCodes = append(syncedCodes, in.Code)
		syncedSet[in.Code] = struct{}{}

		if cur, ok := existingByCode[in.Code]; ok {
			if cur.Source != process.SourceAPI && cur.Source != process.SourceCode {
				continue // never touch UI-authored rows
			}
			cur.Name = in.Name
			cur.Description = in.Description
			cur.Body = in.Body
			if in.DiagramType != nil && strings.TrimSpace(*in.DiagramType) != "" {
				cur.DiagramType = *in.DiagramType
			}
			cur.Tags = in.Tags
			saves = append(saves, commit.SyncSave[process.Process]{
				Aggregate: cur,
				Event: ProcessUpdated{
					Metadata:  usecase.NewEventMetadata(ec, ProcessUpdatedType, Source, subjectFor(cur.ID)),
					ProcessID: cur.ID,
					Name:      cur.Name,
				},
			})
			updated++
			continue
		}

		p, err := process.New(in.Code, in.Name)
		if err != nil {
			return zero, usecase.Validation("INVALID_PROCESS_CODE", err.Error())
		}
		p.Source = process.SourceAPI
		p.Description = in.Description
		p.Body = in.Body
		if in.DiagramType != nil && strings.TrimSpace(*in.DiagramType) != "" {
			p.DiagramType = *in.DiagramType
		}
		if in.Tags != nil {
			p.Tags = in.Tags
		}
		saves = append(saves, commit.SyncSave[process.Process]{
			Aggregate: p,
			Event: ProcessCreated{
				Metadata:  usecase.NewEventMetadata(ec, ProcessCreatedType, Source, subjectFor(p.ID)),
				ProcessID: p.ID,
				Code:      p.Code,
				Name:      p.Name,
			},
		})
		created++
	}

	if cmd.RemoveUnlisted {
		for i := range existing {
			cur := &existing[i]
			if cur.Source != process.SourceAPI && cur.Source != process.SourceCode {
				continue
			}
			if _, present := syncedSet[cur.Code]; present {
				continue
			}
			deletes = append(deletes, commit.SyncDelete[process.Process]{
				Aggregate: cur,
				Event: ProcessDeleted{
					Metadata:  usecase.NewEventMetadata(ec, ProcessDeletedType, Source, subjectFor(cur.ID)),
					ProcessID: cur.ID,
					Code:      cur.Code,
				},
			})
			deleted++
		}
	}

	rollup := ProcessesSynced{
		Metadata:        usecase.NewEventMetadata(ec, ProcessesSyncedType, Source, "platform.processes."+cmd.ApplicationCode),
		ApplicationCode: cmd.ApplicationCode,
		Created:         created,
		Updated:         updated,
		Deleted:         deleted,
		SyncedCodes:     syncedCodes,
	}
	return commit.Sync(ctx, uow, repo, saves, deletes, rollup, cmd)
}
