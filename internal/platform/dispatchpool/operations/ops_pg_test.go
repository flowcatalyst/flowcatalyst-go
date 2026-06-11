//go:build integration

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/dispatchpool/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecasepgx"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

func ptr[T any](v T) *T { return &v }

// mustCreate seeds a dispatch pool through the public operation — the same
// path production uses. Codes are hand-unique per test: the fixture never
// truncates between tests, so tests own their rows and never assert
// table-wide.
func mustCreate(t *testing.T, repo *dispatchpool.Repository, uow *usecasepgx.UnitOfWork, code, name string) operations.DispatchPoolCreated {
	t.Helper()
	committed, err := operations.CreateDispatchPool(context.Background(), repo, uow,
		operations.CreateCommand{Code: code, Name: name}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

// ── Create ────────────────────────────────────────────────────────────────

func TestCreateDispatchPool_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	// Defaults: nil concurrency → 10, nil rateLimit stays nil (no limiter).
	desc := "router pool"
	committed, err := operations.CreateDispatchPool(ctx, repo, uow, operations.CreateCommand{
		Code:        "  DPCreate-Happy  ", // op must trim + lowercase
		Name:        "DP Create Happy",
		Description: &desc,
	}, testpg.TestEC())
	require.NoError(t, err)

	ev := committed.Event()
	assert.NotEmpty(t, ev.PoolID)
	assert.Equal(t, "dpcreate-happy", ev.Code, "code must be trimmed + lowercased")
	assert.Equal(t, "DP Create Happy", ev.Name)

	got, err := repo.FindByID(ctx, ev.PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "dpcreate-happy", got.Code)
	assert.Equal(t, "DP Create Happy", got.Name)
	assert.Equal(t, dispatchpool.StatusActive, got.Status, "new pools start ACTIVE")
	assert.Equal(t, int32(10), got.Concurrency, "nil concurrency must default to 10")
	assert.Nil(t, got.RateLimit, "nil rateLimit must stay nil (concurrency-only pool)")
	assert.Nil(t, got.ClientID)
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)

	// Explicit values: rateLimit 0 is VALID at create (bound is ≥ 0 — sync's
	// is ≥ 1, pinned in TestSyncDispatchPools_Validation), concurrency 1 is
	// the lower bound.
	committed, err = operations.CreateDispatchPool(ctx, repo, uow, operations.CreateCommand{
		Code:        "dpcreate-explicit",
		Name:        "DP Create Explicit",
		RateLimit:   ptr(int32(0)),
		Concurrency: ptr(int32(1)),
	}, testpg.TestEC())
	require.NoError(t, err)

	got, err = repo.FindByID(ctx, committed.Event().PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.RateLimit)
	assert.Equal(t, int32(0), *got.RateLimit)
	assert.Equal(t, int32(1), got.Concurrency)
}

// Pins the underscore union fix: create deliberately validates against
// validate.CodeUnderscorePattern (owner-approved widening from hyphen-only),
// matching sync and the Rust pool_code_pattern.
func TestCreateDispatchPool_UnderscoreCode_Succeeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	committed, err := operations.CreateDispatchPool(ctx, repo, uow, operations.CreateCommand{
		Code: "dp_underscore_ok",
		Name: "Underscore Pool",
	}, testpg.TestEC())
	require.NoError(t, err, "underscores in pool codes must be accepted")
	assert.Equal(t, "dp_underscore_ok", committed.Event().Code)

	got, err := repo.FindByID(ctx, committed.Event().PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "dp_underscore_ok", got.Code)
}

