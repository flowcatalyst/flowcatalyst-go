# FlowCatalyst Go — Rust Parity Remediation Plan

_Created 2026-05-29. Source: full read-only parity audit (Go `flowcatalyst-go` vs Rust reference `flowcatalyst-rust`). This plan tracks closing the behavioural/operational gaps found in that audit._

## Progress & Handover (updated 2026-05-30)

**Branch:** `parity-remediation` (off `main`). **Build:** `go build ./...` clean. **Tests:** every touched suite green.

### Status by phase
- ✅ **Phase 0 — verify (V1–V4):** all confirmed. V1 config wire-shape (`queueName`/`queueUri`), V2 permission-lockout (real, critical), V3 outbox schema mismatch vs SDK, V4 WebAuthn blob-format divergence.
- ✅ **Phase 1 — drop-in schema & wire (S1–S5):** `ccd5f93`. Postgres `queue_messages` schema, config `queueName`/`queueUri` (+`name`/`uri` aliases), outbox SDK schema + delete-on-success, OAuth secret `encrypted:` prefix, migration idempotency audit + `tools/baseline-goose-ledger.sql`.
- ✅ **Phase 2 — OIDC (O1/O2/O4/O5/O6):** `9889a77`. end_session/RP-logout (+persisted post_logout_redirect_uris), `POST /auth/refresh`, in-memory token governor + RFC-6749 `rate_limit_exceeded` 429, `max_age` (in-flight), `GET /auth/check-domain`. Folded in the in-flight governor work. **Remaining → #13** (O3 `?provider=`, O7 document `/auth/*` in spec).
- ✅ **Phase 3 — message router (R1–R8): COMPLETE.** `a16c927`, `3d00f02`, `2545ba8`, `e346d3e`. IMMEDIATE concurrency + capacity backpressure, route-by-`poolCode` + DEFAULT-POOL (topology rewrite: consumers decoupled from passive pools), external-requeue dedup, failure-rate circuit breaker, multi-URL config-sync + retry + first-wins merge, stalled-consumer auto-restart, Rust-aligned Prometheus metrics (real `fc_mediation_duration_seconds` histogram). **Hardening follow-up** `3c3ec7a`: breaker ownership moved into the mediator (one place records success/failure; open-breaker short-circuits via new `common.MediationCircuitOpen`; 4xx records SUCCESS); `processOne` panic isolation (recover→NACK 10s, worker survives, no double-resolve); mediator config-class warnings (400/401/403/404→Error, 501→Critical) on `/warnings`+health via `SetWarnings`; in-flight memory-health warning (>10k entries, RESOURCE category) on the reaper tick; `ConfigPollInterval` default 30s→300s; new `guardrail_test.go` (`-race`). **Hardening #2** `ae36e04` (authored by a parallel agent, reviewed + committed): `Manager.SetWarnings` surfaces ROUTING (unknown `pool_code`→DEFAULT-POOL, 1:1 with Rust `group_by_pool`) + POOL_CAPACITY (all-pools-full consumer pause) warnings. NOTE: POOL_CAPACITY is a **Go-side enhancement** — Rust only debug-logs that pause; the capacity-pause path is not unit-tested (the ROUTING path is).
- 🟡 **Phase 4 — IAM/authz (A1/A3/A4a done):** `6cb1539`, `8e2bdc2`. A1 permission wildcard matcher + real 4-segment strings (THE critical lockout fix), A3 connection mutations anchor-only (was zero authz), A4a WebAuthn delete ownership. **Remaining → #15** (A2 scope-isolation sweep, A4b passkey blob convert-on-read, A5 password-reset flow build-out).
- ✅ **Phase 5 — SDK `/sync` self-registration: COMPLETE.** `a4bf0fb`, `bb1936d`, `c8a6295`, `aa1cf67`, `4478579`, `2194679`, `5c2f072`. New `internal/platform/sdksync` package mounts all 8 `POST /api/applications/{appCode}/{resource}/sync` endpoints (roles, event-types, subscriptions, dispatch-pools, principals, processes, scheduled-jobs, openapi) at byte-parity with Rust `sdk_sync_api.rs` (shared `SyncResultResponse {applicationCode,created,updated,deleted,syncedCodes}`; scheduled-jobs + openapi have their distinct shapes). Built 6 new Sync use-cases (roles/subscriptions/dispatch-pools/principals/processes/scheduled-jobs) + reused event-types/openapi; added per-resource `*Synced` rollup events + the `can_sync_*` authz helpers (full Rust permission sets incl. application-service grants) + the SDK permission constants. Each handler resolves `{appCode}`→app (404 unknown), checks the sync permission, and (openapi/scheduled-jobs) enforces the resource-level guard (own service-account / target-client / anchor). Key parity nuances mirrored: role names `{appCode}:{name.lower()}` + SDK-source-only mutation + ROLE_HAS_ASSIGNMENTS refusal; dispatch-pools global + archive-on-prune + code regex; subscriptions `mode` accepted-but-not-applied; principals SDK_SYNC role merge + strip-on-unlisted; scheduled-jobs change-detection + re-activate + ID-array result.
- ✅ **Phase 6 — dispatch + cron scheduler (SC1–SC9): COMPLETE.** `e59f6a3`, `e29ab91`, `43b4be6`. The scheduled-job scheduler was dead code (zero callers, stale instance schema) → now a wired Rust-parity two-loop engine: poller (skip-to-latest `LatestSlotInWindow` over (last_fired ?? created_at, now], QUEUED instance insert, monotonic `MarkFired` via GREATEST) + dispatcher (QUEUED→IN_FLIGHT→POST snake_case WebhookEnvelope→202 contract: DELIVERED, or requeue→DELIVERY_FAILED at delivery_max_attempts). FireNow inserts a MANUAL instance (two-phase) + accepts correlationId + allows PAUSED. Cron-shape validation 5–7 fields at create/update; firing parser is 6-field seconds-first (cron-crate parity). FC_SCHEDULED_JOB_* config + FC_SCHEDULED_JOB_ENABLED toggle, wired in run.go. Leader-gated via a dedicated Redis election (subsystem-suffixed lock key); the dispatch-job scheduler stays un-gated (already SKIP-LOCKED-safe). API gaps closed: hasActiveInstance, clientId=platform→NULL filter, fireNow correlationId body (202 + instanceId already present). Added InstanceRepository Insert/MarkInFlight/MarkDelivered/MarkDeliveryFailed.
- ✅ **Phase 7 — stream processor (ST1–ST5): COMPLETE.** `9ab1699`. ST1 event projection preserves source `created_at` (partition-key) into `msg_events_read` (was projection-time). ST2 dispatch projection populates `is_terminal` (COMPLETED/FAILED/CANCELLED/EXPIRED) + fixes `is_completed` to `status=COMPLETED` (prior code conflated them + matched bogus SUCCESS/IGNORED). ST3 partition manager leader-gated (projections stay SKIP-LOCKED multi-replica — un-gated by design). ST4 partition manager now drops expired partitions (90d retention via pg_inherits + YYYY_MM parse) + `is_partitioned` guard (drop-in over non-partitioned schema skipped) + daily tick + CREATE…IF NOT EXISTS. ST5 per-projection batch env overrides + partition tuning knobs + toggle renamed `FC_STREAM_PARTITION_MANAGER_ENABLED` (old alias kept).
- ✅ **Phase 8 — outbox processor (OB1–OB8): COMPLETE (core), 2 refinements tracked.** `9dc2b7f`, `f9d6f40`. OB1 MongoDB backend (`internal/outbox/mongo`, SDK-exact wire shape: int status, string payload/timestamps; find+update_many claim; index init) + `FC_OUTBOX_BACKEND=mongo` selection. OB2 crash recovery (`RecoverStuck` resets stuck IN_PROGRESS→PENDING; pg+mongo; processor recovery ticker). OB3 leader-gated processor (`Processor.IsLeader` via newLeaderGate; the non-atomic Mongo claim needs single-active). OB5 (correctness) the dispatcher now parses the per-item `{results:[{id,status,error}]}` body on a 2xx instead of blanket-success (a 2xx batch with per-item failures was marking all SUCCESS = data loss). OB6 max-retries cap (`MarkFailed(requeue)`; stop re-queue once attempt #(retry_count+1) ≥ MaxRetries=3). OB8 env aliases (`FC_OUTBOX_API_URL`/`FC_OUTBOX_TOKEN`/`FC_OUTBOX_DB_URL`). **Tracked follow-up:** OB4 true multi-item batching + the stateful per-group block-on-error model + OB7 explicit max-concurrent-groups semaphore — deferred (the GroupDistributor already guarantees per-group FIFO + concurrent distinct groups, and `MaxInFlight` backpressure already bounds total in-flight, so this is a refinement, not a correctness gap).
- ✅ **Phase 9 — MCP server (M1–M8): COMPLETE.** `34c9e38`. Rewrote `internal/mcp` on the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk` v1.6.1 — the Go analog of Rust `rmcp`), replacing the hand-rolled HTTP-only 1-tool scaffold. M1/M3/M4: SDK initialize+capabilities; all 12 Rust tools (list/get event-types, get_schema w/ CURRENT→FINALISING fallback, list/get subscriptions, list_applications, list/get roles, get_openapi two-hop, whoami, list_my_applications, get_application_capabilities bundle tolerating 404s) + resources (5 fixed collections + 5 hierarchical templates). M5: OAuth2 client_credentials TokenManager (form POST /oauth/token, in-memory cache, refresh 60s pre-expiry) wired as the client TokenProvider. M7: JSON output fixed to pretty JSON (was `%v` map syntax); config env→`~/.cache/flowcatalyst-dev/mcp-credentials.json`→localhost default; bind 127.0.0.1 (`FC_MCP_BIND`), **port kept 8090** (micro-decision #1 resolved). M2: stdio (default for `fc-dev mcp`) + streamable-HTTP at `/mcp`. M6: `fc-dev start` idempotently provisions the local MCP OAuth client (`flowcatalyst-mcp-local`) + super-admin SA + 0600 creds file, and persists a stable `FLOWCATALYST_APP_KEY`. M8: docs de-staled (architecture.md/README.md), stale `fc-mcp-server` binary removed, unused client-id flag dropped. Unit tests cover token cache/config/JSON-output/schema-fallback. **Platform gap (not MCP):** `GET /api/me/applications` is unimplemented in the Go platform → `list_my_applications` 404s until it lands.
- ✅ **Phase 10 — ops surface (P1–P5): COMPLETE.** `fd8a08b`. P5 port: `internal/config` HTTP default 3000→8080 (single canonical default; live `envcfg` already 8080) + test + stale doc refs. P4 aliases: outbox URL/token chains accept `FC_API_BASE_URL`/`FC_API_TOKEN`; router BasicAuth accepts `AUTH_BASIC_USERNAME`/`AUTH_BASIC_PASSWORD` + `AUTH_MODE=NONE` (`resolveRouterAuth`). P1 Secrets Manager: `internal/server/dbsecret.go` resolves the DSN from an AWS SM secret on `DB_SECRET_ARN`+`DB_HOST` (precedence full-URL > SM > explicit creds; 1:1 with Rust `AwsSecretProvider`), wired in `cmd/fc-server`, `secretsmanager` SDK added, unit-tested. **Rotation poller DONE** (`25b5cc0`: `DBSecretRefresher` polls `DB_SECRET_REFRESH_INTERVAL_MS`, default 5m, and injects rotated creds via a pgxpool `BeforeConnect` hook — no restart needed). P2 ALB: `FC_ALB_*` env → `TrafficConfig` plumbed through `newRouterServer` (the ELBv2 register/deregister strategy already existed in `internal/router/traffic.go`, just unfed). P3 Docker: multi-stage `Dockerfile` (node→pnpm SPA → static Go embedding dist+migrations → alpine runtime w/ wget HEALTHCHECK, non-root) + `.dockerignore` + `docker-compose.yml` (postgres+redis healthchecks + fc-server). **fc-server now serves the embedded SPA** when built in (parity with Rust; `frontend.IsAvailable` gate). **VERIFIED (`docker build` + `docker compose up`):** image builds end-to-end (pnpm SPA → embed → Go), boots against Postgres (migrations + seed run), serves /health 200 + SPA root 200 text/html + /api 403-unauth + metrics 200, container Healthy. The runtime test caught + fixed a healthcheck bug (`7441831`: `wget --spider` HEADs a GET-only /health → 405; switched to a GET probe).

### Commits on `parity-remediation`
`ccd5f93` P1 · `9889a77` P2 · `a16c927` P3-R1 · `3d00f02` P3-R2/R4 · `2545ba8` P3-R3/R5 · `e346d3e` P3-R7/R8 · `6cb1539` P4-A1 · `8e2bdc2` P4-A3/A4a · `a4bf0fb` P5-eventtypes · `bb1936d` P5-roles · `c8a6295` P5-openapi · `aa1cf67` P5-processes+pools · `4478579` P5-subscriptions · `2194679` P5-principals · `5c2f072` P5-scheduledjobs · `e59f6a3` P6-cron-core · `e29ab91` P6-poller/dispatcher · `43b4be6` P6-api-gaps · `9ab1699` P7-stream · `9dc2b7f` P8-mongo/leader/env · `f9d6f40` P8-results/retries/recovery · `3c3ec7a` P3-hardening (breaker→mediator/panic-isolation/config-warnings) · `a2d1248` fc-dev embedded-PG cache dir · `34c9e38` P9-MCP (official go-sdk, 12 tools, resources, OAuth, fc-dev bootstrap) · `ae36e04` P3-hardening#2 (router ROUTING + POOL_CAPACITY warnings) · `fd8a08b` P10-ops (Secrets Manager DB mode, ALB self-reg, Docker, env aliases, port 8080, fc-server SPA) · `937ee4d` FU /api/me/applications · `d09acd7` FU #15-A5 password-reset · `1f5bde0` FU #15-A2 scope sweep · `7441831` FU docker healthcheck fix (verified) · `773391c` FU StartMCP creds-file fallback (verified) · `f991de6` FU #15 principal-change moot · `bf5b844` FU #15-A4b legacy-passkey safety · `2683bdc` FU #15-A4b legacy non-blocking · `25b5cc0` FU P10 DB-secret rotation poller · `2a96231` FU #16 outbox block-on-error + OB7 · (docs) #13-O7 endpoint inventory

### Open tracked follow-ups
- **#13** — ✅ **O7 DONE** (documented the chi-mounted `/auth/*` + `/api/me*` + OAuth/OIDC endpoint inventory in `docs/api-parity.md` §Authentication; these are intentionally absent from the huma-generated, CI-gated `api/openapi.lock.json` — hand-editing it would fail `make api-diff`, so a markdown reference is the correct form). **O3 deferred (niche):** the `?provider=` direct-IDP branch returns a clean `server_error` redirect today (correct, non-breaking); full support needs new bridge callback-resume logic + IdP-by-id resolution + a real upstream IdP to verify — not worth building blind for the chained third-party-app-pre-selects-IdP case. Already documented as a known gap in `docs/api-parity.md`.
- **#15** — ✅ A2 DONE (`1f5bde0`: `auth.CheckScopeAccess`/`CanAccessScope` + `requireScopeByID` on 21 by-id mutations across subscription/dispatchpool/eventtype/scheduledjob/principal + 4 dispatchjob read leaks). ✅ A5 DONE (`d09acd7`: `/auth/password-reset/{request,validate,confirm}`, hex-SHA-256 tokens). **A4b PARTIAL** (`bf5b844`): the **silent-failure footgun is closed** — a legacy `webauthn-rs` Passkey blob unmarshals into go-webauthn's Credential with no error but empty id/publicKey; `rowToCredential` now detects it (`isLegacyRustPasskey`) and fails loudly ("re-register this passkey"). **Full convert-on-read deferred** (COSE-key re-encoding is security crypto needing a real webauthn-rs sample to validate; a wrong-but-parseable key can't be safely guarded — and per micro-decision #3 it's conditional on V4 finding prod passkeys). The principal-update **scope/client_id-change** anchor restriction is **moot in Go** (`f991de6`: `UpdatePrincipalRequest`/`UpdateCommand` don't expose scope/client_id).

### Resume notes / handover
- **Recommended next:** all 10 phases + the follow-ups are done. **#16 OB4-block-on-error + OB7** (`2a96231`: GroupDistributor stops a group on a failing item, `Repository.Release`-ing the rest to re-run in order behind it; max-concurrent-groups semaphore) and the **DB-secret rotation poller** (`25b5cc0`: `DBSecretRefresher` + pgxpool `BeforeConnect`) are now CLOSED + tested. **Remaining (all explicitly deferred, low-priority):** #16 true multi-item HTTP batching (pure throughput — dispatcher still 1 item/call) + the operational pause/unblock/skip state machine (needs an admin API); **O3** `?provider=` (niche, clean error today, untestable without a real IdP); **A4b** full passkey convert-on-read (owner: fresh-only — legacy passkeys are now non-blocking, `2683bdc`); Phase 5/6 minor nits. No net-new phases, no open correctness gaps.
- **Phase 9 follow-ups (minor, not blocking):** (1) ✅ `GET /api/me/applications` implemented (`937ee4d`, `internal/platform/shared/me`) — `list_my_applications` works now. (2) ✅ MCP bootstrap + auth chain VERIFIED live (`fc-dev start --mcp`): the M6 bootstrap wrote the creds file (OS cache dir, 0600), client_credentials minted a valid token, and authenticated `/api/event-types` (200) + `/api/me` (SERVICE/ANCHOR/super-admin) worked — the exact path the MCP tools use. Fixed `773391c`: `StartMCP` now falls back to the bootstrapped creds file (was env-only → in-process tools would 403). (Full MCP-protocol stdio/SSE handshake still relies on the SDK + unit tests, not a live client.) (3) The bootstrap is idempotent by client existence — if the creds file is deleted but the DB client remains, it won't regenerate (matches Rust); `fc-dev fresh` recreates both.
- **Phase 8 / #16 follow-up:** ✅ **OB4 block-on-error + OB7 DONE** (`2a96231`): the GroupDistributor stops a message group on a failing item and releases the rest (`Repository.Release`, pg+mongo, sqlite stub) to re-run in order behind it (closing the real ordering gap — previously a later group item could be delivered before an earlier failed one was retried); a max-concurrent-groups semaphore caps group fan-out (`FC_OUTBOX_MAX_CONCURRENT_GROUPS`/`FC_MAX_CONCURRENT_GROUPS`, default 10); `BlockOnError` default true. **Deferred (documented):** true multi-item HTTP batching (the dispatcher still sends 1 item/call — pure throughput) + the operational pause/resume/unblock/skip state machine from Rust `message_group_processor.rs` (a 565-line in-memory per-group actor that needs an admin API to drive; doesn't fit Go's DB-claim poll model). No correctness gap remains.
- **Phase 5 follow-ups (minor, not blocking):** subscription event-type-binding `filter` is set in-memory by the sync use-case but the existing subscription repo `Persist` doesn't write it to `msg_subscription_event_types` (pre-existing Go gap shared by all subscription writes — fix in the persistence/sqlc layer, not sdksync). The Go sync use-cases emit per-row domain events (matching the existing Go `SyncEventTypes` convention); the Rust sync use-cases emit only the rollup — internal/audit-only divergence, no wire impact.
- **Phase 6 follow-ups (minor, not blocking):** (1) the job `Concurrent` flag is not yet gated at fire time — the poller inserts a slot unconditionally (mirrors the quoted Rust poller body); enforcing non-concurrent runs via `HasActiveInstance` needs verification against the full Rust poller. (2) 7-field (year) cron expressions pass `ValidateCronShape` but `robfig/cron` can't parse a year field, so they never fire (5-field also never fires, matching the Rust cron crate's seconds-required rule); only 6-field seconds-first crons fire — verify the populated-DB crons are 6-field. (3) the scheduled-job leader election uses its own lock key (`<StandbyLockKey>:scheduled-job`), so it elects independently of the router's election rather than co-locating on one shared lease as Rust does — single-runner is still guaranteed.
- **Verify-before-deploy decisions:** run `tools/baseline-goose-ledger.sql` before first Go boot against an existing migrated DB (goose ledger baseline; owner accepted msg_events recreation otherwise).
- **Intentionally uncommitted:** `.claude/settings.json` (read-only Bash allowlist added this session) and `HANDOFF.md` (a separate, pre-existing in-flight working doc — not part of this effort).
- **Parity method:** Rust reference at `~/Developer/flowcatalyst-rust`; for platform-API shapes diff the OpenAPI specs (`api/openapi.lock.json` vs `frontend/openapi/openapi.json`), not source. Behavioural parity (OIDC/router/crypto/etc.) is verified against Rust source 1:1.
- **Re-run gate before any commit:** `go build ./...` + `go test ./internal/...` + `gofmt -l` on touched files.

## Decisions baked in (from project owner)

- **Port:** `8080` is the canonical default. We do **not** chase Rust's `3000`; instead fix Go's internal inconsistency and the docs.
- **Binaries:** keep the single `fc-server` binary with `FC_*_ENABLED` toggles. No standalone service binaries required.
- **Compatibility:** Go is a **replacement that must drop into existing, populated databases** without breaking existing systems that use the SDK / public APIs. External contracts — SDK, public APIs, config wire-shapes (`queueName`/`queueUri`), and any **shared DB/queue/outbox schema** — MUST stay interoperable. BFF/internal shapes (casing, list wrappers) may deviate. Go migrations must be safe to apply over an already-provisioned upstream schema.
- **Backends:** router stays SQS/NATS/Postgres; outbox = Postgres **+ add MongoDB**. SQLite/ActiveMQ/MySQL are out of scope.
- **Ops:** in scope now — AWS Secrets Manager DB mode, ALB self-registration, Docker/compose.

## Guiding constraints (every phase)

1. **Drop-in safety is the prime directive.** Anything that reads/writes a *shared* table, a *public/SDK* API, or a *config wire payload* must be byte/shape-compatible with the running Rust system. Internal/BFF shapes are free to differ.
2. **Migrations must be idempotent + additive + guarded** — no-op cleanly on an already-populated upstream DB; no destructive `ALTER`/`DROP` on shared tables.
3. **Crypto outputs must remain cross-readable** (already true; one prefix fix pending).
4. Every behavioural fix gets a **golden parity test** (extend `parityharness`).

---

## Phase 0 — De-risk & verify `[S]`

| ID | Task | Why |
|---|---|---|
| V1 | Confirm router config wire field names (`queueName`/`queueUri` vs `name`/`uri`) and exact shape. | Owner confirmed it MUST be interoperable → definite fix (S2); nail the exact shape/aliases. |
| V2 | Confirm whether the permission-string mismatch (`"READ_EVENT_TYPES"` vs `platform:messaging:event-type:view`) is a live lockout for non-anchor principals. | If real, Phase 4 becomes urgent. |
| V3 | Diff Go outbox schema/queries vs SDK `clients/typescript-sdk/migrations/postgresql/001_create_outbox_messages.sql`. | Confirms exact column contract for S3. |
| V4 | Audit existing WebAuthn credential blob format in a populated DB. | Determines whether drop-in locks out existing passkey users (A4b). |

## Phase 1 — Drop-in schema & wire compatibility `[L]` (FOUNDATIONAL)

| ID | Task | Target | Source of truth |
|---|---|---|---|
| S1 | Postgres queue table → match upstream `queue_messages` (`visible_at BIGINT`, batch receipt handle, `message_group_id` index). | `internal/queue/postgres` | Rust `postgres.rs:35-58` |
| S2 | Config wire-shape → accept `queueName`/`queueUri` (+ `name`/`uri` aliases). | `internal/router/config.go`, `config_sync.go` | Rust `config_sync.rs:97-117` |
| S3 | Outbox table → SDK customer schema (`type`, `payload TEXT`, `retry_count`, `error_message`, `client_id`, `payload_size`, `headers`); delete-on-success. | `internal/outbox/postgres` | SDK migration + Rust `postgres.rs:336-356` |
| S4 | Migration drop-in audit: idempotent/guarded; reconcile Go sqlc column expectations against upstream schema; decide migration-ledger strategy. | `internal/migrate/sql`, `internal/sqlc` | Rust `migrations/` |
| S5 | OAuth secret stored-string: prepend `"encrypted:"` on write; fix stale "Argon2" comment. | `auth/operations/oauth_client.go`, `auth/repository.go:45` | Rust `oauth_clients_api.rs:251` |

## Phase 2 — OIDC finish `[M]` (priority #1)

- **O1** `end_session_endpoint` + persist `post_logout_redirect_uris`. (Rust `oidc_login_api.rs:1491`)
- **O2** `POST /auth/refresh`. (Rust `auth_api.rs:539`)
- **O3** `?provider=` direct-IDP authorize branch. (Rust `oauth_api.rs:504`)
- **O4** in-memory per-client governor on `/oauth/token` + RFC-6749 429 body. (Rust `oauth_api.rs:791`)
- **O5** enforce `max_age` (expose `iat` to session validation). (Rust `oauth_api.rs:425`)
- **O6** `GET /auth/check-domain` query variant. (Rust `auth_api.rs:424`)
- **O7** document `/auth/*` in `api/openapi.lock.json`.

## Phase 3 — Message router behavioural parity `[L]` (priority #3)

- **R1** branch `ProcessPool.submit` on `DispatchMode` (IMMEDIATE → concurrent). (Rust `pool.rs:332`)
- **R2** route by `message.pool_code` + DEFAULT-POOL fallback. (Rust `manager.rs:1095`)
- **R3** failure-rate circuit breaker. (Rust `circuit_breaker_registry.rs:136`)
- **R4** external-requeue dedup. (Rust `manager.rs:1042`)
- **R5** config-sync multi-URL + retry. (Rust `config_sync.rs:193-301`)
- **R7** stalled-consumer auto-restart. (Rust `lifecycle.rs:186`)
- **R8** align Prometheus metric names/labels. (Rust `router_metrics.rs`)

_(R6/R10 handled in Phase 1; R9 out of scope.)_

## Phase 4 — IAM / authz correctness & security `[M]` (urgency set by V2)

- **A1** permission resolution + wildcard matcher (if V2 confirms). (`shared/auth/auth.go`)
- **A2** per-resource scope checks on by-ID mutations (systemic sweep). (Rust `check_scope_access`)
- **A3** authorization on connection update/delete. (`connection/api/api.go:182,195`)
- **A4a** WebAuthn credential delete ownership check. (`webauthn/api/api.go:338`)
- **A4b** WebAuthn credential-blob compatibility (if V4 confirms).
- **A5** password-reset flow with hex SHA-256 token hashing. (Rust `password_reset_api.rs:157`)

## Phase 5 — SDK `/sync` self-registration contract `[L]`

Implement 8 application-scoped sync endpoints + 6 missing use-cases (subscriptions, dispatch-pools, processes, scheduled-jobs, principals; app-scoped roles sync). Match Rust payloads. (Rust `shared/sdk_sync_api.rs:881`)

## Phase 6 — Dispatch + cron scheduler `[L]`

- **SC1** wire up cron scheduler (zero callers today).
- **SC2** fix `fire()` to write real instance columns.
- **SC3** dispatcher/retry engine (poller→dispatcher, IN_FLIGHT→DELIVERED→requeue, 202 contract). (Rust `dispatcher.rs`)
- **SC4** run-now inserts an instance. (Rust `fire_now.rs:101`)
- **SC5** cron syntax 6–7 field + validation.
- **SC6** skip-to-latest downtime semantics; monotonic `mark_fired`.
- **SC7** leader-gate cron + dispatch-job schedulers.
- **SC8** `FC_SCHEDULED_JOB_*` / `FC_SCHEDULER_*` config.
- **SC9** API field gaps (`hasActiveInstance`, `clientId="platform"`, FireNow shape, `correlationId`).

## Phase 7 — Stream processor `[M]`

- **ST1** preserve source `created_at` into read-model. (`events.go:72`)
- **ST2** populate `is_terminal`.
- **ST3** leader-gate projections.
- **ST4** partition retention/drop + `is_partitioned` guard + window/cadence. (Rust `partition_manager.rs:229`)
- **ST5** per-projection batch sizes + env knobs; rename toggle to `FC_STREAM_PARTITION_MANAGER_ENABLED`.

## Phase 8 — Outbox processor `[L]` (schema in Phase 1)

- **OB1** add MongoDB backend. (Rust `mongo.rs`)
- **OB2** crash recovery. **OB3** leader-gating.
- **OB4** API batching. **OB5** per-item 2xx `{results:[]}` response handling.
- **OB6** max-retries cap + group-blocking. **OB7** bounded concurrent groups.
- **OB8** env-var alignment/aliases.

## Phase 9 — MCP server `[L]`

- **M1** MCP library + `initialize` handshake. **M2** stdio transport.
- **M3** remaining 12 tools. **M4** resources.
- **M5** OAuth client-credentials + token cache. **M6** `fc-dev` credential bootstrap.
- **M7** JSON output fix, localhost bind, default port. **M8** remove stale artifact + fix docs.

## Phase 10 — Ops surface `[M]`

- **P1** AWS Secrets Manager DB mode. **P2** ALB self-registration.
- **P3** Dockerfile + docker-compose (+ healthchecks).
- **P4** env-var alias layer for drop-in (router `AUTH_MODE`/`AUTH_BASIC_*`, outbox `FC_API_BASE_URL`/`FC_API_TOKEN`/`FC_OUTBOX_DB_URL`, …).
- **P5** port canonicalization to 8080 (`internal/config/config.go`); README/docs staleness.

## Cross-cutting

- **Shared leader-gating helper** reused by SC7 / ST3 / OB3.
- **Parity test harness** (extend `parityharness`) with golden assertions.

## Open micro-decisions

1. ~~MCP default port — keep `8090` or match Rust `3100`.~~ **RESOLVED (Phase 9): keep `8090`** (consistent with the "keep Go's 8080, don't chase Rust" decision), bind localhost by default.
2. ~~Env-var aliasing (P4) — accept Rust names as aliases (lean: yes).~~ **RESOLVED (Phase 10): yes** — accepted Rust/TS names as aliases (AUTH_BASIC_*, FC_API_BASE_URL/FC_API_TOKEN, DB_*, etc.).
3. WebAuthn blobs (A4b) — convert-on-read vs migration (only if V4 finds existing passkeys).

## Recommended sequence

Phase 0 → Phase 1 (prerequisites for safe deploy). Then parallel tracks: Phase 2 (OIDC) + Phase 3 (router) + Phase 4 (IAM/security). Then 5 → 6 → 7 → 8 → 9, with Phase 10 + the test harness alongside.
