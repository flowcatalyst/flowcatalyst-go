//go:build integration

package operations_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/scheduledjob/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

func ptr[T any](v T) *T { return &v }

// validCrons is the canonical 6-field seconds-first shape the firing parser
// accepts; ValidateCronShape allows 5-7 fields.
var validCrons = []string{"0 0 * * * *"}

// mustCreate seeds a scheduled job through the public operation — the same
// path production uses. Codes are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide.
func mustCreate(t *testing.T, repo *scheduledjob.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.ScheduledJobCreated {
	t.Helper()
	committed, err := operations.CreateScheduledJob(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name, Crons: validCrons}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateScheduledJob_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	desc := "nightly refresh"
	committed, err := operations.CreateScheduledJob(ctx, repo, uow, operations.CreateCommand{
		Code:                "  SJCRT-Happy  ", // op must trim + lowercase
		Name:                "  SJ Create Happy  ",
		Crons:               []string{"0 0 3 * * *", "0 30 9 * * 1-5"},
		Timezone:            "Europe/Amsterdam",
		Description:         &desc,
		Payload:             json.RawMessage(`{"kind":"refresh"}`),
		Concurrent:          true,
		TracksCompletion:    true,
		TimeoutSeconds:      ptr(int32(120)),
		DeliveryMaxAttempts: ptr(int32(5)),
		TargetURL:           ptr("https://jobs.example.test/fire"),
	}, ec)
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.ScheduledJobID)
	assert.Equal(t, "sjcrt-happy", ev.Code, "code must be trimmed + lowercased")

	got, err := repo.FindByID(ctx, ev.ScheduledJobID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "sjcrt-happy", got.Code)
	assert.Equal(t, "SJ Create Happy", got.Name, "name must be trimmed")
	assert.Equal(t, scheduledjob.StatusActive, got.Status, "new jobs start ACTIVE")
	assert.Equal(t, []string{"0 0 3 * * *", "0 30 9 * * 1-5"}, got.Crons)
	assert.Equal(t, "Europe/Amsterdam", got.Timezone)
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)
	assert.JSONEq(t, `{"kind":"refresh"}`, string(got.Payload))
	assert.True(t, got.Concurrent)
	assert.True(t, got.TracksCompletion)
	require.NotNil(t, got.TimeoutSeconds)
	assert.Equal(t, int32(120), *got.TimeoutSeconds)
	assert.Equal(t, int32(5), got.DeliveryMaxAttempts)
	require.NotNil(t, got.TargetURL)
	assert.Equal(t, "https://jobs.example.test/fire", *got.TargetURL)
	require.NotNil(t, got.CreatedBy)
	assert.Equal(t, ec.PrincipalID, *got.CreatedBy)
	assert.Equal(t, int32(1), got.Version)
}

