# Conventions

This file is the contract for adding code to `flowcatalyst-go`. It exists so
business developers can extend the platform without having to reverse-engineer
patterns from existing code, and so that existing code doesn't drift into 17
different ways of doing the same thing.

If you're about to write a new aggregate, a new use case, or a new HTTP
endpoint, read this file first. Everything here is enforced by code review,
not by the compiler — please don't invent a new shape because it feels nicer
in the moment.

The Rust platform (`../flowcatalyst-rust/`) is the canonical wire spec for HTTP
shapes, signing, and TSID format. When in doubt about a request/response field
name, the Rust side wins.

---

## 1. Stack — what's already chosen

These choices are made. Don't re-litigate them per-PR.

| Concern              | Choice                                                   | Rationale                                                                 |
|----------------------|----------------------------------------------------------|---------------------------------------------------------------------------|
| HTTP router          | `github.com/go-chi/chi/v5`                               | stdlib-shaped; one obvious way to wire routes.                            |
| Postgres driver      | `github.com/jackc/pgx/v5` + `pgxpool`                    | typed protocol, no `database/sql` overhead.                               |
| Query generation     | `sqlc` (config: `sqlc.yaml`)                             | generated typed queries in `internal/sqlc/dbq/`. Never write raw SQL in repos. |
| Migrations           | `pressly/goose/v3` against `internal/migrate/sql/NNN_*.sql` | each file starts with `-- +goose Up`. Forward-only. Embedded into binaries. |
| DI                   | plain constructor functions returning `*T`               | no wire, no fx, no DI container.                                          |
| Transactions / UoW   | `pkg/fcsdk/usecasepgx.UnitOfWork`                        | sealed-Result pattern — see §3.                                           |
| Errors (domain)      | `pkg/fcsdk/usecase.Error` with a `Kind`                  | typed; maps to HTTP status via `httperror.Write`.                         |
| Errors (HTTP)        | `internal/platform/shared/httperror`                     | constructors return `*usecase.Error` shaped for the wire envelope.        |
| Logging              | stdlib `log/slog`                                        | structured; no `logrus`/`zap`/`zerolog`.                                  |
| AuthN context        | `internal/platform/shared/auth`                          | `auth.FromContext(ctx)` in handlers.                                      |
| ID generation        | `pkg/fcsdk/tsid`                                         | Crockford Base32 TSID; same wire format across all four SDKs.             |
| Time                 | `time.Now().UTC()`                                       | UTC everywhere; never `time.Now()` without `.UTC()`.                      |
| Test framework       | stdlib `testing` + `github.com/stretchr/testify/{assert,require}` | `require` for fatal, `assert` for non-fatal.                              |

**Do not** introduce a competing library for any row above. If you think one is
needed, open an issue first.

---

## 2. Layout of an aggregate

Every aggregate (Application, EventType, Subscription, etc.) follows this exact
layout under `internal/platform/<aggregate>/`:

```
internal/platform/<aggregate>/
├── entity.go         ← the in-memory type + invariants (no I/O)
├── entity_test.go    ← unit tests of the entity
├── repository.go     ← Postgres-backed CRUD; implements usecase.Persist[T]
├── api/
│   └── api.go        ← HTTP layer: State, RegisterRoutes, handlers
└── operations/
    ├── events.go     ← domain event types (one file for all of them)
    ├── create.go     ← one file per operation
    ├── update.go
    ├── delete.go
    └── activate.go   ← etc.
```

**Hard rules:**
- **One operation per file.** `create.go` contains the create command DTO, use case
  type, constructor, `Validate`, `Authorize`, `Execute`. Don't bundle
  multiple operations.
- **One event types file.** All domain events for the aggregate live in
  `operations/events.go` so they're easy to scan together.
- **No business logic in `api/api.go`.** Handlers do: read auth → decode body
  → build execution context → call use case via `usecase.Run` → write
  response. That's it.
- **No SQL outside `repository.go`.** Repos use the sqlc-generated `dbq`
  package; never construct SQL strings in `operations/`.

The eventtype aggregate at `internal/platform/eventtype/` is the canonical
example. Copy its shape when adding a new aggregate.

