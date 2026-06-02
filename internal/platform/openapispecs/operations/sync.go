package operations

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/commit"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

// SyncOpenApiSpecCommand is the input DTO. The version is read from
// `info.version` in the document; if absent or empty, falls back to
// the synced-at timestamp so each sync gets a unique key under the
// (application_id, version) UNIQUE constraint.
type SyncOpenApiSpecCommand struct {
	ApplicationID   string          `json:"applicationId"`
	ApplicationCode string          `json:"applicationCode"`
	Spec            json.RawMessage `json:"spec"`
}

// SyncOpenApiSpec compares the incoming spec against the application's
// current CURRENT row and either no-ops (byte-identical → emits the
// synced event with Unchanged=true) or flips the prior CURRENT to
// ARCHIVED and inserts a new CURRENT.
//
// Mirrors Rust's SyncOpenApiSpecUseCase. Writes go through the repo
// DIRECTLY — the UoW envelope is reserved for the tail event emission.
// Concurrent dual syncs are caught by the partial unique index on
// (application_id) WHERE status='CURRENT'; the loser sees a
// unique-violation error.
func SyncOpenApiSpec(
	ctx context.Context,
	repo *openapispecs.Repository,
	uow *usecasepgx.UnitOfWork,
	cmd SyncOpenApiSpecCommand,
	ec usecase.ExecutionContext,
) (commit.Committed[ApplicationOpenApiSpecSynced], error) {
	var zero commit.Committed[ApplicationOpenApiSpecSynced]

	if strings.TrimSpace(cmd.ApplicationID) == "" {
		return zero, usecase.Validation("APPLICATION_ID_REQUIRED", "applicationId is required")
	}
	if strings.TrimSpace(cmd.ApplicationCode) == "" {
		return zero, usecase.Validation("APPLICATION_CODE_REQUIRED", "applicationCode is required")
	}
	if !isJSONObject(cmd.Spec) {
		return zero, usecase.Validation("INVALID_OPENAPI_SPEC", "OpenAPI spec must be a JSON object")
	}
	if !hasOpenAPIField(cmd.Spec) {
		return zero, usecase.Validation("INVALID_OPENAPI_SPEC",
			"Spec is missing the top-level `openapi` (or `swagger`) field")
	}

	prior, err := repo.FindCurrentByApplication(ctx, cmd.ApplicationID)
	if err != nil {
		return zero, usecase.Internal("REPO", "find_current_by_application failed", err)
	}

	now := time.Now().UTC()
	newHash := openapispecs.SpecHash(cmd.Spec)

	// No-op short-circuit on byte-identical content.
	if prior != nil && prior.SpecHash == newHash {
		event := ApplicationOpenApiSpecSynced{
			Metadata:        usecase.NewEventMetadata(ec, SpecSyncedType, Source, subjectFor(prior.ID)),
			ApplicationID:   cmd.ApplicationID,
			ApplicationCode: cmd.ApplicationCode,
			SpecID:          prior.ID,
			Version:         prior.Version,
			SpecHash:        prior.SpecHash,
			HasBreaking:     false,
			Unchanged:       true,
		}
		return commit.Emit(ctx, uow, event, cmd)
	}

	var (
		notes           openapispecs.ChangeNotes
		summary         string
		archivedVersion *string
	)
	if prior != nil {
		notes, summary = openapispecs.ComputeChangeNotes(prior.Spec, cmd.Spec)
		v := prior.Version
		archivedVersion = &v
		if _, err := repo.ArchiveCurrent(ctx, cmd.ApplicationID, notes, summary); err != nil {
			return zero, usecase.Internal("REPO", "archive_current failed", err)
		}
	}

	versionCandidate := extractVersion(cmd.Spec, now)
	finalVersion := versionCandidate
	exists, err := repo.ExistsByApplicationAndVersion(ctx, cmd.ApplicationID, versionCandidate)
	if err != nil {
		return zero, usecase.Internal("REPO", "exists_by_application_and_version failed", err)
	}
	if exists {
		finalVersion = versionCandidate + "+" + now.Format("20060102150405")
	}

	newSpec := openapispecs.New(cmd.ApplicationID, finalVersion, cmd.Spec, newHash)
	newSpec.SyncedAt = now
	if ec.PrincipalID != "" {
		pid := ec.PrincipalID
		newSpec.SyncedBy = &pid
	}
	if err := repo.Insert(ctx, newSpec); err != nil {
		return zero, usecase.Internal("REPO", "insert failed", err)
	}

	event := ApplicationOpenApiSpecSynced{
		Metadata:             usecase.NewEventMetadata(ec, SpecSyncedType, Source, subjectFor(newSpec.ID)),
		ApplicationID:        cmd.ApplicationID,
		ApplicationCode:      cmd.ApplicationCode,
		SpecID:               newSpec.ID,
		Version:              newSpec.Version,
		SpecHash:             newSpec.SpecHash,
		ArchivedPriorVersion: archivedVersion,
		HasBreaking:          notes.HasBreaking,
		Unchanged:            false,
	}
	return commit.Emit(ctx, uow, event, cmd)
}

// extractVersion reads `info.version` from the spec; falls back to
// YYYYMMDDHHMMSS when the field is missing or empty.
func extractVersion(spec json.RawMessage, syncedAt time.Time) string {
	var doc map[string]any
	if err := json.Unmarshal(spec, &doc); err == nil {
		if info, ok := doc["info"].(map[string]any); ok {
			if v, ok := info["version"].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return syncedAt.Format("20060102150405")
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{")
}

func hasOpenAPIField(spec json.RawMessage) bool {
	var doc map[string]any
	if err := json.Unmarshal(spec, &doc); err != nil {
		return false
	}
	_, oas := doc["openapi"]
	_, swagger := doc["swagger"]
	return oas || swagger
}