func TestCreateScheduledJob_Validation(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty code", operations.CreateCommand{Name: "X", Crons: validCrons}, "CODE_REQUIRED"},
		// PIN: scheduled-job codes keep the strict hyphen-only pattern —
		// "sj_underscore" FAILS here, in contrast with dispatchpool's
		// widened (underscore-tolerant) create.
		{"underscore code (strict hyphen-only)", operations.CreateCommand{
			Code: "sj_underscore", Name: "X", Crons: validCrons,
		}, "INVALID_CODE_FORMAT"},
		{"digit-leading code", operations.CreateCommand{
			Code: "1sjcrt-bad", Name: "X", Crons: validCrons,
		}, "INVALID_CODE_FORMAT"},
		{"empty name", operations.CreateCommand{Code: "sjcrt-noname", Crons: validCrons}, "NAME_REQUIRED"},
		{"no crons", operations.CreateCommand{Code: "sjcrt-nocron", Name: "X"}, "CRONS_REQUIRED"},
		{"blank cron", operations.CreateCommand{
			Code: "sjcrt-blankcron", Name: "X", Crons: []string{"   "},
		}, "INVALID_CRON"},
		{"three-field cron", operations.CreateCommand{
			Code: "sjcrt-shape3", Name: "X", Crons: []string{"* * *"},
		}, "CRON_INVALID_SHAPE"},
		{"eight-field cron", operations.CreateCommand{
			Code: "sjcrt-shape8", Name: "X", Crons: []string{"0 0 0 1 1 1 2026 extra"},
		}, "CRON_INVALID_SHAPE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateScheduledJob(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second (both anchor-scoped: nil ClientID).
func TestCreateScheduledJob_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "sjdup-code", "First")

	_, err := operations.CreateScheduledJob(context.Background(), repo, uow,
		operations.CreateCommand{Code: "sjdup-code", Name: "Second", Crons: validCrons}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateScheduledJob_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()
	seeded := mustCreate(t, repo, uow, "sjupd-happy", "Before")

	committed, err := operations.UpdateScheduledJob(ctx, repo, uow, operations.UpdateCommand{
		ID:                  seeded.ScheduledJobID,
		Name:                ptr("  After  "), // op must trim
		Description:         ptr("after"),
		Crons:               []string{"0 15 3 * * *"},
		Timezone:            ptr("Europe/Amsterdam"),
		Payload:             json.RawMessage(`{"after":true}`),
		Concurrent:          ptr(true),
		TracksCompletion:    ptr(true),
		TimeoutSeconds:      ptr(int32(45)),
		DeliveryMaxAttempts: ptr(int32(9)),
		TargetURL:           ptr("https://after.example.test/job"),
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, seeded.ScheduledJobID, committed.Event().ScheduledJobID)
	assert.Equal(t, "sjupd-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ScheduledJobID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "sjupd-happy", got.Code, "code is immutable on update")
	assert.Equal(t, "After", got.Name)
	require.NotNil(t, got.Description)
	assert.Equal(t, "after", *got.Description)
	assert.Equal(t, []string{"0 15 3 * * *"}, got.Crons)
	assert.Equal(t, "Europe/Amsterdam", got.Timezone)
	assert.JSONEq(t, `{"after":true}`, string(got.Payload))
	assert.True(t, got.Concurrent)
	assert.True(t, got.TracksCompletion)
	require.NotNil(t, got.TimeoutSeconds)
	assert.Equal(t, int32(45), *got.TimeoutSeconds)
	assert.Equal(t, int32(9), got.DeliveryMaxAttempts)
	require.NotNil(t, got.TargetURL)
	assert.Equal(t, "https://after.example.test/job", *got.TargetURL)
	require.NotNil(t, got.UpdatedBy)
	assert.Equal(t, ec.PrincipalID, *got.UpdatedBy)
	assert.Equal(t, int32(2), got.Version, "update must bump the version")
}

func TestUpdateScheduledJob_Errors(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	// Cron validation runs AFTER the load, so those cases need a real job.
	// All error paths return before any save — the seed stays untouched.
	seeded := mustCreate(t, repo, uow, "sjupd-errors", "Cron Error Target")

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: ptr("X")}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateCommand{ID: "sjb_doesnotexist1", Name: ptr(" ")}, usecase.KindValidation, "NAME_REQUIRED"},
		{"unknown id", operations.UpdateCommand{ID: "sjb_doesnotexist1", Name: ptr("X")}, usecase.KindNotFound, "ScheduledJob_NOT_FOUND"},
		{"empty crons", operations.UpdateCommand{ID: seeded.ScheduledJobID, Crons: []string{}}, usecase.KindValidation, "CRONS_REQUIRED"},
		{"blank cron", operations.UpdateCommand{ID: seeded.ScheduledJobID, Crons: []string{" "}}, usecase.KindValidation, "INVALID_CRON"},
		{"bad cron shape", operations.UpdateCommand{ID: seeded.ScheduledJobID, Crons: []string{"1 2 3"}}, usecase.KindValidation, "CRON_INVALID_SHAPE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateScheduledJob(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Pause / Resume (status round-trip) ────────────────────────────────────

func TestPauseResumeScheduledJob_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "sjpse-roundtrip", "Pause Me")

	paused, err := operations.PauseScheduledJob(ctx, repo, uow,
		operations.PauseCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ScheduledJobID, paused.Event().ScheduledJobID)
	assert.Equal(t, "sjpse-roundtrip", paused.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ScheduledJobID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, scheduledjob.StatusPaused, got.Status)

	resumed, err := operations.ResumeScheduledJob(ctx, repo, uow,
		operations.ResumeCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ScheduledJobID, resumed.Event().ScheduledJobID)

	got, err = repo.FindByID(ctx, seeded.ScheduledJobID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, scheduledjob.StatusActive, got.Status)
}

func TestPauseScheduledJob_Errors(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.PauseScheduledJob(context.Background(), repo, uow,
		operations.PauseCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.PauseScheduledJob(context.Background(), repo, uow,
		operations.PauseCommand{ID: "sjb_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "ScheduledJob_NOT_FOUND")
}

func TestResumeScheduledJob_Errors(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ResumeScheduledJob(context.Background(), repo, uow,
		operations.ResumeCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ResumeScheduledJob(context.Background(), repo, uow,
		operations.ResumeCommand{ID: "sjb_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "ScheduledJob_NOT_FOUND")
}

// ── Archive ───────────────────────────────────────────────────────────────

func TestArchiveScheduledJob_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "sjarc-happy", "Archive Me")

	committed, err := operations.ArchiveScheduledJob(ctx, repo, uow,
		operations.ArchiveCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ScheduledJobID, committed.Event().ScheduledJobID)
	assert.Equal(t, "sjarc-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ScheduledJobID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, scheduledjob.StatusArchived, got.Status)
}

func TestArchiveScheduledJob_Errors(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.ArchiveScheduledJob(context.Background(), repo, uow,
		operations.ArchiveCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.ArchiveScheduledJob(context.Background(), repo, uow,
		operations.ArchiveCommand{ID: "sjb_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "ScheduledJob_NOT_FOUND")
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteScheduledJob_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "sjdel-happy", "Doomed")

	committed, err := operations.DeleteScheduledJob(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.ScheduledJobID, committed.Event().ScheduledJobID)
	assert.Equal(t, "sjdel-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.ScheduledJobID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

func TestDeleteScheduledJob_Errors(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.DeleteScheduledJob(context.Background(), repo, uow,
		operations.DeleteCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.DeleteScheduledJob(context.Background(), repo, uow,
		operations.DeleteCommand{ID: "sjb_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "ScheduledJob_NOT_FOUND")
}

// ── FireNow (two-phase: instance row outside the UoW, then commit.Emit) ───

func TestFireNow_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	instances := scheduledjob.NewInstanceRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "sjfire-happy", "Fire Me")

	corr := "corr-sjfire-happy-1"
	committed, err := operations.FireNow(ctx, repo, instances, uow,
		operations.FireNowCommand{ID: seeded.ScheduledJobID, CorrelationID: &corr}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.Equal(t, seeded.ScheduledJobID, ev.ScheduledJobID)
	assert.Equal(t, "sjfire-happy", ev.Code)
	require.NotEmpty(t, ev.InstanceID, "committed event must carry the new instance id")

	inst, err := instances.FindByID(ctx, ev.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, inst, "instance row is inserted outside the UoW before the event commits")
	assert.Equal(t, seeded.ScheduledJobID, inst.ScheduledJobID)
	assert.Equal(t, "sjfire-happy", inst.JobCode)
	assert.Equal(t, scheduledjob.InstanceStatusQueued, inst.Status)
	assert.Equal(t, scheduledjob.TriggerManual, inst.TriggerKind)
	assert.Equal(t, int32(0), inst.DeliveryAttempts)
	require.NotNil(t, inst.CorrelationID)
	assert.Equal(t, corr, *inst.CorrelationID)
}

// PIN: a PAUSED job IS firable — manual fire is the human override (the
// poller skips PAUSED); only ARCHIVED is rejected.
func TestFireNow_PausedJobIsFirable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	instances := scheduledjob.NewInstanceRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "sjfire-paused", "Paused But Firable")

	_, err := operations.PauseScheduledJob(ctx, repo, uow,
		operations.PauseCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)

	committed, err := operations.FireNow(ctx, repo, instances, uow,
		operations.FireNowCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err, "firing a PAUSED job must succeed")

	inst, err := instances.FindByID(ctx, committed.Event().InstanceID)
	require.NoError(t, err)
	require.NotNil(t, inst)
	assert.Equal(t, scheduledjob.InstanceStatusQueued, inst.Status)
	assert.Equal(t, scheduledjob.TriggerManual, inst.TriggerKind)
}

func TestFireNow_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	instances := scheduledjob.NewInstanceRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	_, err := operations.FireNow(ctx, repo, instances, uow,
		operations.FireNowCommand{}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindValidation, "ID_REQUIRED")

	_, err = operations.FireNow(ctx, repo, instances, uow,
		operations.FireNowCommand{ID: "sjb_doesnotexist1"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindNotFound, "ScheduledJob_NOT_FOUND")

	// ARCHIVED jobs cannot be fired (conflict, not validation).
	seeded := mustCreate(t, repo, uow, "sjfire-archived", "Archived Target")
	_, err = operations.ArchiveScheduledJob(ctx, repo, uow,
		operations.ArchiveCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	require.NoError(t, err)

	_, err = operations.FireNow(ctx, repo, instances, uow,
		operations.FireNowCommand{ID: seeded.ScheduledJobID}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "ARCHIVED")
}

// ── Sync (ClientID-scoped; create / no-op / archive-unlisted / reactivate) ─

// Sync is scoped by ClientID and ArchiveUnlisted sweeps that whole scope, so
// every sync test owns a fresh unique ClientID — never the nil (platform)
// scope, which would sweep other tests' jobs.
func TestSyncScheduledJobs_CreateNoopArchiveAndReactivate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()
	clientID := "cli_sjsynchappy1"

	entry := func(code, name string) operations.ScheduledJobSyncEntry {
		return operations.ScheduledJobSyncEntry{
			Code: code, Name: name, Crons: validCrons,
			Timezone: "UTC", DeliveryMaxAttempts: 3,
		}
	}

	first, err := operations.SyncScheduledJobs(ctx, repo, uow, operations.SyncScheduledJobsCommand{
		ApplicationCode: "sjsyncapp1",
		ClientID:        &clientID,
		Jobs: []operations.ScheduledJobSyncEntry{
			entry("sjsync-a", "A"), entry("sjsync-b", "B"), entry("sjsync-c", "C"),
		},
	}, ec)
	require.NoError(t, err)
	assert.Len(t, first.Event().Created, 3)
	assert.Empty(t, first.Event().Updated)
	assert.Empty(t, first.Event().Archived)
	assert.Equal(t, "sjsyncapp1", first.Event().ApplicationCode)

	jobA, err := repo.FindByCode(ctx, "sjsync-a", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobA)
	assert.Equal(t, scheduledjob.StatusActive, jobA.Status)
	require.NotNil(t, jobA.ClientID)
	assert.Equal(t, clientID, *jobA.ClientID)
	assert.Equal(t, int32(1), jobA.Version)
	jobB, err := repo.FindByCode(ctx, "sjsync-b", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobB)
	jobC, err := repo.FindByCode(ctx, "sjsync-c", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobC)

	// PIN: an identical re-sync is a pure no-op — unchanged rows are
	// neither persisted nor counted.
	second, err := operations.SyncScheduledJobs(ctx, repo, uow, operations.SyncScheduledJobsCommand{
		ApplicationCode: "sjsyncapp1",
		ClientID:        &clientID,
		Jobs: []operations.ScheduledJobSyncEntry{
			entry("sjsync-a", "A"), entry("sjsync-b", "B"), entry("sjsync-c", "C"),
		},
	}, ec)
	require.NoError(t, err)
	assert.Empty(t, second.Event().Created)
	assert.Empty(t, second.Event().Updated, "no-op rows must not be counted")
	assert.Empty(t, second.Event().Archived)
	jobA2, err := repo.FindByCode(ctx, "sjsync-a", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobA2)
	assert.Equal(t, int32(1), jobA2.Version, "no-op rows must not be persisted (version unchanged)")

	// Pause C: ArchiveUnlisted only sweeps ACTIVE jobs.
	_, err = operations.PauseScheduledJob(ctx, repo, uow,
		operations.PauseCommand{ID: jobC.ID}, testpg.TestEC())
	require.NoError(t, err)

	// PIN: ArchiveUnlisted archives ACTIVE unlisted jobs in scope (B) but
	// leaves non-ACTIVE unlisted jobs alone (C stays PAUSED). The listed,
	// unchanged A is again neither persisted nor counted.
	third, err := operations.SyncScheduledJobs(ctx, repo, uow, operations.SyncScheduledJobsCommand{
		ApplicationCode: "sjsyncapp1",
		ClientID:        &clientID,
		Jobs:            []operations.ScheduledJobSyncEntry{entry("sjsync-a", "A")},
		ArchiveUnlisted: true,
	}, ec)
	require.NoError(t, err)
	assert.Empty(t, third.Event().Created)
	assert.Empty(t, third.Event().Updated)
	assert.Equal(t, []string{jobB.ID}, third.Event().Archived)

	jobB3, err := repo.FindByCode(ctx, "sjsync-b", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobB3)
	assert.Equal(t, scheduledjob.StatusArchived, jobB3.Status)
	jobC3, err := repo.FindByCode(ctx, "sjsync-c", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobC3)
	assert.Equal(t, scheduledjob.StatusPaused, jobC3.Status, "ArchiveUnlisted must only sweep ACTIVE jobs")

	// A reappearing archived job is re-activated and counted as updated.
	fourth, err := operations.SyncScheduledJobs(ctx, repo, uow, operations.SyncScheduledJobsCommand{
		ApplicationCode: "sjsyncapp1",
		ClientID:        &clientID,
		Jobs:            []operations.ScheduledJobSyncEntry{entry("sjsync-a", "A"), entry("sjsync-b", "B")},
	}, ec)
	require.NoError(t, err)
	assert.Empty(t, fourth.Event().Created)
	assert.Equal(t, []string{jobB.ID}, fourth.Event().Updated)
	assert.Empty(t, fourth.Event().Archived)

	jobB4, err := repo.FindByCode(ctx, "sjsync-b", &clientID)
	require.NoError(t, err)
	require.NotNil(t, jobB4)
	assert.Equal(t, scheduledjob.StatusActive, jobB4.Status, "a reappearing job must be re-activated")
}

func TestSyncScheduledJobs_Validation(t *testing.T) {
	t.Parallel()
	repo := scheduledjob.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	clientID := "cli_sjsyncval1"

	// Any entry missing code, name, or crons fails with the one shared code.
	cases := []struct {
		name string
		job  operations.ScheduledJobSyncEntry
	}{
		{"missing code", operations.ScheduledJobSyncEntry{Name: "X", Crons: validCrons}},
		{"missing name", operations.ScheduledJobSyncEntry{Code: "sjsync-noname", Crons: validCrons}},
		{"missing crons", operations.ScheduledJobSyncEntry{Code: "sjsync-nocron", Name: "X"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SyncScheduledJobs(context.Background(), repo, uow,
				operations.SyncScheduledJobsCommand{
					ApplicationCode: "sjsyncbad",
					ClientID:        &clientID,
					Jobs:            []operations.ScheduledJobSyncEntry{tc.job},
				}, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, "INVALID_SYNC_ENTRY")
		})
	}
}