// Note: an uppercase code like "Bad" can never hit INVALID_CODE_FORMAT on
// create — the op lowercases BEFORE validating (pinned by the happy path).
// Sync does NOT lowercase; see TestSyncDispatchPools_Validation.
func TestCreateDispatchPool_Validation(t *testing.T) {
	t.Parallel()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.CreateCommand
		code string
	}{
		{"empty code", operations.CreateCommand{Name: "X"}, "CODE_REQUIRED"},
		{"code starts with digit", operations.CreateCommand{Code: "1bad", Name: "X"}, "INVALID_CODE_FORMAT"},
		{"code with space", operations.CreateCommand{Code: "bad code", Name: "X"}, "INVALID_CODE_FORMAT"},
		{"empty name", operations.CreateCommand{Code: "dpcrt-noname"}, "NAME_REQUIRED"},
		{"zero concurrency", operations.CreateCommand{
			Code: "dpcrt-conc", Name: "X", Concurrency: ptr(int32(0)),
		}, "INVALID_CONCURRENCY"},
		{"negative rate limit", operations.CreateCommand{
			Code: "dpcrt-rate", Name: "X", RateLimit: ptr(int32(-1)),
		}, "INVALID_RATE_LIMIT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.CreateDispatchPool(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// Conflict is pinned by seeding through the operation itself: the first
// create IS the seed for the second (both anchor-scoped: nil ClientID).
func TestCreateDispatchPool_DuplicateCode_Conflict(t *testing.T) {
	t.Parallel()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	mustCreate(t, repo, uow, "dpdup-pool", "First")

	_, err := operations.CreateDispatchPool(context.Background(), repo, uow,
		operations.CreateCommand{Code: "dpdup-pool", Name: "Second"}, testpg.TestEC())
	testpg.RequireUsecaseError(t, err, usecase.KindConflict, "CODE_EXISTS")
}

// ── Update ────────────────────────────────────────────────────────────────

func TestUpdateDispatchPool_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "dpupd-happy", "Before")

	desc := "after"
	committed, err := operations.UpdateDispatchPool(ctx, repo, uow, operations.UpdateCommand{
		ID:          seeded.PoolID,
		Name:        ptr("  After  "), // op must trim
		Description: &desc,
		RateLimit:   ptr(int32(60)),
		Concurrency: ptr(int32(4)),
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.PoolID, committed.Event().PoolID)
	assert.Equal(t, "After", committed.Event().Name)

	got, err := repo.FindByID(ctx, seeded.PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "After", got.Name)
	require.NotNil(t, got.Description)
	assert.Equal(t, "after", *got.Description)
	require.NotNil(t, got.RateLimit)
	assert.Equal(t, int32(60), *got.RateLimit)
	assert.Equal(t, int32(4), got.Concurrency)
	assert.Equal(t, "dpupd-happy", got.Code, "code is immutable on update")
}

func TestUpdateDispatchPool_Errors(t *testing.T) {
	t.Parallel()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.UpdateCommand
		kind usecase.Kind
		code string
	}{
		{"missing id", operations.UpdateCommand{Name: ptr("X")}, usecase.KindValidation, "ID_REQUIRED"},
		{"blank name", operations.UpdateCommand{ID: "dpl_doesnotexist1", Name: ptr(" ")}, usecase.KindValidation, "NAME_REQUIRED"},
		{"zero concurrency", operations.UpdateCommand{
			ID: "dpl_doesnotexist1", Concurrency: ptr(int32(0)),
		}, usecase.KindValidation, "INVALID_CONCURRENCY"},
		{"negative rate limit", operations.UpdateCommand{
			ID: "dpl_doesnotexist1", RateLimit: ptr(int32(-1)),
		}, usecase.KindValidation, "INVALID_RATE_LIMIT"},
		{"unknown id", operations.UpdateCommand{ID: "dpl_doesnotexist1", Name: ptr("X")}, usecase.KindNotFound, "DispatchPool_NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.UpdateDispatchPool(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, tc.kind, tc.code)
		})
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestDeleteDispatchPool_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "dpdel-happy", "Doomed")

	committed, err := operations.DeleteDispatchPool(ctx, repo, uow,
		operations.DeleteCommand{ID: seeded.PoolID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.PoolID, committed.Event().PoolID)
	assert.Equal(t, "dpdel-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.PoolID)
	require.NoError(t, err)
	assert.Nil(t, got, "deleted row must be gone")
}

// ── Archive ───────────────────────────────────────────────────────────────

func TestArchiveDispatchPool_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "dparc-happy", "Archive Me")

	committed, err := operations.ArchiveDispatchPool(ctx, repo, uow,
		operations.ArchiveCommand{ID: seeded.PoolID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.PoolID, committed.Event().PoolID)
	assert.Equal(t, "dparc-happy", committed.Event().Code)

	got, err := repo.FindByID(ctx, seeded.PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, dispatchpool.StatusArchived, got.Status, "archive must flip ACTIVE → ARCHIVED")
}

// ── Suspend / Activate (status flips) ─────────────────────────────────────

