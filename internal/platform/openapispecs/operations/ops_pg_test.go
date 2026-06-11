//go:build integration

package operations_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/application"
	appops "github.com/flowcatalyst/flowcatalyst-go/internal/platform/application/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs"
	"github.com/flowcatalyst/flowcatalyst-go/internal/platform/openapispecs/operations"
	"github.com/flowcatalyst/flowcatalyst-go/internal/testpg"
	"github.com/flowcatalyst/flowcatalyst-go/pkg/fcsdk/usecase"
)

func TestMain(m *testing.M) { testpg.RunMain(m) }

// mustCreateApp seeds a real application via application/operations —
// app_application_openapi_specs.application_id has a HARD FK to
// app_applications, so a fabricated id cannot satisfy the happy path.
func mustCreateApp(t *testing.T, code, name string) appops.ApplicationCreated {
	t.Helper()
	repo := application.NewRepository(testpg.Pool(t))
	committed, err := appops.CreateApplication(context.Background(), repo, testpg.NewUoW(t),
		appops.CreateCommand{Code: code, Name: name}, testpg.TestEC())
	require.NoError(t, err)
	return committed.Event()
}

func TestSyncOpenApiSpec_Validation(t *testing.T) {
	t.Parallel()
	repo := openapispecs.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)

	valid := json.RawMessage(`{"openapi":"3.0.0","info":{"version":"1.0.0"},"paths":{}}`)
	cases := []struct {
		name string
		cmd  operations.SyncOpenApiSpecCommand
		code string
	}{
		{"missing application id",
			operations.SyncOpenApiSpecCommand{ApplicationCode: "x", Spec: valid},
			"APPLICATION_ID_REQUIRED"},
		{"missing application code",
			operations.SyncOpenApiSpecCommand{ApplicationID: "app_x", Spec: valid},
			"APPLICATION_CODE_REQUIRED"},
		// Site 1: the document must be a JSON object at all.
		{"spec is a JSON array",
			operations.SyncOpenApiSpecCommand{ApplicationID: "app_x", ApplicationCode: "x",
				Spec: json.RawMessage(`["not","an","object"]`)},
			"INVALID_OPENAPI_SPEC"},
		{"spec is nil",
			operations.SyncOpenApiSpecCommand{ApplicationID: "app_x", ApplicationCode: "x"},
			"INVALID_OPENAPI_SPEC"},
		// Site 2: an object missing both `openapi` and `swagger`.
		{"spec missing openapi/swagger field",
			operations.SyncOpenApiSpecCommand{ApplicationID: "app_x", ApplicationCode: "x",
				Spec: json.RawMessage(`{"info":{"version":"1.0.0"}}`)},
			"INVALID_OPENAPI_SPEC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := operations.SyncOpenApiSpec(context.Background(), repo, uow, tc.cmd, testpg.TestEC())
			testpg.RequireUsecaseError(t, err, usecase.KindValidation, tc.code)
		})
	}
}