---

## 3. The sealed Committed pattern — read this before writing a use case

Platform use cases must guarantee — at compile time, not at code review —
that every successful domain event is committed atomically with its
aggregate state. The seal is on `pkg/fcsdk/commit.Committed[E]`: its
`event` field is unexported, so external code cannot populate it. The
only way to produce a `Committed[E]` carrying a real event is to call
one of `commit.Save` / `commit.Delete` / `commit.SaveAll` / `commit.Emit`,
each of which writes the event row in the same transaction as the
aggregate row.

You will write code that returns `(commit.Committed[SomethingEvent], error)`.
You will be tempted to construct a `Committed` literal. Don't — even if
you do, the `event` field is unreachable from outside the `commit`
package; the zero `Committed[E]` carries the zero E (no event ID, no
type), so the database invariant (aggregate-write ⇒ event-row) holds
vacuously.

### The contract

A use case is a plain function:

```go
func VerbAggregate(
    ctx context.Context,
    repo *aggregate.Repository,
    uow *usecasepgx.UnitOfWork,
    cmd VerbCommand,
    ec usecase.ExecutionContext,
) (commit.Committed[VerbEvent], error)
```

Inside, you do five things — but they're inline early returns, not a
typed pipeline:

1. **Validate** the shape (`return zero, usecase.Validation(...)`).
2. **Authorize** if there's resource-level access logic (the handler
   typically does coarse-grained auth already).
3. **Find** the entity / check business invariants.
4. **Mutate** the entity and construct the domain event.
5. **Commit** via `return commit.Save(ctx, uow, entity, repo, event, cmd)`
   (or `commit.Delete` / `commit.SaveAll` / `commit.Emit`).

### Worked example — see `internal/platform/eventtype/operations/create.go`

```go
type CreateCommand struct {
    Code, Name string
    ClientID   *string
    Schema     json.RawMessage
}

func CreateEventType(
    ctx context.Context,
    repo *eventtype.Repository,
    uow *usecasepgx.UnitOfWork,
    cmd CreateCommand,
    ec usecase.ExecutionContext,
) (commit.Committed[EventTypeCreated], error) {
    var zero commit.Committed[EventTypeCreated]

    if strings.TrimSpace(cmd.Code) == "" {
        return zero, usecase.Validation("CODE_REQUIRED", "Event type code is required")
    }
    // ... more validation ...

    existing, err := repo.FindByCode(ctx, cmd.Code)
    if err != nil {
        return zero, usecase.Internal("REPO", "find_by_code failed", err)
    }
    if existing != nil {
        return zero, usecase.Conflict("CODE_EXISTS", "...")
    }

    et, _ := eventtype.New(cmd.Code, cmd.Name)
    event := EventTypeCreated{
        Metadata:    usecase.NewEventMetadata(ec, EventTypeCreatedType, /* ... */),
        EventTypeID: et.ID,
        // ... event payload ...
    }

    // commit.Save writes entity + event in one transaction. The seal is
    // here: the only way to produce a non-zero Committed[E] is this call.
    return commit.Save(ctx, uow, et, repo, event, cmd)
}
```

### When to use which commit function

| Function          | Use when                                                            |
|-------------------|---------------------------------------------------------------------|
| `commit.Save`     | One aggregate, one event. The 90% case.                             |
| `commit.Delete`   | Deleting an aggregate. Repo deletes; event records the deletion.    |
| `commit.SaveAll`  | Multi-aggregate transaction (rare; coordinate with a reviewer).     |
| `commit.Emit`     | Event with no aggregate state change (e.g. login, fire-now).        |

### Why the seal is at the right level

- External code can syntactically construct `commit.Committed[E]{}` (zero
  value). That returns the zero E from `Event()` — empty fields, no DB
  rows written. The aggregate-vs-event invariant holds vacuously.
- External code **cannot** populate the unexported `event` field. So no
  caller can fabricate a "real" Committed without going through the
  commit functions, which write the event atomically.
