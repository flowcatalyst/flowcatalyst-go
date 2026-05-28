# FlowCatalyst Go Port — Handoff

This document is the canonical "where we are, where we're going" reference
for the Rust → Go port. Read it cold to pick up the work.

---

## ✅ DONE: OIDC fosite-replacement — STATUS (2026-05-28)

Replaced `ory/fosite` with a 1:1 hand-rolled port of the Rust OIDC server
(`crates/fc-platform/src/auth/*`, `shared/well_known_api.rs`). **All 10
plan tasks done.** Tasks 1–9 committed (`aec24be` → `13e37cb`); the task-9
teardown is complete (committed separately — see git log for the fosite
removal). Only the deferred follow-ups below remain, and none block.

**Done & committed (each its own commit, tree green throughout):**
- `internal/platform/auth/authservice` — JWT mint/validate, exact Rust
  claim shapes (`type`/`jti`/`nbf`, bare-string `aud`, **no** `permissions`
  claim), RS256 + HS256 fallback, multi-key JWKS (SHA-256 kid).
- `internal/platform/auth/grantstore` — auth-code + refresh-token +
  pending-auth storage in `oauth_oidc_payloads` (composite ids, camelCase
  payloads; the `iam_authorization_codes`/`iam_refresh_tokens` tables are
  legacy/unused in Rust).
- `internal/platform/auth/oauthapi` — hand-rolled `/oauth/{token,authorize,
  introspect,revoke,userinfo}` + `.well-known/openid-configuration` +
  `jwks.json`. 3 grants + PKCE (S256+plain). RFC-6749 `{error,
  error_description}` bodies.
- **OAuth client secrets switched Argon2→encryption** (`client_secret_ref`,
  via `internal/platform/shared/encryption`) — Rust parity. Touches
  oauth_client create/rotate, serviceaccount/application provisioning, and
  `fc-dev init` (now provisions+persists `FLOWCATALYST_APP_KEY`).
- Auth middleware now derives permissions from roles when a token lacks a
  `permissions` claim (`provider.FlattenPermissions`).
- `internal/platform/auth/loginbackoff` — failed-login backoff +
  attempt recording on `/auth/login` and `/oauth/token`.
- `internal/platform/shared/ratelimit` — distributed rate-limit store
  (Redis `INCR+EXPIRE` / Postgres `iam_rate_limit_events` / Noop;
  `FC_REDIS_URL` → Redis else Postgres else `FC_RATE_LIMIT_DISABLE=1`).
  Per-client + per-IP throttle on `/oauth/token` + `/oauth/authorize`.

**fosite is fully removed** — no `ory/fosite` dependency remains (and
`go-jose/v3` went with it via `go mod tidy`). The `provider/` package is
now a single `provider.go` holding only the session/claims helpers
(`ValidateSessionToken`, `MintSessionToken`, `ResolveClaims`,
`BuildClaims`, `FlattenPermissions`, `SigningKey`, `Issuer`,
`AccessTokenTTL`) plus a trimmed `Config{Issuer, SigningKey,
AccessTokenTTL}`. The 10 fosite endpoint/storage/hasher/session files +
their 2 tests were deleted; middleware's `writeInvalidTokenError` no longer
calls `fosite.ErrorToRFC6749Error`; `wire.go`/`envcfg.go` dropped
`GlobalSecret`, `SigningKeyID`, and the `payload.Repository` arg to
`NewProvider` (the `payload` package still lives — `subsystems.go`'s purger
uses it). Docs updated: ADR-0001 has an Update note, `architecture.md` and
`PLAN.md` describe the hand-rolled provider.

**Verified:** `go build ./...` clean, auth/middleware/server tests pass,
`make api-diff` exit 0 (OAuth routes are chi handlers, not huma — the lock
file is unaffected).

### Deferred (follow-ups, not blockers)
- `/oauth/authorize?provider=` direct-IDP branch (returns server_error) —
  the Go bridge resolves IDPs by **email domain**, not provider-id; needs a
  bridge method to build an IDP authorization URL from a provider id.
- In-memory per-instance rate-limit governor (perf layer atop the
  distributed store; `rate_limit_middleware.rs`).

**Done since:** `max_age` enforcement in authorize (2026-05-28) —
`sessiontoken.Claims` now carries `IssuedAt`, `ValidateSession` returns it,
and authorize re-authenticates a session older than `max_age`
(login_required under prompt=none).

### Parity references
- Rust source: `crates/fc-platform/src/auth/{auth_service,oauth_api,
  authorization_code*,refresh_token*,pending_auth_repository,login_backoff}.rs`,
  `shared/{well_known_api,rate_limit_store/*}.rs`.
- ADR-0001 (`docs/adr/0001-session-token-vs-oauth.md`) — why sessiontoken
  is split from fosite.

---

## 0. Session orientation (read this first)

**Where to start cold:**
- `make build` builds frontend (`pnpm install --frozen-lockfile && pnpm build`)
  then every Go binary. `make go-build` skips the frontend step.
- `make test-unit` runs all unit tests (DB-free, fast).
  `make test-integration` runs the testcontainers Postgres suite.
- `make api-diff` checks the committed `api/openapi.lock.json` against the
  live huma-generated spec; part of `make ci`.
- `go run ./cmd/fc-dev start --embedded-db-reset` boots an embedded PG +
  applies migrations + runs seeds + serves the platform API on :3000.
- `go run ./cmd/fc-dev init` (after start, with PG running) bootstraps
  admin user + internal IDP + default Client + Application + Service
  Account + OAuth client + writes `.env`.

**Drop-in port status:** functional end-to-end on fresh embedded Postgres
(events ingest → fanout → dispatch jobs → scheduler claims → mediator
dispatches with HMAC signing → frontend serves at `/`). The wire-format
parity claim vs Rust is **not yet empirically verified** — that's the
remaining gate before cutover. See §6.

### Recently landed (sealed-Result simplification + huma OpenAPI)

A 12-task architectural refactor completed in 2026-05:
- **Goose** replaces the hand-rolled migration runner; transparent
  `_fc_migrations` → `goose_db_version` bootstrap.
- **Sealed `pkg/fcsdk/commit`** package replaces the
  `usecase.Result[E]` / `UseCase[Cmd,Evt]` machinery. Use cases are now
  plain functions `func VerbAggregate(ctx, repo, uow, cmd, ec) (commit.Committed[E], error)`.
  Old machinery retained only for the consumer-app SDK
  (`pkg/fcsdk/usecase/*`). Compile-time seal preserved via unexported
  `event` field on `Committed[E]`.
- **Huma** (`danielgtaylor/huma/v2`) replaces kin-openapi. All 20
  aggregates registered via `huma.Register(api, op, handler)`; spec
  derived from the Input/Output struct types. Wire DTOs split into
  `<aggregate>/api/dto.go` separate from `operations.Command`.
- **`api/openapi.lock.json`** committed baseline; `make api-bump` /
  `make api-diff` enforce wire-format change discipline. CI fails on
  unintentional drift.
- **`internal/platform/shared/jsontime.Time`** + `httpcompat.Time` —
  fixed-precision microsecond ISO-8601 for all API response timestamps.
- **`internal/platform/shared/httpcompat`** — huma error transformer
  that emits the canonical `{code, message, details}` envelope with the
  right HTTP status mapped from `*usecase.Error.Kind`.
- **`pkg/fcsdk/commit` worked example:**
  `internal/platform/eventtype/{operations,api}/*.go` (also cors, role,
  every other aggregate — all follow the same shape).
- See CONVENTIONS.md §1 / §3 / §4 / §5a for the current patterns.

### Outstanding port work (read this before picking up the next task)

**Production-blocking (P0)** — empirical drop-in verification (§6):
no one has yet (a) brought up Rust against a fresh PG, (b) stopped Rust
and brought up Go against the same PG, (c) run the parity harness +
frontend + a real SDK consumer end-to-end. The infrastructure is in
place (`tools/parityharness/`, 14 YAML cases). The procedure is the
last gate before cutover.

**Parity surface gaps (P1)**
1. **BFF routes** for the embedded frontend. **All landed:** dashboard
   stats, filter-options, event-types (13/13), roles (9/9), processes
   (7/7, mount-twice), scheduled-jobs (6/6), **developer (7/7 — adds a
   new `openapispecs` package with diff/hash/sync use case)**.
   **Caveat on event-types sync-platform:** the `schemas` tally in the
   response is wire-compatible but currently zeroed — the eventtype
   sync use case doesn't yet track per-schema outcomes
   (created/updated/unchanged). Event-type-level counts
   (created/updated/deleted/total) ARE correct.
2. **Per-row sync events.** ~~`eventtype/operations/sync.go` emits only
   the rollup.~~ **Done for eventtype + role.** `commit.Sync` (backed
   by `usecasepgx.CommitSync`) persists a batch of save + delete
   pairs alongside their per-row events and the rollup, all in one
   transaction. Two worked examples: `eventtype/operations/sync.go`
   and `role/operations/sync.go` (the latter is the
   sync-from-static-catalogue case — exports `seed.PlatformRoles`,
   uses new `role.Repository.FindBySource` + `CountAssignments`).
   Same gap still open for dispatch_pool, subscription, scheduled_job,
   process, platformconfig.
3. **Specialty routes** — connection activate/pause, dispatch-pool
   suspend (new use cases needed).
4. **Principal find-methods** — Go has 3, Rust has 14+
   (`find_by_service_account`, `find_users`, `find_with_filters`, etc.)
   Needed when filtered `/api/principals` endpoints + OIDC bridge
   auto-provisioning land.

**Subsystem gaps**
5. **Router** — Prometheus metrics + HdrHistogram; full
   `/monitoring/*` / `/warnings/*` / `/config/*` HTTP surface
   (~40 routes); TrafficStrategy for ALB integration. Manager surface
   for `check_memory_health`/`restart_consumer`/`get_pool_stats`/
   `reap_stale_entries`/`pool_codes`/`consumer_ids` — blocks
   deferred LifecycleManager tasks.
6. **Outbox** — MySQL/Mongo backends, GlobalBuffer with
   BufferFullError, RecoveryTask for stuck PROCESSING items,
   Prometheus `/metrics`.
7. **Stream** — StreamHealthService with per-projection snapshots,
   `StreamProcessorHandle.Stop()` graceful shutdown, `/ready` endpoint.
8. **Scheduler** — `PausedConnectionCache.spawn_refresh_task`,
   separate `BlockOnErrorChecker` component.