func TestSuspendAndActivateDispatchPool_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	seeded := mustCreate(t, repo, uow, "dpsts-happy", "Flip Me")

	suspended, err := operations.SuspendDispatchPool(ctx, repo, uow,
		operations.SuspendCommand{ID: seeded.PoolID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.PoolID, suspended.Event().PoolID)
	assert.Equal(t, "dpsts-happy", suspended.Event().Code)

	got, err := repo.FindByID(ctx, seeded.PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, dispatchpool.StatusSuspended, got.Status, "suspend must flip ACTIVE → SUSPENDED")

	activated, err := operations.ActivateDispatchPool(ctx, repo, uow,
		operations.ActivateCommand{ID: seeded.PoolID}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, seeded.PoolID, activated.Event().PoolID)

	got, err = repo.FindByID(ctx, seeded.PoolID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, dispatchpool.StatusActive, got.Status, "activate must flip SUSPENDED → ACTIVE")
}

// All four ID-only ops share identical guard rails — fold them into one
// table (the connection pause/activate pattern, just denser).
func TestDispatchPoolIDOps_Errors(t *testing.T) {
	t.Parallel()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	ops := []struct {
		name string
		call func(id string) error
	}{
		{"delete", func(id string) error {
			_, err := operations.DeleteDispatchPool(context.Background(), repo, uow, operations.DeleteCommand{ID: id}, ec)
			return err
		}},
		{"archive", func(id string) error {
			_, err := operations.ArchiveDispatchPool(context.Background(), repo, uow, operations.ArchiveCommand{ID: id}, ec)
			return err
		}},
		{"suspend", func(id string) error {
			_, err := operations.SuspendDispatchPool(context.Background(), repo, uow, operations.SuspendCommand{ID: id}, ec)
			return err
		}},
		{"activate", func(id string) error {
			_, err := operations.ActivateDispatchPool(context.Background(), repo, uow, operations.ActivateCommand{ID: id}, ec)
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()
			testpg.RequireUsecaseError(t, op.call(""), usecase.KindValidation, "ID_REQUIRED")
			testpg.RequireUsecaseError(t, op.call("dpl_doesnotexist1"), usecase.KindNotFound, "DispatchPool_NOT_FOUND")
		})
	}
}

// ── Sync (GLOBAL matching; upsert; RemoveUnlisted archives) ───────────────

// Plain upsert (no RemoveUnlisted) is safe to run in parallel: it only
// touches pools whose codes it lists, and those codes are unique to this
// test.
func TestSyncDispatchPools_Upsert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	// rateLimit 1 pins sync's lower bound: ≥ 1 when set (create's is ≥ 0).
	first, err := operations.SyncDispatchPools(ctx, repo, uow, operations.SyncDispatchPoolsCommand{
		ApplicationCode: "dpsyncapp",
		Pools: []operations.SyncDispatchPoolInput{
			{Code: "dpsynup-one", Name: "A", Concurrency: 5},
			{Code: "dpsynup-two", Name: "B", Concurrency: 1, RateLimit: ptr(int32(1))},
		},
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), first.Event().Created)
	assert.Equal(t, uint32(0), first.Event().Updated)
	assert.Equal(t, uint32(0), first.Event().Deleted)
	assert.Equal(t, []string{"dpsynup-one", "dpsynup-two"}, first.Event().SyncedCodes)

	second, err := operations.SyncDispatchPools(ctx, repo, uow, operations.SyncDispatchPoolsCommand{
		ApplicationCode: "dpsyncapp",
		Pools: []operations.SyncDispatchPoolInput{
			{Code: "dpsynup-one", Name: "A renamed", Concurrency: 7, RateLimit: ptr(int32(60))},
		},
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), second.Event().Created)
	assert.Equal(t, uint32(1), second.Event().Updated)
	assert.Equal(t, uint32(0), second.Event().Deleted, "no RemoveUnlisted → nothing archived")

	one, err := repo.FindByCode(ctx, "dpsynup-one", nil)
	require.NoError(t, err)
	require.NotNil(t, one)
	assert.Equal(t, "A renamed", one.Name)
	assert.Equal(t, int32(7), one.Concurrency)
	require.NotNil(t, one.RateLimit)
	assert.Equal(t, int32(60), *one.RateLimit)

	two, err := repo.FindByCode(ctx, "dpsynup-two", nil)
	require.NoError(t, err)
	require.NotNil(t, two)
	assert.Equal(t, dispatchpool.StatusActive, two.Status, "unlisted pool untouched without RemoveUnlisted")
}