// TestSyncOpenApiSpec_Lifecycle drives the full state machine against one
// application: first sync persists a CURRENT row; a byte-identical re-sync
// no-ops (Unchanged=true, no new row); a changed document archives the
// prior CURRENT and inserts the new one; an info.version collision with a
// stored row gets the "+timestamp" disambiguation suffix.
func TestSyncOpenApiSpec_Lifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := openapispecs.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	ec := testpg.TestEC()
	app := mustCreateApp(t, "oassynchappy1", "OpenAPI Sync App")

	spec1 := json.RawMessage(`{"openapi":"3.0.0","info":{"title":"T","version":"1.0.0"},"paths":{"/a":{"get":{}}}}`)

	// 1. First sync → new CURRENT row.
	first, err := operations.SyncOpenApiSpec(ctx, repo, uow, operations.SyncOpenApiSpecCommand{
		ApplicationID:   app.ApplicationID,
		ApplicationCode: "oassynchappy1",
		Spec:            spec1,
	}, ec)
	require.NoError(t, err)

	ev1 := first.Event()
	assert.NotEmpty(t, ev1.SpecID)
	assert.Equal(t, app.ApplicationID, ev1.ApplicationID)
	assert.Equal(t, "oassynchappy1", ev1.ApplicationCode)
	assert.Equal(t, "1.0.0", ev1.Version, "version is read from info.version")
	assert.Equal(t, openapispecs.SpecHash(spec1), ev1.SpecHash)
	assert.False(t, ev1.Unchanged)
	assert.Nil(t, ev1.ArchivedPriorVersion)

	cur, err := repo.FindCurrentByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, cur, "spec row must persist under the application")
	assert.Equal(t, ev1.SpecID, cur.ID)
	assert.Equal(t, openapispecs.StatusCurrent, cur.Status)
	assert.Equal(t, "1.0.0", cur.Version)
	assert.Equal(t, ev1.SpecHash, cur.SpecHash)
	require.NotNil(t, cur.SyncedBy)
	assert.Equal(t, "prn_optestrunner1", *cur.SyncedBy)
	assert.JSONEq(t, string(spec1), string(cur.Spec))

	// 2. Byte-identical re-sync → Unchanged short-circuit, no new row.
	second, err := operations.SyncOpenApiSpec(ctx, repo, uow, operations.SyncOpenApiSpecCommand{
		ApplicationID:   app.ApplicationID,
		ApplicationCode: "oassynchappy1",
		Spec:            spec1,
	}, ec)
	require.NoError(t, err)
	assert.True(t, second.Event().Unchanged)
	assert.Equal(t, ev1.SpecID, second.Event().SpecID, "unchanged sync reports the existing row")

	all, err := repo.FindAllByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	assert.Len(t, all, 1, "unchanged sync must not insert a row")

	// 3. Changed document → prior CURRENT archived, new CURRENT inserted.
	spec2 := json.RawMessage(`{"openapi":"3.0.0","info":{"title":"T","version":"2.0.0"},"paths":{"/a":{"get":{}},"/b":{"post":{}}}}`)
	third, err := operations.SyncOpenApiSpec(ctx, repo, uow, operations.SyncOpenApiSpecCommand{
		ApplicationID:   app.ApplicationID,
		ApplicationCode: "oassynchappy1",
		Spec:            spec2,
	}, ec)
	require.NoError(t, err)

	ev3 := third.Event()
	assert.False(t, ev3.Unchanged)
	assert.Equal(t, "2.0.0", ev3.Version, "re-sync picks up the bumped info.version")
	assert.NotEqual(t, ev1.SpecID, ev3.SpecID)
	require.NotNil(t, ev3.ArchivedPriorVersion)
	assert.Equal(t, "1.0.0", *ev3.ArchivedPriorVersion)

	cur, err = repo.FindCurrentByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, ev3.SpecID, cur.ID)
	assert.Equal(t, "2.0.0", cur.Version)

	all, err = repo.FindAllByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	require.Len(t, all, 2)
	var archived *openapispecs.OpenApiSpec
	for i := range all {
		if all[i].ID == ev1.SpecID {
			archived = &all[i]
		}
	}
	require.NotNil(t, archived, "prior row must still exist")
	assert.Equal(t, openapispecs.StatusArchived, archived.Status, "prior CURRENT is flipped to ARCHIVED")
	assert.NotNil(t, archived.ChangeNotesText, "archived row carries the rendered diff summary")

	// 4. New content reusing an already-stored info.version: the UNIQUE
	//    (application_id, version) guard appends a "+timestamp" suffix.
	spec3 := json.RawMessage(`{"openapi":"3.0.0","info":{"title":"T renamed","version":"2.0.0"},"paths":{"/b":{"post":{}}}}`)
	fourth, err := operations.SyncOpenApiSpec(ctx, repo, uow, operations.SyncOpenApiSpecCommand{
		ApplicationID:   app.ApplicationID,
		ApplicationCode: "oassynchappy1",
		Spec:            spec3,
	}, ec)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(fourth.Event().Version, "2.0.0+"),
		"colliding info.version must be disambiguated with a +timestamp suffix, got %q",
		fourth.Event().Version)

	all, err = repo.FindAllByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

// The second validation site accepts `swagger` (2.0 documents) in place
// of `openapi`.
func TestSyncOpenApiSpec_SwaggerFieldAccepted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := openapispecs.NewRepository(testpg.Pool(t))
	uow := testpg.NewUoW(t)
	app := mustCreateApp(t, "oasswagger1", "Swagger App")

	committed, err := operations.SyncOpenApiSpec(ctx, repo, uow, operations.SyncOpenApiSpecCommand{
		ApplicationID:   app.ApplicationID,
		ApplicationCode: "oasswagger1",
		Spec:            json.RawMessage(`{"swagger":"2.0","info":{"version":"0.1.0"},"paths":{}}`),
	}, testpg.TestEC())
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", committed.Event().Version)

	cur, err := repo.FindCurrentByApplication(ctx, app.ApplicationID)
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, committed.Event().SpecID, cur.ID)
}