**Background workers + glue**
9. ~~**WebAuthn ceremony purger**~~ **Done.** `StartPurger` in
   `internal/server/subsystems.go` runs every minute on a single
   goroutine and calls `PurgeExpired` on three ephemeral-auth repos:
   `oauth_oidc_payloads`, `oauth_oidc_login_states`, and
   `webauthn_ceremonies`. Wired into `fc-server` (when
   `FC_PLATFORM_ENABLED=true`) and `fc-dev start` unconditionally.
10. **OIDC bridge auto-provisioning + IDP role-mapping** — ~~Go fails
    with `USER_NOT_PROVISIONED` for unknown emails.~~ **Done.**
    `LoginEndpoint.autoProvision` looks up the `EmailDomainMapping`
    carried by the login_state row, creates the Principal via
    `principalops.CreateUser` with the mapping's scope +
    primary-client-id. `LoginEndpoint.syncIdpRoles` then runs on every
    callback (new OR existing user): the `roles` claim is translated
    through `oauth_idp_role_mappings`, filtered through the
    EmailDomainMapping's `allowed_role_ids`, and applied via the new
    `principalops.SyncIdpRoles` use case that preserves
    admin-assigned roles untouched (only IDP_SYNC-sourced
    assignments get replaced). Unknown IDP role names log a security
    rejection. Bridge wired in `WirePlatform`: `POST /oauth/check-domain`,
    `GET /oauth/oidc/login`, `GET /oauth/oidc/callback`. SessionWriter
    is left at its JSON-200 fallback; the frontend will swap it for
    the session-cookie write at startup.
11. **WebAuthn enumeration defence** — Rust returns deterministic-fake
    `allowCredentials` for unknown emails; Go returns empty.
12. **Fanout subscription cache race** — 5s TTL race window between
    create + first event. Mitigation: drop TTL or refresh-per-cycle.

**Infrastructure**
13. **AWS Secrets Manager** for DB credentials rotation (`DB_SECRET_ARN`) —
    Rust supports it; Go has a TODO in `envcfg.go::ResolveDatabaseURL`.
14. **ALB target-group registration** on leader transition (Rust
    feature-gated; not ported).
15. **HTTP slot-pool port** — DONE per `internal/router/host_pool.go`.

**Quality / quality-of-life**
16. **Structured logging tidy** — Go uses inline `slog.Warn(...)` with
    ad-hoc keys vs TS's consistent `this.logger.warn({...}, "msg")`
    shape. Sweep + normalise.
17. **Flaky TSID test** — `TestUniquenessSerial`/`Parallel`
    intermittently fail with `duplicate TSID generated` at high
    throughput. Lower sample count or add sub-ms counter.

**Just-introduced from the huma migration**
18. **WebAuthn session cookie** (`webauthn/api/api.go`) — `SessionWriter`
    can't reach `http.ResponseWriter` from inside a huma handler.
    Currently unwired. Needs huma adapter or move to OIDC bridge.
19. **`emaildomainmapping /lookup` negative case** — now `200 + {found:false}`,
    was `404 + {found:false}`. Restore the 404 if byte-identical parity
    is required.

**Suggested next moves** (priority):
1. **Drop-in verification (P0)** — run §6 procedure. The proof.
2. **BFF routes (P1.1)** — unblocks the embedded frontend.
3. **Per-row sync events (P1.2)** — visible regression for SDK consumers
   relying on per-row events.
4. **Subsystem gaps** — needed for prod observability + graceful shutdown.

## Historical refactor notes (sealed-Result + huma OpenAPI — completed)

The following is historical context for the 12-task refactor that landed
in 2026-05. The summary in §0 above is the canonical "what landed"
description; keep reading only if you need the per-step detail.

## Active refactor — sealed-Result simplification + huma OpenAPI

A 12-task plan is in flight to (a) move every use case from the
`UseCase[Cmd,Evt]` interface + `usecase.Result[E]` machinery onto plain
functions returning `(commit.Committed[E], error)`, and (b) replace the
hand-rolled kin-openapi spec builder with `danielgtaylor/huma/v2` once
the use case layer is leaner. The seal moves to a new package
`pkg/fcsdk/commit` with an unexported `event` field on `Committed[E]`
that external code cannot populate — the protection is preserved, the
ceremony drops by ~70%.

### Done so far (foundations + full use case sweep + huma eventtype pilot)

- **Goose migrations.** `internal/migrate/migrate.go` rewritten to
  delegate to `pressly/goose/v3`. All 29 SQL files prefixed with
  `-- +goose Up`. Forward-only. Bootstrap seeds `goose_db_version`
  from any pre-existing `_fc_migrations` and drops the legacy table.
  Top-level `/migrations/` deleted; `internal/migrate/sql/` is the
  single canonical source.
- **`internal/platform/shared/jsontime`.** Time wrapper that always
  marshals as fixed-6-digit microsecond ISO-8601 (`2026-05-26T12:00:00.123456Z`).
  Used by the upcoming huma response DTOs. Does NOT touch the HMAC
  signing path in `internal/router/mediator.go` (still milliseconds).
- **`pkg/fcsdk/commit` package.** `Committed[E]` sealed struct +
  `Save` / `Delete` / `SaveAll` / `Emit`, each delegating to the
  existing `usecasepgx.Commit*` family for the transaction machinery
  and re-shaping the return type. Compile-time seal verified by
  attempting external construction (`commit.Committed[int]{event: 42}`
  is rejected with `cannot refer to unexported field event`).
- **Use case migration: ALL 16 aggregates done.** Each dropped its
  `*XxxUseCase` structs, `NewXxxUseCase` constructors, three-method
  interface, and `var _ usecase.UseCase[...]` assertions. Each now
  exposes plain functions `func VerbAggregate(ctx, repo, uow, cmd, ec)
  (commit.Committed[E], error)` with the validate/authorize/business
  logic inlined. The api package's `State` struct collapsed to
  `{Repo, UoW *usecasepgx.UnitOfWork}` (plus any extra repos needed
  for cross-aggregate validation, e.g. principal/api/api.go).
- **Old `usecase.Result/Run/UseCase` surface** — kept (not deleted).
  Zero platform-internal code references it; only `pkg/fcsdk/*`
  (consumer-app SDK) still uses it, and the new `commit` package
  delegates to `usecasepgx.Commit*` internally. CONVENTIONS.md §3
  has been rewritten with the new worked example.
- **Huma foundation landed.** `danielgtaylor/huma/v2` installed.
  `internal/platform/shared/httpcompat` exposes:
  - `httpcompat.Init()` — installs the huma error transformer that
    maps `*usecase.Error` → the canonical `{code, message, details}`
    envelope with the right HTTP status (called once in WirePlatform).
  - `httpcompat.ErrorModel` — wire shape; implements `huma.StatusError`.
  - `httpcompat.Time` — alias for `jsontime.Time` (microsecond ISO-8601).
- **Eventtype huma pilot landed.** `internal/platform/eventtype/api/`:
  - `dto.go` — wire DTOs (`CreateEventTypeRequest`, `EventTypeResponse`,
    etc.) with explicit `toCommand()` / `fromEntity()` mappers; uses
    `httpcompat.Time` for timestamp fields.
  - `api.go` — fully huma-based, `Register(api huma.API, *State)`,
    seven operations registered (list / create / get-by-id /
    get-by-code / update / delete / add-schema).
  - `openapi.go` — DELETED (kin-openapi spec); huma derives the spec
    from the Input/Output struct types.
  - `WirePlatform` creates a `humachi.New(r, ...)` API once and threads
    it into the eventtype `Register` call.