// HAZARD — deliberately NOT parallel: sync matches pools GLOBALLY (not
// app-scoped) and RemoveUnlisted archives EVERY non-listed, non-archived
// pool in the database. Running serially means it completes before any
// paused t.Parallel() bodies create their pools; it still only asserts on
// pools it created itself, never table-wide.
func TestSyncDispatchPools_RemoveUnlisted_Archives(t *testing.T) {
	ctx := context.Background()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()

	_, err := operations.SyncDispatchPools(ctx, repo, uow, operations.SyncDispatchPoolsCommand{
		ApplicationCode: "dpsyncrm",
		Pools: []operations.SyncDispatchPoolInput{
			{Code: "dpsyncrm-keep", Name: "Keep", Concurrency: 2},
			{Code: "dpsyncrm-drop", Name: "Drop", Concurrency: 2},
		},
	}, ec)
	require.NoError(t, err)

	second, err := operations.SyncDispatchPools(ctx, repo, uow, operations.SyncDispatchPoolsCommand{
		ApplicationCode: "dpsyncrm",
		Pools: []operations.SyncDispatchPoolInput{
			{Code: "dpsyncrm-keep", Name: "Keep renamed", Concurrency: 3},
		},
		RemoveUnlisted: true,
	}, ec)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), second.Event().Created)
	assert.Equal(t, uint32(1), second.Event().Updated)
	// Removal is global, so other tests' rows could inflate the count —
	// assert only the lower bound our own dropped pool guarantees.
	assert.GreaterOrEqual(t, second.Event().Deleted, uint32(1))

	kept, err := repo.FindByCode(ctx, "dpsyncrm-keep", nil)
	require.NoError(t, err)
	require.NotNil(t, kept)
	assert.Equal(t, "Keep renamed", kept.Name)
	assert.Equal(t, dispatchpool.StatusActive, kept.Status)

	dropped, err := repo.FindByCode(ctx, "dpsyncrm-drop", nil)
	require.NoError(t, err)
	require.NotNil(t, dropped, "RemoveUnlisted archives, never hard-deletes")
	assert.Equal(t, dispatchpool.StatusArchived, dropped.Status, "unlisted pool must be ARCHIVED")
}

func TestSyncDispatchPools_Validation(t *testing.T) {
	t.Parallel()
	repo := dispatchpool.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	cases := []struct {
		name string
		cmd  operations.SyncDispatchPoolsCommand
		code string
	}{
		{"missing application code", operations.SyncDispatchPoolsCommand{}, "APPLICATION_CODE_REQUIRED"},
		// Sync does NOT lowercase codes (deliberate Rust parity — create
		// DOES): the uppercase letters fail the pattern outright, proving
		// no normalization happens before validation.
		{"uppercase code rejected", operations.SyncDispatchPoolsCommand{
			ApplicationCode: "dpsyncval",
			Pools:           []operations.SyncDispatchPoolInput{{Code: "DPSync-Mixed", Name: "X", Concurrency: 1}},
		}, "INVALID_POOL_CODE"},
		{"code starts with digit", operations.SyncDispatchPoolsCommand{
			ApplicationCode: "dpsyncval",
			Pools:           []operations.SyncDispatchPoolInput{{Code: "1bad", Name: "X", Concurrency: 1}},
		}, "INVALID_POOL_CODE"},
		{"missing name", operations.SyncDispatchPoolsCommand{
			ApplicationCode: "dpsyncval",
			Pools:           []operations.SyncDispatchPoolInput{{Code: "dpsyncval-noname", Concurrency: 1}},
		}, "NAME_REQUIRED"},
		// Sync's rateLimit bound is ≥ 1 when set — rateLimit 0 is an error
		// here but VALID at create (pinned in TestCreateDispatchPool_HappyPath).
		{"zero rate limit", operations.SyncDispatchPoolsCommand{
			ApplicationCode: "dpsyncval",
			Pools: []operations.SyncDispatchPoolInput{
				{Code: "dpsyncval-rate", Name: "X", Concurrency: 1, RateLimit: ptr(int32(0))},
			},
		}, "INVALID_RATE_LIMIT"},
		{"zero concurrency", operations.SyncDispatchPoolsCommand{
			ApplicationCode: "dpsyncval",
			Pools:           []operations.SyncDispatchPoolInput{{Code: "dpsyncval-conc", Name: "X"}},
		}, "INVALID_CONCURRENCY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SyncDispatchPools(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}
