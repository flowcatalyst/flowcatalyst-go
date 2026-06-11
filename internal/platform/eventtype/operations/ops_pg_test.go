//go:build integration

package operations_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/eventtype/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreate seeds an event type through the public operation — the same
// path production uses. Codes are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide.
func mustCreate(t *testing.T, repo *eventtype.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.EventTypeCreated {
	t.Helper()
	committed, err := operations.CreateEventType(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateEventType_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	desc := "Order was created"
	committed, err := operations.CreateEventType(ctx, repo, uow, operations.CreateCommand{
		Code:        "etcreate:orders:order:created",
		Name:        "Order Created",
		Description: &desc,
		Schema:      json.RawMessage(`{"type":"object"}`),
	}, ec)
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.EventTypeID)
	assert.Equal(t, "etcreate:orders:order:created", ev.Code)
	assert.Equal(t, "Order Created", ev.Name)
	assert.Equal(t, "etcreate", ev.Application)
	assert.Equal(t, "orders", ev.Subdomain)
	assert.Equal(t, "order", ev.Aggregate)
	assert.Equal(t, "created", ev.EventName)
	require.NotNil(t, ev.Description)
	assert.Equal(t, desc, *ev.Description)

	got, err := repo.FindByID(ctx, ev.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, eventtype.StatusCurrent, got.Status)
	assert.Equal(t, eventtype.SourceUI, got.Source)
	// created_by persists since migration 035 (Rust never wrote it; its
	// rows read back NULL).
	require.NotNil(t, got.CreatedBy)
	assert.Equal(t, ec.PrincipalID, *got.CreatedBy)
	require.Len(t, got.SpecVersions, 1, "schema in create cmd must mint version 1.0")
	assert.Equal(t, "1.0", got.SpecVersions[0].Version)
	assert.Equal(t, eventtype.SpecFinalising, got.SpecVersions[0].Status)
}

func TestCreateEventType_Validation(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty code", operations.CreateCommand{Name: "X"}, "CODE_REQUIRED"},
		{"empty name", operations.CreateCommand{Code: "a:b:c:d"}, "NAME_REQUIRED"},
		{"three segments", operations.CreateCommand{Code: "a:b:c", Name: "X"}, "INVALID_CODE_FORMAT"},
		{"blank segment", operations.CreateCommand{Code: "a: :c:d", Name: "X"}, "INVALID_CODE_FORMAT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateEventType(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second.
func TestCreateEventType_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "etdup:orders:order:created", "First")

	_, err := operations.CreateEventType(context.Background(), repo, uow,
		operations.CreateCommand{Code: "etdup:orders:order:created", Name: "Second"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateEventType_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "etupd:orders:order:created", "Before")

	newDesc := "after"
	committed, err := operations.UpdateEventType(ctx, repo, uow, operations.UpdateCommand{
		ID: seeded.EventTypeID, Name: "After", Description: &newDesc,
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.EventTypeID, committed.Event().EventTypeID)
	assert.Equal(t, "After", committed.Event().Name)

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name)
	require.NotNil(t, got.Description)
	assert.Equal(t, "after", *got.Description)
	assert.Equal(t, "etupd:orders:order:created", got.Code, "code is immutable on update")
}

func TestUpdateEventType_Errors(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: "X"}, usecase.KindValidation, "ID_REQUIRED"},
		{"missing name", operations.UpdateCommand{ID: "evt_doesnotexist1", Name: " "}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "evt_doesnotexist1", Name: "X"}, usecase.KindNotFound, "EventType_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateEventType(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteEventType_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "etdel:orders:order:created", "Doomed")

	committed, err := operations.DeleteEventType(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.EventTypeID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.EventTypeID, committed.Event().EventTypeID)
	assert.Equal(t, "etdel:orders:order:created", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteEventType_Errors(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteEventType(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteEventType(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "evt_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "EventType_NOT_FOUND")
}

// ── Archive (state-transition conflict) ───────────────────────────────────

func TestArchiveEventType_HappyPathAndAlreadyArchived(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "etarc:orders:order:created", "Archive Me")

	_, err := operations.ArchiveEventType(ctx, repo, uow,
		operations.ArchiveCommand{ID: seeded.EventTypeID}, testpg.TestEC())
	require.NoError(t, err)

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, eventtype.StatusArchived, got.Status)

	_, err = operations.ArchiveEventType(ctx, repo, uow,
		operations.ArchiveCommand{ID: seeded.EventTypeID}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "ALREADY_ARCHIVED")
}

// ── Sync (app-scoped; created/updated/deleted; API-source-only removal) ───

func TestSyncEventTypes_UpsertAndRemoveUnlisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	// UI-sourced row in the same application scope: sync must NEVER touch it.
	uiRow := mustCreate(t, repo, uow, "etsync:ui:thing:kept", "UI Kept")

	first, err := operations.SyncEventTypes(ctx, repo, uow, operations.SyncEventTypesCommand{
		ApplicationCode: "etsync",
		EventTypes: []operations.SyncEventTypeInput{
			{Code: "etsync:orders:order:created", Name: "A"},
			{Code: "etsync:orders:order:updated", Name: "B"},
		},
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), first.Event().Created)
	assert.Equal(t, uint32(0), first.Event().Deleted)

	second, err := operations.SyncEventTypes(ctx, repo, uow, operations.SyncEventTypesCommand{
		ApplicationCode: "etsync",
		EventTypes: []operations.SyncEventTypeInput{
			{Code: "etsync:orders:order:created", Name: "A renamed"},
		},
		RemoveUnlisted: true,
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), second.Event().Created)
	assert.Equal(t, uint32(1), second.Event().Updated)
	assert.Equal(t, uint32(1), second.Event().Deleted)

	kept, err := repo.FindByCode(ctx, "etsync:orders:order:created")
	require.NoError(t, err)
	require.NotNil(t, kept)
	assert.Equal(t, "A renamed", kept.Name)
	assert.Equal(t, eventtype.SourceAPI, kept.Source)

	goneB, err := repo.FindByCode(ctx, "etsync:orders:order:updated")
	require.NoError(t, err)
	assert.Nil(t, goneB, "unlisted API row must be deleted")

	stillUI, err := repo.FindByID(ctx, uiRow.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, stillUI, "RemoveUnlisted must never touch UI-sourced rows")
}

func TestSyncEventTypes_Validation(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.SyncEventTypes(context.Background(), repo, uow,
		operations.SyncEventTypesCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "APPLICATION_CODE_REQUIRED")

	_, err = operations.SyncEventTypes(context.Background(), repo, uow, operations.SyncEventTypesCommand{
		ApplicationCode: "etsyncbad",
		EventTypes:      []operations.SyncEventTypeInput{{Code: "not-four-parts", Name: "X"}},
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "INVALID_CODE")
}

// ── Schema lifecycle (add / finalise / deprecate) ─────────────────────────

// mustCreateWithSchema seeds an event type whose create-time schema mints
// spec version 1.0 in FINALISING state (pinned by TestCreateEventType_HappyPath).
func mustCreateWithSchema(t *testing.T, repo *eventtype.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.EventTypeCreated {
	t.Helper()
	committed, err := operations.CreateEventType(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name, Schema: json.RawMessage(`{"type":"object"}`)},
		testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// specByVersion finds a spec version on a reloaded aggregate by version
// string — never by slice index, so load order can't flake the assert.
func specByVersion(t *testing.T, et *eventtype.EventType, version string) *eventtype.SpecVersion {
	t.Helper()
	for i := range et.SpecVersions {
		if et.SpecVersions[i].Version == version {
			return &et.SpecVersions[i]
		}
	}
	t.Fatalf("spec version %q not found (have %d versions)", version, len(et.SpecVersions))
	return nil
}

func TestAddSchema_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateWithSchema(t, repo, uow, "etaddsch:orders:order:created", "Add Schema")

	committed, err := operations.AddSchema(ctx, repo, uow, operations.AddSchemaCommand{
		EventTypeID: seeded.EventTypeID,
		Version:     "2.0",
		Schema:      json.RawMessage(`{"type":"object","title":"v2"}`),
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.EventTypeID, committed.Event().EventTypeID)
	assert.Equal(t, "2.0", committed.Event().Version)

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.SpecVersions, 2, "new version must be appended, not replace 1.0")
	added := specByVersion(t, got, "2.0")
	assert.Equal(t, eventtype.SpecFinalising, added.Status, "added versions start FINALISING")
	assert.JSONEq(t, `{"type":"object","title":"v2"}`, string(added.SchemaContent))
	assert.Equal(t, eventtype.SpecFinalising, specByVersion(t, got, "1.0").Status, "1.0 untouched")
}

func TestAddSchema_Errors(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	schema := json.RawMessage(`{"type":"object"}`)

	cases := []struct {
		name string
		cmd  operations.AddSchemaCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.AddSchemaCommand{Version: "2.0", Schema: schema}, usecase.KindValidation, "ID_REQUIRED"},
		{"missing version", operations.AddSchemaCommand{EventTypeID: "evt_doesnotexist1", Schema: schema}, usecase.KindValidation, "VERSION_REQUIRED"},
		{"missing schema", operations.AddSchemaCommand{EventTypeID: "evt_doesnotexist1", Version: "2.0"}, usecase.KindValidation, "SCHEMA_REQUIRED"},
		{"unknown id", operations.AddSchemaCommand{EventTypeID: "evt_doesnotexist1", Version: "2.0", Schema: schema}, usecase.KindNotFound, "EventType_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.AddSchema(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// The create-time schema already minted 1.0, so re-adding 1.0 conflicts.
func TestAddSchema_DuplicateVersion_Conflict(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateWithSchema(t, repo, uow, "etadddup:orders:order:created", "Dup Version")

	_, err := operations.AddSchema(context.Background(), repo, uow, operations.AddSchemaCommand{
		EventTypeID: seeded.EventTypeID,
		Version:     "1.0",
		Schema:      json.RawMessage(`{"type":"object"}`),
	}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "VERSION_EXISTS")
}

func TestFinaliseEventTypeSchema_HappyPathAndNotFinalising(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateWithSchema(t, repo, uow, "etfin:orders:order:created", "Finalise Me")

	committed, err := operations.FinaliseEventTypeSchema(ctx, repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.EventTypeID, committed.Event().EventTypeID)
	assert.Equal(t, "1.0", committed.Event().Version)
	assert.Nil(t, committed.Event().DeprecatedVersion, "no same-major CURRENT sibling to auto-deprecate")

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, eventtype.SpecCurrent, specByVersion(t, got, "1.0").Status,
		"finalise must flip FINALISING → CURRENT")

	// Finalising twice conflicts: the version is now CURRENT, not FINALISING.
	_, err = operations.FinaliseEventTypeSchema(ctx, repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "NOT_FINALISING")
}

func TestFinaliseEventTypeSchema_Errors(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.FinaliseEventTypeSchema(context.Background(), repo, uow,
		operations.FinaliseSchemaCommand{Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.FinaliseEventTypeSchema(context.Background(), repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: "evt_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "VERSION_REQUIRED")

	_, err = operations.FinaliseEventTypeSchema(context.Background(), repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: "evt_doesnotexist1", Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "EventType_NOT_FOUND")

	seeded := mustCreateWithSchema(t, repo, uow, "etfinerr:orders:order:created", "Finalise Errors")
	_, err = operations.FinaliseEventTypeSchema(context.Background(), repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "9.9"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "SpecVersion_NOT_FOUND")
}

// Full lifecycle pin: FINALISING refuses direct deprecation, CURRENT
// deprecates, DEPRECATED refuses a second deprecation.
func TestDeprecateEventTypeSchema_HappyPathAndConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreateWithSchema(t, repo, uow, "etdep:orders:order:created", "Deprecate Me")

	// Still FINALISING → direct deprecation is refused.
	_, err := operations.DeprecateEventTypeSchema(ctx, repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "STILL_FINALISING")

	_, err = operations.FinaliseEventTypeSchema(ctx, repo, uow,
		operations.FinaliseSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	require.NoError(t, err)

	committed, err := operations.DeprecateEventTypeSchema(ctx, repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.EventTypeID, committed.Event().EventTypeID)
	assert.Equal(t, "1.0", committed.Event().Version)

	got, err := repo.FindByID(ctx, seeded.EventTypeID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, eventtype.SpecDeprecated, specByVersion(t, got, "1.0").Status,
		"deprecate must flip CURRENT → DEPRECATED")

	_, err = operations.DeprecateEventTypeSchema(ctx, repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "ALREADY_DEPRECATED")
}

func TestDeprecateEventTypeSchema_Errors(t *testing.T) {
	t.Parallel()
	repo := eventtype.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeprecateEventTypeSchema(context.Background(), repo, uow,
		operations.DeprecateSchemaCommand{Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeprecateEventTypeSchema(context.Background(), repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: "evt_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "VERSION_REQUIRED")

	_, err = operations.DeprecateEventTypeSchema(context.Background(), repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: "evt_doesnotexist1", Version: "1.0"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "EventType_NOT_FOUND")

	seeded := mustCreateWithSchema(t, repo, uow, "etdeperr:orders:order:created", "Deprecate Errors")
	_, err = operations.DeprecateEventTypeSchema(context.Background(), repo, uow,
		operations.DeprecateSchemaCommand{EventTypeID: seeded.EventTypeID, Version: "9.9"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "SpecVersion_NOT_FOUND")
}