- **Lockfile + spec gate.**
  - `api/openapi.lock.json` — committed baseline of the live spec
    (currently only eventtype's 7 endpoints; grows per huma migration).
  - `tools/dump-spec/main.go` — emits the live spec to stdout without
    touching the DB (registers routes against nil-dep `*State` values;
    the spec generator only inspects Input/Output types).
  - `make dump-spec` / `make api-bump` / `make api-diff` Makefile
    targets; `make ci` now runs `api-diff` as part of the CI sweep.
  - `tools/dump-spec/dump_spec_test.go` — `TestOpenAPISpecLocked`
    snapshot test that fails if the live spec drifts from the lockfile.
- **CONVENTIONS.md updated.** §3 rewritten for `commit.Committed[E]` /
  the plain-function use case shape. §4 (handler pattern) updated to
  show the huma `func(ctx, *Input) (*Output, error)` shape calling
  use case functions directly. §5a documents the spec lockfile + CI
  gate workflow.

### Remaining

The 12-task refactor is **complete**. All 20 platform aggregates are
huma-registered, all `<aggregate>/api/openapi.go` files are gone,
`internal/platform/shared/openapi/` (kin-openapi framework) is gone,
`getkin/kin-openapi` removed from `go.mod`. `WirePlatform` configures
huma to serve `/api/openapi.json`, `/api/openapi.yaml`, and
`/api/docs` (Swagger UI). The committed `api/openapi.lock.json`
covers every endpoint; `make api-diff` is part of `make ci`.

Two known follow-ups noted during the migration:

- `internal/platform/webauthn/api/api.go` — the legacy
  `SessionWriter` cookie-write callback can't reach
  `http.ResponseWriter` from inside a huma handler. The field stays
  on `State` but the cookie path is unwired; `authenticateComplete`
  returns `{principalId: ...}` JSON only. Either add a huma adapter
  that observes the response, or move the cookie write into the
  chi-side OIDC bridge package. Marked with a TODO in the handler.
- `internal/platform/emaildomainmapping/api/api.go` — the `/lookup`
  route's negative case now returns `200 + {found: false}` instead
  of the previous `404 + {found: false}` body. If byte-identical 404
  is required for parity, swap that branch back to
  `httperror.NotFound(...)`.

### Patterns for picking up Task 11

Worked example: `internal/platform/eventtype/api/`. The shape:

```go
// dto.go
type CreateXxxRequest struct {
    Field string `json:"field" doc:"..."`
}
func (r CreateXxxRequest) toCommand() operations.CreateCommand { ... }

type XxxResponse struct {
    ID        string          `json:"id"`
    CreatedAt httpcompat.Time `json:"createdAt"`
    // ... wire-shaped fields with explicit JSON tags ...
}
func fromEntity(e *xxx.Xxx) XxxResponse { ... }

// api.go
type State struct { Repo *xxx.Repository; UoW *usecasepgx.UnitOfWork }
const tag = "xxxes"

func Register(api huma.API, s *State) {
    huma.Register(api, huma.Operation{
        OperationID: "createXxx",
        Method: http.MethodPost,
        Path: "/api/xxxes",
        Tags: []string{tag},
        DefaultStatus: http.StatusCreated,
    }, s.create)
    // ... etc ...
}

type createInput struct { Body CreateXxxRequest }
type createOutput struct { Body apicommon.CreatedResponse }

func (s *State) create(ctx context.Context, in *createInput) (*createOutput, error) {
    ac := auth.FromContext(ctx)
    if err := auth.CanWriteXxx(ac); err != nil { return nil, err }
    ec := usecase.NewExecutionContext(ac.PrincipalID)
    committed, err := operations.CreateXxx(ctx, s.Repo, s.UoW, in.Body.toCommand(), ec)
    if err != nil { return nil, err }
    return &createOutput{Body: apicommon.CreatedResponse{ID: committed.Event().XxxID}}, nil
}
```

### Build status

`go build ./...` clean. `go test -race -short ./...` green
(including `TestOpenAPISpecLocked` snapshot of the current
eventtype-only spec).

---

## 1. The Drop-In Contract

The Go port is a **drop-in replacement** for the Rust binaries. That means:

1. **Same Postgres database.** No new tables, no schema changes. The
   29 migrations under `internal/migrate/sql/` are the Rust source's
   own — copied verbatim and embedded into every Go binary via
   `internal/migrate/`. Either build can apply them. Runner is
   `pressly/goose/v3`; legacy `_fc_migrations` databases upgrade
   transparently to `goose_db_version`.

2. **Byte-identical HTTP APIs.** Every existing SDK consumer + the Vue
   frontend MUST continue working with zero source changes after cutover.
   This is the load-bearing rule. Specifically:
   - Path: `/api/clients/{id}/activate` (not `/api/clients/{id}:activate`).
   - JSON shape: snake_case for inbound, camelCase for outbound (matches
     the TS+Rust convention).
   - Status codes: 201/204/200 must match Rust for the same operation.
   - Error envelope: `{ "error": "<code>", "error_description": "..." }`.

3. **Byte-identical event types.** The 41 platform event-type codes
   (`platform:iam:user:created` etc.) are emitted into `msg_events` and
   read by consumer SDKs. Codes are pinned in
   `internal/platform/seed/event_types.go` and individual subdomain
   `operations/events.go` files. **Fix the constant, not the consumer.**

4. **Same router behaviour for SDK consumers.** The message router is
   the most sensitive surface — see §5 below. Wire-format compatibility
   is mandatory: same HMAC signing scheme, same retry semantics, same
   header names, same per-message-group FIFO ordering, same circuit-
   breaker thresholds. Existing SDK consumer apps cannot tell whether
   they're being delivered to by the Rust or Go router.

5. **Single Go module.** `github.com/flowcatalyst/flowcatalyst-go` —
   all binaries + libraries live here.

6. **Existing JWT/token compatibility is NOT required.** The user
   explicitly accepted that tokens issued by Rust won't validate after
   cutover. Users + service accounts need to re-auth. New tokens use
   fosite + the existing `oauth_oidc_payloads` storage table.

## 2. Architecture at a Glance

```
cmd/
  fc-server/          — unified production binary (FC_*_ENABLED toggles)
  fc-dev/             — developer monolith with embedded Postgres (cobra subcommands)
  fc-router/          — standalone router binary (264 lines)
  fc-platform-server/ — placeholder; superseded by fc-server
  fc-stream-processor/
  fc-outbox-processor/
  fc-mcp-server/

internal/
  server/             — shared wiring (EnvCfg, WirePlatform, subsystem launchers)
  migrate/            — goose v3 migration runner (applies sql/*.sql)
  platform/           — every subdomain (20+ aggregates with operations/api/)
    auth/             — fosite-backed OAuth provider + OIDC bridge
    seed/             — 12 roles + 41 event types + 41 schemas
    scheduler/        — dispatch-job poller + dispatcher + stale recovery
    scheduledjob/     — cron-fired scheduled jobs
    shared/           — auth context, sink, BFF, SDK ingest
  router/             — message router internals (mediator/pool/queue_health/etc.)
  stream/             — CQRS projectors (events, dispatch_jobs, fan_out)
  outbox/             — consumer-app outbox processor
  mcp/                — MCP server
  queue/              — queue abstraction (Postgres, SQS)
  standby/            — Redis-backed leader election
  secrets/            — secret resolver
  sealed/             — compile-time-enforced UoW seal token
  tsid/               — typed TSID generator (prefix per entity)

pkg/fcsdk/            — SDK exported to consumer apps (usecase + usecasepgx)

migrations/           — 29 SQL files (verbatim from Rust)
```

### Key patterns

- **Sealed Unit-of-Work seal.** Every domain write must go through
  `usecasepgx.Commit` (or `CommitDelete`/`CommitAll`/`EmitEvent`). The
  seal is a sealed.Token whose only constructor lives in the `sealed`
  package; no one outside `usecasepgx` can mint one. This guarantees
  every aggregate write emits its DomainEvent + audit row atomically.

- **OAuth via fosite.** `internal/platform/auth/provider/` implements
  fosite's Storage + ClientManager + Hasher (Argon2id) against the
  existing `oauth_oidc_payloads` and `iam_oauth_clients` tables.
  fosite's `compose.Compose(...)` mints the OAuth2Provider with
  client_credentials + refresh_token + revoke + introspect + PKCE +
  authorize_explicit factories registered. **The token endpoint is
  ~80 lines of glue** — fosite does the rest.

- **Embedded Postgres for dev.** `cmd/fc-dev` uses
  `github.com/fergusstrange/embedded-postgres` (pure-Go, downloads PG
  binaries on first run). Same UX as Rust's `pg_embed` feature.

- **Event-type catalog as static Go data.** `internal/platform/seed/`
  ports the Rust `seed/platform_event_types.rs` + `platform_event_schemas.rs`
  as Go literals. The DSL (`obj/reqStr/optStr/reqBool/reqU32/...`)
  mirrors the Rust helpers so transcription stays mechanical.

## 3. Build / Run

```bash
go build ./...                # everything
go test ./...                 # everything (no DB required for current tests)

go run ./cmd/fc-dev --help    # see subcommands
go run ./cmd/fc-dev           # default: start embedded PG + platform API
go run ./cmd/fc-dev fresh --yes
go run ./cmd/fc-server        # production-shape binary (needs external PG)
```

Env toggles for `fc-server` (TS-aliased names also supported):

| Toggle                          | Default | Purpose                       |
| ------------------------------- | ------- | ----------------------------- |
| `FC_PLATFORM_ENABLED`           | `true`  | Run the platform API server   |
| `FC_ROUTER_ENABLED`             | `false` | Run the SQS message router    |
| `FC_SCHEDULER_ENABLED`          | `false` | Run the dispatch scheduler    |
| `FC_STREAM_PROCESSOR_ENABLED`   | `false` | Run the CQRS stream processor |
| `FC_OUTBOX_ENABLED`             | `false` | Run the outbox processor      |
| `FC_STANDBY_ENABLED`            | `false` | Redis leader election         |

## 4. State of the Port

### What's done

- Phases 0–2 complete (foundation, common, tsid, queue, router primitives, secrets, config, standby).
- Phase 3 (every subdomain): 20+ aggregates with their operations, repository, api routes. Includes principal IAM verbs, serviceaccount/application/auth provisioning, full event-type/role/dispatch-pool/subscription/connection/scheduled-job CRUD.
- Phase 4 (stream + outbox): packages exist, internal APIs present.
- Phase 5 (SDK + MCP): SDK ported to `pkg/fcsdk/`. MCP scaffolded.
- OAuth/OIDC runtime via fosite: token, authorize, revoke, introspect, /.well-known/openid-configuration, /.well-known/jwks.json, OIDC bridge for external IDPs.
- WebAuthn HTTP wiring.
- Seed data: 12 platform roles + 1 platform application + 41 event types + 41 JSON schemas.
- Migration runner + 29 embedded SQL files.
- `cmd/fc-server` — unified binary.
- `cmd/fc-dev` — developer monolith with embedded PG + subcommands.

### What's stubbed (Production-blocking)

These are the items the audits surfaced. Each has a `TODO(<name>)` in
the source. Search for the marker to find the exact file:line.

1. ~~**`internal/server/subsystems.go`** — `StartStreamProcessor`,
   `StartOutboxProcessor`, `StartRouter` are signal-only stubs.~~
   **Done.** `StartStreamProcessor` launches events / dispatch_jobs /
   fan_out / partition_manager (per-projection sub-toggles default ON).
   `StartOutboxProcessor` runs the Postgres outbox poller against the
   shared pool; FC_OUTBOX_PLATFORM_URL is required, sqlite/mysql/mongo
   remain in `cmd/fc-outbox-processor`. `StartRouter` delegates to a
   new `internal/router.Server` (NewServer + Run); `cmd/fc-router/main.go`
   now only contributes signal handling + HTTP listener. See
   `EnvCfg`'s new Stream/Outbox/Router fields for the knob set.

2. ~~**Auth middleware.**~~ **Done.** `internal/platform/shared/middleware/middleware.go`
   now introspects the inbound `Authorization: Bearer <jwt>` (or the
   `fc_session` cookie used by the Vue frontend) through the fosite
   provider, builds an `AuthContext` from the JWT's extra claims
   (PrincipalID, Scope, Clients, Roles, Applications, Permissions, Email),
   and attaches it to the request. The `X-FC-Test-Principal` dev fallback
   now requires `FC_AUTH_ALLOW_TEST_HEADERS=true`. To make Permissions
   self-contained in the JWT, `BuildClaims` now flattens role→permissions
   at mint time (so `NewProvider` takes a `*role.Repository`). The
   middleware is mounted globally in `WirePlatform` via `r.Use(...)`.

3. **Init bootstrap depth.** `fc-dev init` writes a placeholder `.env`.
   The Rust impl creates: admin user + default Client + default
   Application + Service Account + OAuth client + anchor domain row.
   Port from `crates/fc-platform/src/shared/bootstrap_admin.rs` +
   `bin/fc-dev/src/init.rs`.

4. ~~**Argon2id PHC salt.**~~ **Done.** Shared
   `internal/platform/auth/passwordhash` package now owns Argon2id
   hashing — PHC envelope (`$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>`)
   with per-row random salt. Used by:
   - `principal/operations/create.go::Execute` (user passwords)
   - `principal/operations/reset_password.go` (password reset)
   - `cmd/fc-dev/init.go::hashSecret` (init admin password)
   - `auth/operations/oauth_client.go::generateSecret` (OAuth client
     secret)
   - `auth/provider/hasher.go` (fosite's `ClientSecretsHasher`)
   Verified end-to-end: init mints a CONFIDENTIAL OAuth client with
   PHC-hashed secret → `/oauth/token` exchange with the plaintext
   passes fosite's `Compare`; wrong secret returns `invalid_client`.
   Unit tests in `passwordhash_test.go` cover round-trip,
   per-call salt uniqueness, and 5 invalid-envelope rejections.

### What's stubbed (Lower priority)

5. **Per-row sync events.** `eventtype/operations/sync.go` is wired
   but emits only the rollup event. Rust emits per-row Created/Updated/
   Deleted alongside the rollup. Same pattern needed for role,
   dispatch_pool, subscription, scheduled_job, process, platformconfig
   sync ops (only event_type is ported today).

6. **WebAuthn enumeration defence.** `authenticate/begin` currently
   returns an empty challenge for unknown/federated emails. Rust
   returns deterministic-fake `allowCredentials` keyed by HMAC(email)
   so the response shape is indistinguishable from a real one.

7. **Router gaps** (from `Router parity audit`):
   - ~~RouterError struct~~ **Done.** `internal/router/error.go` with
     the full ErrorKind enum + helper constructors + AsRouterError unwrap.
   - ~~HealthService with rolling-window success rates~~ **Done.**
     `internal/router/health.go` — per-pool rolling counter (30m
     default, amortised O(1) record), consumer poll/stall tracking,
     HealthReport with Healthy/Warning/Degraded bands matching the
     Java/Rust warning-count thresholds (5/20). 8 unit tests.
   - ~~WarningService with TTL cleanup + acknowledgement tracking~~
     **Done.** `internal/router/warning.go` — in-memory store with
     UUID ids, ack state, auto-ack on age, capacity-bounded with
     oldest-10% eviction, optional Notifier forwarding. 5 unit tests.
     The existing router Warning struct in `notification.go` now
     carries id/createdAt/acknowledged so it's the same shape Rust
     uses (no separate stored-vs-emitted distinction).
   - ~~LifecycleManager coordination~~ **Done.**
     `internal/router/lifecycle.go` — owns the warning-cleanup,
     consumer-health, and health-report background loops. Wired in
     `Server.NewServer`/`Server.Run`/`Server.Shutdown`. Manager-coupled
     tasks (memory health monitor, consumer auto-restart, stale-entry
     reaper) deferred until the Go `Manager` grows the matching
     surface (Rust has `check_memory_health`, `restart_consumer`,
     `get_pool_stats`, `reap_stale_entries`, `cleanup_draining_pools`,
     `pool_codes`, `consumer_ids`). Optional `PoolStatsProvider`
     interface lets the health-report logger pick up real pool stats
     once that lands.

   Still pending: Prometheus metrics + HdrHistogram, full `/monitoring/*`
   / `/warnings/*` / `/config/*` HTTP surface (40+ routes),
   Swagger/OpenAPI, TrafficStrategy for ALB integration.

8. **Outbox gaps** (from `Outbox parity audit`): MySQL/Mongo backends,
   GlobalBuffer with BufferFullError, RecoveryTask for stuck PROCESSING
   items, GroupDistributorConfig + DistributorStats, Prometheus
   `/metrics`.

9. **Stream gaps** (from `Stream parity audit`): StreamHealthService
   with per-projection snapshots, `StreamProcessorHandle.Stop()` graceful
   shutdown, `/ready` endpoint.

10. **Scheduler gaps** (from `Scheduler parity audit`):
    `PausedConnectionCache.spawn_refresh_task` integration, separate
    `BlockOnErrorChecker` component.

11. **Specialty routes**: connection activate/pause, dispatch-pool
    suspend (new ops needed, not just route wiring).

12. **AWS Secrets Manager integration** for DB credentials rotation
    (Rust supports DB_SECRET_ARN; Go skips it — explicit TODO in
    `internal/server/envcfg.go::ResolveDatabaseURL`).

13. **ALB target-group registration** on leader transition (Rust feature-gated; not yet ported).

13a. **Embedded NATS as dev broker** — deferred. Considered as a
    replacement for the Postgres-table queue in fc-dev. Held off
    because (a) prod uses SQS, so introducing NATS only in dev breaks
    dev/prod parity; (b) "single-node simple NATS" loses messages on
    restart, JetStream adds a stateful component undoing the
    one-data-dir appeal of embedded PG; (c) nothing today needs broker
    semantics PG polling can't model. Revisit if prod ever migrates
    off SQS, or if true durable-multi-consumer fan-out at the broker
    level becomes a requirement. **If/when this happens, NATS must
    slot in via the existing `internal/queue.Publisher` /
    `internal/queue.Consumer` abstraction** — same contract as PG
    dev + SQS prod backends, not a parallel pattern.

14. ~~**Frontend.**~~ **Done.** Vue 3 source copied to top-level
    `frontend/`; `dist/` + `node_modules/` gitignored.
    `frontend/embed.go` + `frontend/handler.go` provide
    `frontend.Handler() http.Handler` that mirrors Rust's
    `bin/fc-dev/src/main.rs::embedded_asset_handler`: exact-path
    asset → MIME-typed response with `Cache-Control: immutable` for
    `/assets/*`, otherwise SPA fallback to `index.html`. Mounted on
    fc-dev as the chi NotFound handler so every API route takes
    precedence. `frontend.IsAvailable()` lets the caller skip the
    mount cleanly when the binary was built without
    `make frontend`. **Build pipeline:** `make build` now depends on
    `make frontend`, which runs `pnpm install --frozen-lockfile` +
    `pnpm build` in `frontend/`. For backend-only iteration,
    `make go-build` skips the frontend step. Smoke verified end-to-end:
    `GET /` → SPA shell; `GET /assets/*.js` → immutable cache;
    `GET /api/event-types` → still served by the API; SPA history-mode
    routes (`/principals/some-id`) → fall back to `index.html`. The
    Hey-API generated TypeScript client under
    `frontend/src/api/generated/` is currently committed (matches
    Rust); will be regenerated from Go's spec — see item #24a
    (OpenAPI), now landed (framework + 3 aggregates). Binary size
    delta: +7.9MB (matches the 7.4MB dist/ + embed overhead).

14a. **OpenAPI spec generation.** **Framework done.**
    `internal/platform/shared/openapi/` provides `Doc` + `Op()` +
    helper option builders (`Tag`, `PathParam`, `QueryParam`,
    `RequestBody`, `Response`) with reflective schema generation via
    `getkin/kin-openapi/openapi3gen`. Each api package pairs its
    `RegisterRoutes` with an `OpenAPI(doc)` function — three landed
    today (eventtype, principal, subscription), each covering every
    route the package exposes. `WirePlatform` builds the Doc,
    threads it through each registrar, and mounts
    `GET /api/openapi.json` unauthenticated for tooling
    (oasdiff, hey-api codegen). Smoke verified:
    `curl /api/openapi.json` → 200, 22.5KB, 17 paths, 19 component
    schemas. Parity-harness YAML in
    `tests/parity/requests/openapi/spec.yaml` asserts the spec is
    served + has the core OpenAPI 3.0 top-level shape.
    **Still pending** (per-PR work as api packages get touched):
    spec for the other ~17 aggregates (client, role, application,
    serviceaccount, etc.); wiring the parity-spec CI job
    (`.github/workflows/ci.yml`) to actually run `oasdiff` once the
    spec is complete; pointing frontend's `openapi-ts.config.ts` at
    Go's spec URL; Swagger UI at `/api/swagger`. Pattern recommendation
    for future packages: prefer a fused `Mount(r, doc, state)` helper
    that registers route + spec together so they can't drift; today
    `RegisterRoutes` + `OpenAPI` are paired by convention.

15. **Frontend-only `/bff/*` routes.** Frontend source enumerated:
    the Vue app calls ~30 BFF endpoints across dashboard, filter-options,
    event-types, roles, processes, scheduled-jobs, and developer pages.
    **Now ported:** `/bff/dashboard/stats`, `/bff/filter-options/clients`,
    `/bff/event-types/filters/applications` —
    `internal/platform/shared/bff/filter_options.go`. Smoke-verified.
    Parity YAML at `tests/parity/requests/bff/filter-options-clients.yaml`.

    **Remaining BFF endpoints** (prioritised by frontend page usage):

    *Event-types page (`/bff/event-types/*`):* **9 of 13 routes
    ported.** `internal/platform/shared/bff/event_types.go` covers
    list (with status/application/subdomain/aggregate filters),
    get-by-id, create (with optional initial schema), update
    (metadata), delete, add-schema, plus the cascading filter
    endpoints (`/filters/subdomains?application=...` and
    `/filters/aggregates?application=...&subdomain=...`). Wire
    DTOs match Rust's BffEventTypeResponse exactly: items wrapped
    in `{items, total}`, denormalised application/subdomain/
    aggregate/event fields, ISO-8601 timestamps as strings,
    embedded specVersions with schema as a stringified JSON blob.
    Smoke-verified — `total: 73` returned on a fresh boot (72
    platform seeds + 1 we created).
    Parity YAML in `tests/parity/requests/bff/event-types-list.yaml`.
    **Still pending** (each needs a corresponding use case):
    archive (`POST /{id}/archive` — needs ArchiveUseCase),
    finalise-schema, deprecate-schema (`POST /{id}/schemas/{v}/finalise`
    and `/deprecate` — need lifecycle UCs), sync-platform (needs
    SyncEventTypesUseCase wired through state).
    Rust source: `crates/fc-platform/src/shared/bff_event_types_api.rs`
    (954 LoC).

    *Roles page (`/bff/roles/*` — 6 routes):*
    list, get-by-name, create, permissions list, permissions detail,
    sync-platform, `/roles/filters/applications`. Rust:
    `bff_roles_api.rs` (751 LoC).

    *Processes page (`/bff/processes/*` — 7 routes):*
    CRUD + archive + by-code lookup. Rust mounts the canonical
    `processes_router` under both `/api/processes` and `/bff/processes`
    so the BFF surface is the same as the API surface. Go equivalent:
    extract the chi.Mux from `processes/api.RegisterRoutes` and
    re-mount under `/bff/processes` — needs a small refactor to make
    `RegisterRoutes` accept a configurable prefix, or factor out the
    sub-router so it can be mounted twice. Same pattern applies to
    scheduled-jobs.

    *Scheduled-jobs page (`/bff/scheduled-jobs/*` — 6 routes):*
    list, get, instances list, instance get, filter-options. Same
    mount-twice pattern as processes. Rust:
    `bff_scheduled_jobs_api.rs` (467 LoC) — but a chunk of that is
    Rust-specific scaffolding; effective Go port is much smaller.

    *Developer page (`/bff/developer/*` — 8 routes):*
    applications list/get, OpenAPI current/versions, event-types,
    sync-platform-openapi. Rust: `bff_developer_api.rs` (446 LoC).
    Lower priority — only the Developer Portal page calls these.

    *Permissions (`/bff/roles/permissions/*` — 2 routes):* covered
    above under roles.

    **Recommended porting order for next session:**
    event-types + roles (most frontend pages), then processes +
    scheduled-jobs via mount-twice refactor, finally developer.
    Total remaining ~25 endpoints.

16. **OIDC bridge auto-provisioning.** Today the bridge fails with
    `USER_NOT_PROVISIONED` if no FlowCatalyst principal matches the
    IDP's email. Rust auto-creates via the anchor-domain row.

17. **WebAuthn ceremony purger.** `webauthn.CeremonyRepository.PurgeExpired`
    exists (added in the sqlc sweep, matches Rust's `purge_expired`),
    but nothing calls it on a loop. Rust runs it as a background task.
    Wire it alongside the payload purger or as a sibling poller.

18. ~~**sqlc nullable-JSONB override gap.**~~ **Done.** Added a second
    override entry (`db_type: "jsonb"`, `nullable: true`) so nullable
    JSONB columns now generate as `json.RawMessage` like the NOT NULL
    case. `audit.jsonOf()` helper removed; `OperationJson` reads
    directly as `json.RawMessage`. (The remaining `[]byte` in
    `dbq/models.go` is `WebauthnCredential.CredentialID`, which is a
    BYTEA column — the correct mapping.)

19. **Principal find-method surface.** The Go repo exposes only
    `FindByID`, `FindByEmail`, `FindAll`. Rust additionally has
    `find_by_service_account`, `find_active`, `find_users`,
    `find_services`, `find_by_client`, `find_by_scope`,
    `find_with_filters`, `find_anchors`, `find_by_application`,
    `find_with_role`, `search`, `find_names_by_ids`,
    `count_by_email_domain`. None are called by the current Go API
    surface, but they'll be needed as `/api/principals` filter
    endpoints + the OIDC bridge auto-provisioning land. Each is a
    small sqlc query; the schema is already mapped correctly.

20. ~~**`app_applications.service_account_id` is a principal id, not
    a SA row id.**~~ **Done.** `attach_service_account.go` now takes
    a `*principal.Repository` dependency and resolves the SA's
    linked principal id via `PrincipalFindByServiceAccount` (new sqlc
    query) before writing `app.ServiceAccountID = saPrincipal.ID`.
    `internal/server/wire.go` updated to pass the principal repo.

21. ~~**Password-hash verifiers must base64-decode.**~~ Superseded by
    the PHC envelope (§4 #4) — `passwordhash.Verify` parses the
    envelope and does the right thing. No more base64-stopgap.

22. **Pre-existing build failures (not my work).** Two packages don't
    compile against the current SDK client surface — flagged here
    because `make ci` will fail on them:
    - `cmd/fc-mcp-server/main.go` references `client.Config` which
      doesn't exist on `pkg/fcsdk/client` today.
    - `pkg/fcsdk/sync/synchronizer.go` references `client.FlowCatalystClient`,
      `client.SyncRoleItem`, `client.SyncRolesRequest`, `client.SyncResult`,
      etc. — none of which exist. `pkg/fcsdk/sync` was scaffolded ahead
      of the client expansion and the client expansion never happened.
    Either flesh out `pkg/fcsdk/client` to match what the consumers
    expect, or delete the consuming code if it's not on the immediate
    roadmap. `go build ./cmd/fc-dev ./cmd/fc-server ./internal/...`
    is clean — only these two paths fail.

23. **Flaky TSID test.** `internal/tsid.TestUniquenessSerial` /
    `TestUniquenessParallel` intermittently fail with `duplicate TSID
    generated`. Generator runs at >10k IDs/test which can hit
    millisecond-bucket collisions on a fast machine. Either lower the
    sample count or change the generator to incorporate a
    sub-millisecond counter. Not my work — flagged for follow-up.

## 5. The Message Router — Drop-in Specifics

The router is the most sensitive subsystem for drop-in compatibility
because **consumer apps' SDKs are wire-coupled to its behaviour**.
Specifically:

### Wire format

- **HMAC-SHA256 signing.** Auth token in `Authorization: Bearer fc_<token>`
  header is the existing scheme. Go side: `internal/platform/scheduler/auth.go::DispatchAuthService.Sign`
  produces the same token shape; the consumer SDK's verifier checks
  `HMAC(secret, jobID)`.

- **Per-message-group FIFO.** `MessageGroupDispatcher` (Go) +
  Rust's equivalent enforce per-message-group ordering. Same group →
  serial dispatch; different groups → concurrent under a global
  semaphore cap.

- **Headers.** `X-FC-Job-ID`, `X-FC-Subscription-ID`, `X-FC-Attempt`,
  `X-FC-Max-Attempts`, `X-FC-Message-Group` are emitted on every
  dispatch. **Don't rename these — consumers parse them.**

- **Retry strategy.** Exponential backoff with jitter (default).
  Specifically: `min(base * 2^(attempt-1), cap)` + ±15% jitter.
  Configurable per dispatch-pool. The values + jitter algorithm need
  to match Rust's; the current Go impl needs an audit pass to confirm.

- **HTTP transport parity (`internal/router/mediator.go`).** Audited
  against `crates/fc-router/src/mediator.rs`. Aligned on
  `MaxIdleConnsPerHost=10` ↔ `pool_max_idle_per_host(10)`,
  `IdleConnTimeout=90s` ↔ reqwest default, HMAC sign format
  (`%Y-%m-%dT%H:%M:%S%.3fZ`), retry policy (3 × [1s,2s,3s]), skip-retry
  rules (Success/ErrorConfig/RateLimited), HTTP/2 default + HTTP/1.1
  forcing (`TLSNextProto={}` + `ForceAttemptHTTP2=false` is
  functionally equivalent to reqwest's `http1_only()` for HTTPS — the
  only mode used in prod). **One bug caught + fixed during the audit:**
  `MediatorConfig.ConnectTimeout` was stored but never wired into the
  Transport's `DialContext`, so a slow TCP connect was bounded by
  `Client.Timeout` (15min prod) rather than `ConnectTimeout` (30s prod).
  Fix: explicit `net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30s}`
  feeding `transport.DialContext`. Regression test
  `TestMediatorConnectTimeoutHonoured` points at RFC-5737 TEST-NET-1
  and asserts elapsed < 2s with a 250ms ConnectTimeout.

- **Intentional Go-only divergence: `StrictMaxConcurrentStreams`.**
  AWS ALBs advertise a per-H2-connection stream cap (~128) via SETTINGS
  frames. Without `StrictMaxConcurrentStreams`, Go's HTTP/2 client
  ignores the hint and opens streams until the server returns
  `REFUSED_STREAM` / `GOAWAY` — bad for tail latency and failure-mode
  observability. With it, the client *waits* for an in-flight stream
  to complete before starting a new one. Rust's reqwest doesn't expose
  this knob today, so Go is strictly safer against ALB's H2→H1
  translation cap. Set via
  `http2.ConfigureTransports(transport).StrictMaxConcurrentStreams = true`
  in `NewHTTPMediator` (production HTTP/2 path only; HTTP/1.1 dev
  path unaffected).

- **Per-host HTTP/2 connection pool (Rust parity).**
  `internal/router/host_pool.go` ports
  `crates/fc-router/src/http_pool.rs`. Each origin (scheme+host+port)
  gets a `HostConnectionPool` holding one or more `ClientSlot`s; each
  slot owns a dedicated `*http.Client` whose `*http.Transport` keeps
  its own connection pool. Under HTTP/2 that means one h2 connection
  per slot, so growing slots raises the effective concurrent-stream
  cap past what a single connection can multiplex. Defaults match
  Rust `HostPoolSizing::default()` (high-watermark=100, low=20,
  max-slots=8, slot-idle-grace=60s, sweep=15s). Replaces the older
  `host_limiter` semaphore (commit 49dd5a9) — the pool grows
  capacity on demand instead of just throttling, which matches what
  Rust does and removes the cross-pool dispatch ceiling the limiter
  imposed. Sweep goroutine started by `NewHTTPMediator`; stop it
  with `HTTPMediator.Close()`.

- **Intentional Go-only knob: explicit `TLSHandshakeTimeout`.** Go's
  stdlib default is 10s but we set it explicitly via
  `MediatorConfig.TLSHandshakeTimeout` so a stdlib default change can't
  silently shift our handshake budget.

### Sub-systems that talk to consumers

- `/oauth/token` (client_credentials grant) — SDK consumers exchange
  the OAuth client_id + client_secret for a JWT, then call back into
  the platform's `/api/dispatch-jobs/batch` + `/api/events/batch` with
  the JWT in the Authorization header.

- `/api/dispatch-jobs/batch` — SDK outbox processors POST batched
  dispatch jobs. **This is the highest-traffic SDK endpoint.** Wired
  in `internal/platform/shared/sdk/dispatch_jobs_batch.go`.

- `/api/events/batch` — SDK consumers POST domain events for fan-out.
  Wired in `internal/platform/event/api/api.go`.

- **`/api/platform/cors/allowed`** — public; SDKs hit this pre-flight
  to learn which origins the platform accepts.

### Co-ordination

- **Same database, same queue.** During cutover, the Go scheduler can
  pick up where Rust left off because `msg_dispatch_jobs` is the
  source of truth — both implementations claim PENDING jobs via
  `FOR UPDATE SKIP LOCKED`. **It's safe to run one of each pointing at
  the same DB for the duration of the migration** (each will claim
  half the work).

- **Stale recovery.** Both implementations revert QUEUED→PENDING
  after a stale-after window. If running side-by-side, set Rust's
  window equal to Go's (or shorter on whichever you trust more).

### Tests we DON'T yet have

- **Contract tests** that hit the Rust binary + Go binary with the
  same input and diff the JSON / header output. This is the highest-
  leverage thing to build next for the router specifically.

- **Integration tests against a live Postgres.** All current tests are
  unit-level. A `docker-compose.test.yml` with PG + a parity harness
  would catch the byte-identical-API requirement automatically.

## 6. How to Verify Drop-in (the P0 gate)

The drop-in claim is **functional** (Go boots, applies migrations,
serves requests, fanout produces jobs, scheduler dispatches) but
**not yet empirically verified for wire-format parity vs Rust**.
This is the last gate before cutover.

### What's in place

- **`tools/parityharness/`** — a working binary that hits two URLs
  with the same YAML-described request and diffs status + headers +
  body shape. Placeholders (`tsid`, `uuid`, `iso8601-microsecond`,
  `any-string`, `any-object`, etc.) handle inherently-different
  values (IDs, timestamps). Missing env vars cause clean SKIPs.
- **14 YAML cases** under `tests/parity/requests/{smoke,event-types,
  dispatch-jobs,principals,bff,router,openapi}/`. Coverage is
  partial — the full API surface is ~200 routes; this is the smoke
  set. **Grow as confidence demands.**
- **`api/openapi.lock.json`** — the committed Go spec. Once Rust
  emits its own spec, the cleanest forward-looking parity check is
  `oasdiff breaking <rust.json> api/openapi.lock.json` in CI.

### Procedure (executable by hand; CI variant noted)

1. **Bring up Rust** against a fresh PG. Apply Rust's migrations.
   Run init (or its Rust equivalent). Note the URL — call it
   `RUST_URL=http://localhost:3000`.

2. **Bring up Go** against a SECOND fresh PG (or the same one — both
   migration sets are idempotent, but a fresh PG eliminates one
   variable). Run `go run ./cmd/fc-dev start --embedded-db-reset`.
   Note the URL — call it `GO_URL=http://localhost:3001`.

3. **Authenticate.** Both binaries need an admin to be useful past
   the unauthenticated endpoints. Run `fc-dev init` (Go) and the
   Rust equivalent against their respective DBs. Mint an admin token
   on each via `POST /oauth/token` with the seeded credentials.
   Export the token: `export ANCHOR_TOKEN=...` — the parity harness
   substitutes this into YAML cases that need it.

4. **Run the parity harness:**

   ```bash
   go run ./tools/parityharness \
       -rust=$RUST_URL \
       -go=$GO_URL \
       -dir=tests/parity/requests
   ```

   Expected outcomes: PASS for cases that match exactly, FAIL with a
   diff for cases that differ, SKIP for cases whose required env
   vars aren't set. Exit 0 if no FAIL; exit 1 otherwise.

5. **Investigate any FAIL.** Common shapes of failure:
   - Wire-format drift (a field name differs) → fix the Go side
   - Status-code drift → fix the Go side
   - Header drift (X-FC-* family) → fix the Go side
   - Placeholder mismatch (e.g. timestamp format) → fix the YAML
     or the Go side, whichever is canonical for that field

6. **Frontend smoke** — set `VITE_API_BASE_URL=$GO_URL`, build the
   Vue app, click through the main screens (clients, users,
   applications, event types, dispatch jobs). Anything that breaks
   becomes a parity harness YAML case + a Go fix.

7. **SDK consumer smoke** — point one of your real consumer apps at
   the Go server (change `FLOWCATALYST_URL`). Watch for outbox
   delivery, event ingestion, dispatch retries.

### CI variant

Once Rust binaries are published in a way GitHub Actions can pull
them down, add a `parity-spec` CI job that does steps 1-4 in a
matrix. Until then, the harness runs locally as part of the cutover
checklist; the `api/openapi.lock.json` snapshot test catches Go-side
drift in every PR (already wired into `make ci`).

### Known parity gaps (intentional or unresolved)

- **Existing JWT/token compatibility is NOT required** (§1 #6) —
  users + service accounts re-auth post-cutover. Don't test this.
- **WebAuthn session cookie** (`webauthn/api/api.go`) — currently
  unwired post-huma migration. Affects passkey flows.
- **`emaildomainmapping /lookup` negative case** — `200 + {found:false}`
  vs Rust's `404 + {found:false}`. Restore the 404 if needed.
- **Per-row sync events** — Go emits the rollup only; Rust emits
  per-row Created/Updated/Deleted alongside. SDK consumers that
  subscribe to per-row events will see fewer events from Go.

## 7. Suggested Sequence for the Next Session

If picking this up cold, I'd tackle in this order:

1. ~~**Subsystem wiring** (`internal/server/subsystems.go`)~~ — **Done.**
   All three launchers now host the real loops. New env knobs:
   `FC_STREAM_{EVENTS,DISPATCH_JOBS,FAN_OUT,PARTITIONS}_ENABLED` (default true),
   `FC_STREAM_BATCH_SIZE`, `FC_OUTBOX_PLATFORM_URL`,
   `FC_OUTBOX_PLATFORM_AUTH_TOKEN`, `FC_OUTBOX_{BATCH_SIZE,MAX_IN_FLIGHT,POLL_INTERVAL_MS}`,
   `FLOWCATALYST_CONFIG_URL`, `FLOWCATALYST_DEV_MODE`,
   `FC_NOTIFY_WEBHOOK_URL`, `FC_DRAIN_TIMEOUT_SECONDS`.

2. ~~**Auth middleware**~~ — **Done.** Bearer-token + `fc_session`
   cookie resolution via fosite introspection, mounted globally in
   `WirePlatform`. `FC_AUTH_ALLOW_TEST_HEADERS` gates the dev
   `X-FC-Test-Principal` path.

3. ~~**Run `fc-dev start` end-to-end.**~~ **Done.** Embedded PG boots,
   migrations apply, seed runs (1 application + 12 roles + 72 event
   types), `/health` returns 200, `/api/event-types` returns 403 with
   no token and 200 with `X-FC-Test-*` headers (dev mode). The boot
   path surfaced two bug classes:

   1. **Table-name mismatches** — the Rust→Go transcription picked the
      wrong "current" table name in several places along the migration
      history. Fixed: `iam_applications`/`msg_applications` →
      `app_applications`, `iam_clients` → `tnt_clients`,
      `iam_audit_logs` → `aud_logs`, `iam_webauthn_credentials` →
      `webauthn_credentials`, `iam_oauth_clients` → `oauth_clients`,
      `iam_anchor_domains` → `tnt_anchor_domains`,
      `iam_identity_providers` → `oauth_identity_providers`,
      `iam_idp_role_mappings` → `oauth_idp_role_mappings`,
      `iam_platform_configs` → `app_platform_configs`,
      `iam_platform_config_access` → `app_platform_config_access`,
      `iam_client_auth_configs` → `tnt_client_auth_configs`,
      `msg_dispatch_attempts` → `msg_dispatch_job_attempts`. Also
      cleaned the bogus join tables `iam_client_auth_config_*_clients`
      (those columns are JSONB on `tnt_client_auth_configs` per the
      schema) — `auth/repository.go` ClientAuthConfigRepo rewritten to
      read/write the two `JSONB` arrays directly.

   2. **chi middleware-after-routes** — `WirePlatform`'s `r.Use(...)`
      panicked when the caller had already registered `/health`. Fixed
      by wrapping the platform's route block in `r.Group(...)` so the
      Authenticator + CorrelationID middleware is scoped locally to
      the platform routes without ordering coupling to the caller.

   **sqlc adoption (in progress).** sqlc has been wired in:
   `sqlc.yaml` at repo root, queries live in `internal/sqlc/queries/`,
   generated code in `internal/sqlc/dbq/`. Schema source is the
   embedded migration set (`internal/migrate/sql/`) so sqlc's view of
   the DB matches what fc-dev/fc-server apply at boot. `make sqlc`
   regenerates; `make sqlc-verify` is wired into `make ci`.

   The **client** repository (`internal/platform/client/repository.go`)
   is migrated as the pattern: all 5 ops (FindByID, FindByIdentifier,
   Search, FindAll, Persist, Delete) go through `*dbq.Queries`. End-to-end
   smoke test passes (create → list → get → search). Remaining ~20
   repositories follow the same pattern.

   Migrating the first repo surfaced **three more bugs in the
   platformsink** (which writes to `msg_events` + `aud_logs`):

   1. **Event IDs were UUIDs but `msg_events.id` is `VARCHAR(13)`**. The
      Rust source uses an untyped 13-char TSID. The SDK's `pkg/fcsdk/usecase`
      now generates IDs via `pkg/fcsdk/tsid.GenerateUntyped()` — and to
      avoid duplication, the TSID primitives (`GenerateRaw`,
      `GenerateUntyped`, `GenerateWithPrefix`, `ToLong`, `FromLong`,
      Crockford encode/decode) have been moved to **`pkg/fcsdk/tsid`**;
      `internal/tsid` keeps the FlowCatalyst-specific `EntityType`
      catalog and forwards to the SDK primitives.

   2. **`msg_events` INSERT had `context` (wrong column), missing
      `time`/`correlation_id`/`causation_id`/`message_group`/`client_id`,
      and `ON CONFLICT (deduplication_id)` against a composite unique
      index** that Postgres can't infer. Now matches the Rust 14-column
      INSERT, no ON CONFLICT (matches Rust — dedup duplicates bubble up
      as tx failures).

   3. **`aud_logs` INSERT used phantom columns** (`event_id`,
      `event_type`, `aggregate_type`, `aggregate_id`, `command`,
      `created_at`). The actual schema has `entity_type`, `entity_id`,
      `operation`, `operation_json`, `principal_id`, `application_id`,
      `client_id`, `performed_at`. Also, `aud_logs.id VARCHAR(17)` —
      the old `newAuditID()` produced 26-char UUIDs. Now uses
      `tsid.Generate(tsid.AuditLog)` → `"aud_<13>"`.

   These three were also masking each other: each only became visible
   after the previous one was fixed.

   **sqlc bulk migration (mostly complete).** Repositories migrated (19/20):
   `client`, `role`, `cors`, `dispatchpool`, `identityprovider`,
   `process`, `application`, `connection`, `eventtype`, `serviceaccount`,
   `platformconfig`, `subscription`, **`application/client_config`,
   `principal`, `audit`, `webauthn/credentials`, `webauthn/ceremonies`,
   `auth/payload`, `emaildomainmapping`, `auth/{OAuthClient,AnchorDomain,
   ClientAuthConfig,IdpRoleMapping}`**. Each migration surfaced its own
   schema-vs-entity bug; tally of latent bugs caught and fixed during
   this pass:

   - `identityprovider` — the repo SELECTed an `allowed_email_domains`
     column that doesn't exist; the schema uses a junction table
     (`oauth_identity_provider_allowed_domains`). Rewritten to read/write
     the junction.
   - `process` — repo wrote `created_by` to `msg_processes`; the column
     doesn't exist (matches Rust's `created_by: None`). Dropped from
     persistence; entity field retained for API-shape compat.
   - `serviceaccount` — repo wrote `webhook_credentials JSONB` and
     `scope`, neither of which exist. Schema has flat `wh_*` columns;
     repo now maps `WebhookCredentials` struct ↔ flat columns.
   - `subscription` — repo wrote `endpoint` (schema is `target`),
     `filter` on the event-types junction (no such column),
     `key`/`value` on configs junction (schema is `config_key`/
     `config_value`), and `created_by` (no column). All fixed.
   - `principal` — repo read/wrote `user_identity` and `external_identity`
     JSONB columns that don't exist. Schema has flat
     `email/email_domain/idp_type/external_idp_id/password_hash/last_login_at`.
     Repo now maps the entity's UserIdentity/ExternalIdentity structs
     to those flat columns (mirrors Rust). `email_domain` is now
     computed at write-time from the email. Delete now explicitly
     clears `iam_principal_application_access` and
     `iam_client_access_grants` (only `iam_principal_roles` has FK
     ON DELETE CASCADE).
   - `webauthn/credentials` — repo wrote to a non-existent `credential`
     column. Schema has `passkey_data` (JSONB). Fixed the column name.
   - `webauthn/ceremonies` — INSERT to `oauth_oidc_payloads` omitted
     the `type` NOT NULL column. Now sets `type` to
     `WebauthnRegistration` / `WebauthnAuthentication`. Adds
     `PurgeExpired` to match Rust's purge surface.
   - `auth/OAuthClient` — repo wrote 5 columns that don't exist
     (`secret_hash`, `redirect_uris`, `grant_types`, `scopes`,
     `principal_id`). Schema has `client_secret_ref`, `default_scopes`
     (comma-joined VARCHAR), `pkce_required`,
     `service_account_principal_id`, plus junction tables for redirect
     URIs + grant types. Repo now: wires the `oauth_client_redirect_uris`
     and `oauth_client_grant_types` junctions; writes the Argon2 hash
     to `client_secret_ref`; joins `Scopes` as comma-string into
     `default_scopes`; maps `PrincipalID` to
     `service_account_principal_id`; defaults `pkce_required=true`.
     **Three more junction tables exist** —
     `oauth_client_post_logout_redirect_uris`,
     `oauth_client_allowed_origins`, `oauth_client_application_ids` —
     and the entity is missing `PostLogoutRedirectURIs`,
     `AllowedOrigins`, `ApplicationIDs`, `PKCERequired` fields. These
     are a separate task once the API surface needs them (see §4
     follow-ups).
   - `auth/IdpRoleMapping` — repo wrote `idp_type` and
     `platform_role_name` columns. Schema has only `idp_role_name`
     and `internal_role_name`; there's no `idp_type` (matches Rust,
     where the column was dropped). The entity's `IdpType` field is
     now ignored on persist and reads back as `""` to keep the API
     shape stable. `PlatformRoleName` maps to `internal_role_name`.
   - `auth/ClientAuthConfig` — the JSONB-array columns already matched
     the schema (this part was previously fixed); pure sqlc port.
   - `auth/AnchorDomain` — trivial sqlc port; no schema bugs.
   - `audit` — repo referenced `application_id` + `client_id` columns
     on `aud_logs`; they exist (added in 009_p0_alignment.sql) — no bug,
     but flagged `nullable JSONB` columns generating `[]byte` instead of
     `json.RawMessage` (the `db_type: "jsonb"` override only catches the
     non-nullable case). Wrapped in a `jsonOf()` helper. `DistinctValues`
     stays hand-rolled (dynamic column name).
   - `application/client_config` — trivial sqlc port; no schema bugs.

   **All repos migrated.** `dispatchjob` was the last; done in §7 #6.

   - `event` → reconciled + sqlc-migrated (see §4 boot-smoke note).
   - `scheduledjob` → sqlc-migrated. Schema (migration 021) matched
     the entity 1:1 — no reconciliation needed; `FindWithFilters` kept
     hand-rolled for the optional-filter pattern (mirrors application
     repo), all other ops go through `*dbq.Queries`.
   - `dispatchjob` → **Done.** Entity reconciled against the post-019
     schema (composite PK `(id, created_at)`, partitioned), repo
     sqlc-migrated. Specifics: dropped phantom `last_status_code`,
     `next_retry_at`, `dispatched_at`; added `last_attempt_at`,
     `last_error`, `duration_millis`, `scheduled_for`, `expires_at`,
     `idempotency_key`. `Metadata` switched from `map[string]string` →
     `[]Metadata{Key,Value}` so the JSONB column is `[]`-array shaped
     per Rust drop-in parity (the SDK BatchItem follows). Attempt
     persistence: `RecordAttempt` now mints a row id (TSID), derives
     the `status` column from the entity's `Success` bool, and drops
     the phantom `success` column. `FindWithFilters` + `DistinctValues`
     + `InsertBatch` stay hand-rolled (dynamic SQL / pgx.Batch);
     everything else goes through `*dbq.Queries`.

   **Build state.** `go build ./...`, `go test ./...`, `go vet ./...`,
   and `go run ./tools/analyzer/uowseal ./internal/platform/...` all
   pass after the sweep.

   **Boot smoke (run).** `fc-dev start --embedded-db-reset` boots cleanly
   on a fresh PG. The following round-tripped end-to-end through the
   new sqlc-backed repos:
   - `POST /api/principals` → 201 + `GET /api/principals/{id}` → 200
     with `userIdentity.email` correctly read back from the flat
     `email` column. List endpoint hydrates roles/assignedClients/
     accessibleApplicationIds as empty slices (junctions not wired in
     Persist by design — Phase 3c deferral).
   - `POST /api/oauth-clients` (CONFIDENTIAL) → 201 + returns the
     plaintext secret. `GET /api/oauth-clients/{id}` hydrates the
     `redirect_uris` and `grant_types` junction rows correctly.
   - `POST /api/oauth-clients/{id}/rotate-secret` → 200 + new
     plaintext secret. The hash lands in the `client_secret_ref`
     column via the sqlc `OAuthClientUpsert`.
   - Subsequent `POST /oauth/token` against the rotated client reads
     the row through the fosite Storage adapter (proves the read path).
     The 401 it returned was a business-logic check ("Client has no
     owning principal") — fosite read the row fine.
   - `GET /api/audit-logs` → returns rows with `entityType`,
     `entityId`, `operationJson`, `principalId` populated; the
     `IS NULL OR ...` filter (`?entityType=Oauthclient`) narrows
     correctly; `/api/audit-logs/entity-types` distinct-values facet
     works.
   - `POST /api/email-domain-mappings` with `additionalClientIds`
     populated → 201; read back hydrates the JSONB junction. (One
     gotcha: `identity_provider_id` is `VARCHAR(17)`; longer test
     IDs fail with a generic `PERSIST` 500 because the
     `repository persist failed` envelope swallows the underlying
     pgx error. Worth surfacing the cause to the API layer in a
     future polish pass.)
   - `GET /api/anchor-domains`, `GET /api/idp-role-mappings` →
     return empty lists; query paths exercised.

   **Not exercised** (require external state or a passkey
   authenticator): webauthn register/authenticate,
   `application/client_config` enable/disable (no direct API route
   today — driven by `POST /api/applications/{id}/clients/{id}/*`).

   **Pre-existing runtime errors surfaced by boot — now fixed:**
   - ~~`projection_status` doesn't exist~~ — `event_projection` +
     `dispatch_job_projection` now use `projected_at IS NULL` as the
     unprojected predicate (mirrors Rust). Both projectors run
     cleanly on an embedded boot.
   - ~~`fanout_status` doesn't exist~~ — `event_fan_out` now uses
     `fanned_out_at IS NULL`. The fanout implementation is
     **scope-limited**: it currently just stamps `fanned_out_at` on
     unfanned events without producing dispatch jobs (the "no
     subscriptions" fast path). Full pattern-matching subscription
     lookup + dispatch-job production lands together with the
     dispatchjob entity reconciliation — see §7 #6.
   - ~~`dispatch_mode` doesn't exist on msg_dispatch_jobs~~ — the
     scheduler poller now reads `mode` (the actual column name).
   - ~~`next_retry_at` doesn't exist~~ — the embedded schema has
     `scheduled_for` (from migration 004) but not `next_retry_at`
     (added in 011's `CREATE TABLE IF NOT EXISTS` which is a no-op
     once 004 has created the table). Poller now matches Rust:
     `WHERE status = 'PENDING'` only, ordered by
     `message_group ASC NULLS LAST, sequence ASC, created_at ASC`.
     Retry timing is owned by the dispatcher's backoff loop.
   - ~~ON CONFLICT mismatch~~ — `msg_events_read` and
     `msg_dispatch_jobs_read` PKs became `(id, created_at)`
     composites in migration 018. Both projectors now use
     `ON CONFLICT (id, created_at)`.
   - **Event aggregate reconciled.** `internal/platform/event/`
     entity + repo rewritten to match the actual schema:
     - Added `Time` field to the entity (CloudEvents `time`,
       distinct from `CreatedAt` which is DB insertion time).
     - `InsertBatch` now writes to `context_data` (was phantom
       `context`), includes `time` (was missing, NOT NULL
       constraint), drops `ON CONFLICT (deduplication_id)` (composite
       unique index can't always be inferred).
     - `FindByID` + `FindWithFilters` + `DistinctValues` drop the
       phantom `principal_id` column (no backing column on
       msg_events_read; the PrincipalID() helper still pulls from
       Context but reads come back with Context=[]).
   - **Event projection moved to Rust's CTE shape** — splits
     `application`/`subdomain`/`aggregate` from the `type` string
     via `split_part(type, ':', N)`; reads `e.data::text`, picks up
     `correlation_id`/`causation_id`/`message_group`/`client_id`.

   **End-to-end event verification (post-fix).** Create a principal
   → audit log row appears → event_projection picks up the
   `msg_events` row → `/api/events` returns 1 row with split
   `subject`/`type`/etc; `/api/events/{id}` and the filter +
   distinct-values endpoints all work.

   **Remaining stream gap (dispatchjob/scheduledjob):**
   the fanout fast path no longer crashes the projector loop, but
   dispatch-job production from subscription matches is stubbed.
   Lands with §7 #6 (dispatchjob entity reconciliation).

4. ~~**Boot smoke against the sqlc sweep.**~~ **Done.** All migrated
   repos round-trip end-to-end on a fresh PG. See §3 above for the
   exact endpoints exercised. The pre-existing
   `projection_status`/`fanout_status`/`dispatch_mode` column-mismatch
   errors on the stream + dispatch loops are now confirmed
   stream-processor blockers (not just CRUD-side) — the
   dispatchjob/scheduledjob/event schema reconciliation is the next
   bottleneck.

5. ~~**Init bootstrap depth**~~ **Done.** `fc-dev init` now mirrors the
   Rust `bin/fc-dev/src/init.rs` flow:
   - Runs migrations + the built-in seeds (idempotent).
   - Creates the anchor admin if no anchor USER exists — wires the
     internal IDP row, an anchor EDM for the admin's domain, the
     Principal with hashed password, and the `platform:super-admin`
     role assignment.
   - Resolves or creates the Default Client.
   - Errors if the supplied `--code` already exists; else creates the
     Application.
   - Mints the SA: a SERVICE Principal + a ServiceAccount row +
     attach back to the application + a CONFIDENTIAL OAuth client
     with `client_credentials` grant pointing at the SA principal.
   - Writes `.env` with `FLOWCATALYST_BASE_URL/APP_CODE/CLIENT_ID/
     CLIENT_SECRET` — in-place update for existing keys, appended
     under a `# FlowCatalyst (added by fc-dev init)` header
     otherwise. Idempotent: re-running with the same flags overwrites
     only the changed keys.
   - Flag set: `--admin-email`, `--admin-password`, `--code`,
     `--name`, `--app-type`, `--description`, `--default-base-url`,
     `--client-identifier`, `--client-name`, `--api-base-url`,
     `--root`, `--yes`, `--database-url`. `--yes` requires the
     required fields (`code`, `name`, `admin-email/password` on first
     run) to be provided as flags.

   **Two latent bugs caught + fixed during init port (both since
   superseded):**
   1. ~~`hashPassword`/`hashSecret` base64 stopgap~~ — replaced by the
      shared `passwordhash` PHC envelope (§4 #4 above).
   2. ~~`attach_service_account.go` FK bug~~ — fixed (§4 #20 above).
      The use-case now resolves the SA's principal id before writing
      `app.ServiceAccountID`.

   **WrapTxForBootstrap.** Added
   `pkg/fcsdk/usecasepgx.WrapTxForBootstrap(pgx.Tx) *DbTx` for
   infrastructure-bootstrap callers (init, seeders, admin tools) to
   reuse the sqlc-backed repos without going through the use-case
   envelope. Documented as bootstrap-only — production paths must
   still use `Commit/CommitDelete/CommitAll/EmitEvent`.

   **OAuthClient entity follow-up** (newly surfaced by the sqlc sweep):
   extend the entity with `PostLogoutRedirectURIs`, `AllowedOrigins`,
   `ApplicationIDs`, `PKCERequired`; wire the three corresponding
   junction tables. The repo currently hardcodes `pkce_required=true`
   and silently drops the other three concepts. Once the entity gains
   the fields, also extend create/update commands + API DTOs.
   Init currently uses a direct INSERT to `oauth_client_application_ids`
   as a workaround — the bootstrap is the one place that needs this
   linkage today.

6. ~~**Schema reconciliation for `dispatchjob`**~~ **Done.** Entity +
   repo reconciled against the post-019 partitioned schema, repo
   migrated to sqlc, and Rust's fanout pattern-matching ported into
   `internal/stream/fan_out.go` (`CachedSubscription` + wildcard
   matcher + dispatch-job assembly + per-cycle insert in the same tx
   as the `fanned_out_at` stamp).

   **End-to-end smoke verified on fresh embedded PG.** Created a
   subscription with pattern `test:demo:order:created`, posted a
   matching event via `/api/events/batch`, watched the fanout
   projector produce a `msg_dispatch_jobs` row with the correct
   payload (raw event data, `dataOnly=true`), `idempotencyKey`
   (`{eventId}:{subscriptionId}`), and inherited `mode=IMMEDIATE`
   from the subscription. The scheduler poller then claimed it
   (status PENDING → QUEUED).

   **Two latent bugs caught + fixed during the port:**
   1. **`msg_dispatch_jobs.id` is `VARCHAR(13)`** — Rust mints typed
      IDs (`djb_<13>` = 17 chars) which overflow the column. The Go
      port now uses `tsid.GenerateUntyped()` (13 chars) in both fanout
      (`internal/stream/fan_out.go`) and the SDK batch path
      (`internal/platform/shared/sdk/dispatch_jobs_batch.go`). Same
      latent bug exists upstream in Rust.
   2. **`metadata` JSONB column shape** — Rust stores `[{key,value}]`
      arrays; Go was marshalling `map[string]string` as `{k:v}`
      objects, breaking JSONB drop-in parity with consumer SDKs.
      Entity Metadata is now `[]Metadata{Key,Value}` end-to-end.

   **Known residual: subscription cache TTL race window.** The fanout
   subscription cache refreshes every 5s (matches Rust). Events
   ingested in the gap between a subscription being created and the
   next cache refresh will be claimed by the no-subs fast path and
   stamped `fanned_out_at` without producing jobs. Mitigation:
   producer apps shouldn't immediately follow `POST /api/subscriptions`
   with the first event. For tests, wait >5s before publishing. To
   tighten further: drop the TTL or refresh on every cycle.

7. ~~**Router gaps — loop-correctness pieces**~~ **Done.** RouterError,
   HealthService, WarningService, LifecycleManager all ported and
   wired into `internal/router/server.go`. 13 unit tests pass. See
   §4 #7 for the field-level summary. Manager-coupled lifecycle
   tasks (memory health, consumer restart, reaper) defer until the
   Go Manager grows the supporting surface. Next router work is the
   HTTP route surface (40+ endpoints under `/monitoring/*`,
   `/warnings/*`, `/config/*`) + Prometheus metrics.

8. ~~**Contract harness**~~ **Done (framework).** See §6 #3 for
   detail. To use it for the actual drop-in proof: bring up the Rust
   `fc-platform-server` (or whichever binary serves the API) on one
   port, bring up Go `fc-dev` on another, point the harness at both.
   Cases turn from SKIP → PASS as you supply auth (`ANCHOR_TOKEN` env)
   and as the YAML library grows. Wire into CI once both binaries can
   be brought up in the GitHub Actions runner (currently only Postgres
   is — Rust binary needs publishing).

9. ~~**Argon2id PHC salt**~~ **Done.** See §4 #4.

10. ~~**Frontend port**~~ **Done.** See §4 #14.

## 8. Conventions Cheat Sheet

- **Event types** follow `application:subdomain:aggregate:event` with
  hyphens (not underscores) inside segments. **Don't use underscores.**
  `platform:iam:user:roles-assigned` ✅
  `platform:iam:user:roles_assigned` ❌

- **Aggregate names with no hyphens** in the event-type catalog —
  e.g. `serviceaccount` (not `service-account`), `eventtype` (not
  `event-type`). HTTP routes use the hyphenated form
  (`/api/service-accounts`) but event-type codes don't.

- **Field naming.** `Type` (Go field) → `type` (JSON) is fine.
  But aggregate IDs use the aggregate's own field name in the payload
  — e.g. `principalId` (not `userId`) for user events, except where
  Rust uses `userId` (application_access_assigned) — see
  `principal/operations/events.go` for the deliberate divergence.

- **Source field on event metadata** always equals
  `application:subdomain` (e.g. `platform:iam`). Confusingly the
  earlier Go impl used `platform:admin` for IAM events — that was a
  bug, fixed during this port.

- **Per-aggregate code structure**:
  ```
  internal/platform/<aggregate>/
    entity.go              — aggregate root + sub-entities
    repository.go          — pgx-backed repo, implements Persist[T]
    operations/
      events.go            — every DomainEvent the subdomain emits
      create.go / update.go / etc. — one file per verb
    api/
      api.go               — chi routes, State struct, handlers
  ```

- **TSID prefixes** are pinned in `internal/tsid/tsid.go`. Don't
  reuse prefixes across entity types. Adding a new entity type means
  adding a new EntityType const + a new prefix in `Prefix()`.

## 9. Where the rough edges are

- The `usecasepgx.UnitOfWork.WithTx` shape doesn't quite match what
  the sync ops want — see the comment in
  `eventtype/operations/sync.go` about "TODO(sync-runtime): batch into
  one tx." A small refactor on the UoW would close that.

- `Scheduler.Run(ctx)` is naive — it doesn't surface the dispatcher's
  health, doesn't expose metrics, doesn't co-ordinate shutdown with
  in-flight messages. Adequate for now, not adequate for prod.

- ~~`cmd/fc-router/main.go` is monolithic — can't be imported by
  fc-server today.~~ Done — wiring lives in `internal/router/server.go`
  (`Server.Run(ctx)`). The cmd binary now only contributes signal
  handling + the `/health` `/ready` `/metrics` HTTP surface, and
  fc-server's `StartRouter` calls the same `Run`.

- The Go side has tests for the seed catalog + auth provider helpers,
  but **no integration tests against a real PG**. Every wired endpoint
  is technically untested until an integration harness lands.

## 10. Asking the Right Questions

If you're stuck and need to ask the user something, these are the
already-decided answers (don't re-ask):

- **Existing JWT compatibility:** not required. New tokens issued post-cutover.
- **Library choices:** fosite (OAuth), coreos/go-oidc (IDP bridge),
  go-webauthn (passkeys), pgx/v5 (Postgres), chi (router),
  robfig/cron/v3 (cron), fergusstrange/embedded-postgres (dev PG).
  All locked in.
- **Repo layout:** single Go module at github.com/flowcatalyst/flowcatalyst-go.
- **API parity:** byte-identical. No URL changes, no JSON shape changes.
- **Database:** same Postgres, same migrations, no schema rewrites.

If something doesn't match this contract, **fix the Go side, not the
contract.**