- The `sealed.Token` field on `Committed[E]` is documentary; it signals
  membership in the sealed UoW family but isn't load-bearing — the
  unexported `event` field is.

### Consumer-app SDK (separate from this)

`pkg/fcsdk/usecase.Result[E]` + `UseCase[Cmd,Evt]` + `usecase.Run` exist
for the **consumer-app Go SDK** (apps that publish events into the
platform via the SDK). Platform code does NOT use them — those are only
for external consumer apps. If you're touching `internal/` or `cmd/`,
the new commit pattern is the only valid one.

---

## 4. The HTTP handler pattern

Every handler is the same five steps. Copy this shape.

```go
func (s *State) create(w http.ResponseWriter, r *http.Request) {
    // 1. Authn context.
    ac := auth.FromContext(r.Context())
    if err := auth.CanWriteApplications(ac); err != nil {
        httperror.Write(w, err)
        return
    }

    // 2. Decode the command DTO.
    var body operations.CreateCommand
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        httperror.Write(w, httperror.BadRequest("INVALID_JSON", err.Error()))
        return
    }

    // 3. Build the execution context.
    ec := usecase.NewExecutionContext(ac.PrincipalID)

    // 4. Call the use case function directly. Returns (Committed[E], error).
    committed, err := operations.CreateApplication(r.Context(), s.Repo, s.UoW, body, ec)
    if err != nil {
        httperror.Write(w, err)
        return
    }

    // 5. Write the response. Use apicommon constructors for the standard
    //    envelopes so frontend can rely on the shape.
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    _ = json.NewEncoder(w).Encode(apicommon.CreatedResponse{ID: committed.Event().ApplicationID})
}
```

The `State` struct holds only `{Repo, UoW *usecasepgx.UnitOfWork}` (plus
any extra repos a particular aggregate needs for cross-aggregate
validation). No per-operation pointers.

**Do not** put business logic in the handler. If you're tempted, that's a
use case.

**Do not** call `httperror.Write` with a custom error. Use the constructors
in `internal/platform/shared/httperror/` (`NotFound`, `BadRequest`,
`Forbidden`, etc.). They produce the wire-format envelope the frontend
expects.

---

## 5. Repositories

Repos live in the aggregate's package root (e.g. `eventtype/repository.go`)
and follow a fixed shape:

```go
type Repository struct {
    pool *pgxpool.Pool
    q    *dbq.Queries  // sqlc-generated
}

func NewRepository(pool *pgxpool.Pool) *Repository { ... }

// CRUD. Use sqlc-generated queries — no SQL strings in this file.
func (r *Repository) FindByID(ctx context.Context, id string) (*T, error) { ... }
func (r *Repository) FindByCode(ctx context.Context, code string) (*T, error) { ... }

// Persist implements usecase.Persist[T]. UnitOfWork calls this inside
// the committing transaction. The DbTx wraps a pgx.Tx the SDK owns.
func (r *Repository) Persist(ctx context.Context, e *T, tx *usecasepgx.DbTx) error { ... }

// Delete (only if the aggregate supports it).
func (r *Repository) Delete(ctx context.Context, id string, tx *usecasepgx.DbTx) error { ... }
```

**Rules:**
- `Find*` returns `(nil, nil)` on "not found" (not an error). The use case
  decides whether absence is an error.
- `Persist` is the only method called inside a transaction; everything else
  takes its own connection from the pool.
- Sqlc-generated code goes in `internal/sqlc/dbq/`. Re-run `sqlc generate`
  after editing `internal/sqlc/queries/*.sql`.

---

## 5a. OpenAPI spec — committed lockfile + CI gate

The platform's OpenAPI spec is derived from huma's per-route registrations
in `internal/platform/<aggregate>/api/`. The committed contract is
`api/openapi.lock.json` — the parity-spec CI job fails on any drift
between the live spec and this file.

**When you intentionally change the wire shape** (add/remove a field,
change a status code, rename an operation):

1. Run `make api-bump` to regenerate `api/openapi.lock.json`.
2. Commit the diff in the same PR as the code change.
3. The PR review should see the spec diff alongside the code diff —
   that's the human gate on wire-format changes.

**When you do NOT intend to change the wire shape:**

The `TestOpenAPISpecLocked` snapshot test and the `make api-diff` CI step
both fail loudly if the live spec drifts from the lockfile. If you see
that failure unexpectedly, you've probably renamed a Go field or
changed a JSON tag — fix the code, not the lockfile.

**Adding a new aggregate to the spec:** register it in
`tools/dump-spec/main.go` alongside the existing aggregates. The
dump-spec binary builds the spec without touching the database, so it
runs in CI without infra.

---

## 6. Migrations

- One numbered file per migration: `internal/migrate/sql/NNN_subject.sql` where
  `NNN` is a three-digit zero-padded sequence (`001_tenant_tables.sql`). This
  directory is the single canonical source — the embed.FS in the migrate
  package and sqlc both read from here.
- Every file starts with a `-- +goose Up` line. We are forward-only; no
  `-- +goose Down` sections.
- Migrations are append-only. **Never** edit a migration that has shipped.
  If you need to change a schema, write a new migration.
- Use `IF NOT EXISTS` / `IF EXISTS` so re-running is idempotent. Goose
  tracks applied versions in `goose_db_version`, but idempotency is still
  defensive against partial-apply scenarios and dev-DB rewinds.
- Don't mix DDL and DML in the same file unless the DML is bootstrap data
  with stable IDs.
- Don't reference tables introduced in later migrations.

If you need to rename a column or drop a constraint, write two migrations: one
to add the new shape, one (later, in a follow-up release) to drop the old
shape. Never break running services with a single migration.

The runner (`internal/migrate.Run`) handles the legacy `_fc_migrations`
tracker transparently: existing databases are upgraded to `goose_db_version`
on first boot without re-applying anything.

---

## 7. Errors

There's exactly one error type for domain failures: `*usecase.Error`. The
`Kind` field controls HTTP status mapping.

Build them with the helpers — never construct the struct manually:

```go
usecase.Validation(code, msg)      // → 400
usecase.Authorization(code, msg)   // → 403
usecase.NotFound(code, msg)        // → 404
usecase.Conflict(code, msg)        // → 409
usecase.BusinessRule(code, msg)    // → 422
usecase.Internal(code, msg, err)   // → 500; preserves wrapped err for logs

httperror.NotFound("Application", id)  // sugar for the common 404 case
httperror.BadRequest("INVALID_JSON", err.Error())
```

The `code` is a short SCREAMING_SNAKE string the frontend can switch on. The
`msg` is human-readable. Don't put dynamic data in the code; put it in the
message.

---

## 8. Logging

stdlib `log/slog` only. Structured key-value pairs, not formatted strings.

```go
slog.Info("event delivered", "msg_id", msg.ID, "target", msg.MediationTarget)
slog.Warn("ack failed", "msg_id", msg.ID, "err", err)
slog.Error("repo find_by_id failed", "id", id, "err", err)
```

Don't log inside use cases — logging is the handler's job for request errors,
and `slog.Error` at infra failure sites. Use cases return typed errors; the
handler decides whether to log.

---

## 9. Channels & goroutines

The trickiest comprehension barrier in Go. Conventions to keep it sane:

1. **Document channel ownership at the declaration site.** One line:
   `// Closed by X; receivers must not close.` See
   `internal/router/pool.go` for examples.
2. **Comment wakeup conditions on every select.** Each case gets a phrase
   like `// shutdown` or `// rate-limit budget restored`. See
   `internal/router/lifecycle.go`.
3. **Pick from this small library of patterns; don't invent new ones:**
   - Fan-out worker pool with ctx-cancel
   - `time.Ticker`-driven background loop
   - Buffered channel as a semaphore (`sem chan struct{}`)
   - `sync.WaitGroup` + `done` channel for "wait with timeout"
4. **`go.uber.org/goleak`** is allowed (and encouraged) in `TestMain` for
   tests that exercise concurrent code. Goroutine leaks become CI failures
   instead of paged-at-3am surprises.
5. **Every goroutine must have an exit path tied to `ctx.Done()`** or a
   close on its input channel. No fire-and-forget goroutines, ever.

---

## 10. Testing

- File next to the file under test: `repository.go` → `repository_test.go`.
- Package: same package (whitebox) when testing private helpers, `_test`
  package (blackbox) when verifying the public API.
- `testify/require` for setup assertions (test must abort if these fail).
  `testify/assert` for behaviour assertions.
- DB tests use the throwaway Postgres in `docker-compose.dev.yml` —
  never mock pgx. Mock-pgx tests miss real-driver behaviour (e.g. tx scoping,
  `RETURNING` handling). The pain of a real DB in CI is worth it.
- Each test is self-contained: setup, action, assertions, teardown. No
  shared fixtures across tests.

---

## 11. Adding a new aggregate — checklist

Copy from `internal/platform/eventtype/` as the template.

- [ ] Migration: `migrations/NNN_<aggregate>_tables.sql`
- [ ] Sqlc queries: `internal/sqlc/queries/<aggregate>.sql`
- [ ] Run `sqlc generate` to refresh `internal/sqlc/dbq/`
- [ ] `internal/platform/<aggregate>/entity.go` — type + invariants
- [ ] `internal/platform/<aggregate>/entity_test.go`
- [ ] `internal/platform/<aggregate>/repository.go` — `FindBy*`, `Persist`,
      `Delete`
- [ ] `internal/platform/<aggregate>/operations/events.go` — event types
- [ ] One `operations/<op>.go` per command
- [ ] `internal/platform/<aggregate>/api/api.go` — `State`, `RegisterRoutes`,
      handlers
- [ ] Auth helpers in `internal/platform/shared/auth/` if new permission
      types are needed
- [ ] Wire into platform server entrypoint (currently
      `cmd/fc-platform-server/main.go` — see existing aggregate wiring once
      it lands)
- [ ] Tests pass: `go test ./internal/platform/<aggregate>/...`
- [ ] `go vet ./...` clean
- [ ] If this aggregate is exposed to consumer apps, add a typed accessor
      to `pkg/fcsdk/client/` and add a sync category to
      `pkg/fcsdk/sync/` if it should be declaratively reconcilable.

---

## 12. What NOT to do

These come up often enough to call out explicitly:

- **Don't import `internal/sealed` from outside the SDK.** It's the seal
  on `usecase.Result`; if you can reach it, the seal is broken.
- **Don't construct domain events in handlers.** Events are an `Execute`
  side effect, committed atomically via `Commit*`. Handlers see them only
  as the success value of `usecase.Into`.
- **Don't use `database/sql`** anywhere on the platform side. pgx only.
  (`pkg/fcsdk/usecasesql` exists for SDK consumers who use `database/sql`
  for their own reasons; the platform itself uses pgx.)
- **Don't add OpenAPI annotations to handlers.** The OpenAPI spec lives at
  `openapi.yaml` and is the source of truth; handlers must conform to it,
  not the other way around.
- **Don't introduce `panic` as flow control.** Even in dev code. `panic`
  is for invariants the type system can't express, used at most once or
  twice in this codebase.
- **Don't rename DTO fields without coordinating across all four SDKs**
  (Rust, TS, Laravel, Go). Wire compatibility matters.
- **Don't skip pre-commit hooks** (`--no-verify`). If a hook fails, fix
  the cause.

---

## 13. When to ask vs. when to just do it

**Just do it:**
- Add a new use case to an existing aggregate following the create.go shape.
- Add a new field to a DTO that the Rust spec already has.
- Refactor within a single file.
- Add tests.

**Ask first (open an issue or grab a reviewer in chat):**
- Introduce a new aggregate.
- Add a new top-level package outside `internal/platform/<aggregate>/`.
- Change the wire format of any DTO.
- Touch `internal/sealed`, `pkg/fcsdk/usecase`, or the `Commit*` functions.
- Add a new third-party dependency.
- Change a migration that's already been merged.

---

If something here feels wrong, raise it — but raise it as "I want to change the
convention" rather than working around it in a PR. The whole point of this file
is that the codebase stays coherent across the dozens of aggregates that will
eventually live in it.
