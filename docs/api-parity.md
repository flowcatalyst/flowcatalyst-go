# API Parity Strategy

The Go server must serve responses byte-compatible with the Rust server so existing consumers (frontend, TypeScript SDK, Laravel SDK, webhook subscribers) continue working without modification.

This is the single most important constraint of the rewrite. It's the difference between a drop-in replacement and a fork.

---

## What "compatible" means concretely

Two responses are **compatible** if and only if:

1. **HTTP status codes match.** Same outcome → same status.
2. **Response headers match for the headers that consumers depend on**: `Content-Type`, `Cache-Control`, `WWW-Authenticate`, `Location`, `X-Fc-*` headers. Other headers (`Date`, `Server`, `Content-Length`) may differ.
3. **JSON bodies are structurally equal.** Same set of keys at every level. Same value types. Same enum string values. Same nullability posture (`null` vs missing must match Rust). Field order does NOT matter (JSON objects are unordered).
4. **Floating-point and integer representation matches.** Timestamps in RFC3339 with microsecond precision (`2026-05-24T08:30:00.123456Z`, not `...123Z`). Numbers serialized without `.0` suffix where they're integers in the schema.
5. **For binary/HMAC content** (webhook signatures): byte-equality.

---

## What this rules out

These changes look harmless and are **not** allowed during the rewrite:

- **Renaming a JSON field.** `eventTypeId` → `event_type_id`: no.
- **Changing an enum's string value.** `"CURRENT"` → `"current"`: no.
- **Changing nullable to required (or vice versa).**
- **Returning a field that Rust omits, or omitting a field Rust returns.** Even when the value is null. Even when "logically equivalent".
- **Changing status code for an error case.** 422 → 400: no.
- **Returning the updated entity from PUT when Rust returns 204 No Content.** The frontend treats `Promise<void>` differently from `Promise<Entity>`. See [conventions §7](./conventions.md#7-frontend-api-response-handling).
- **Changing OpenAPI operationId.** The frontend's generated TypeScript client uses operationIds as method names (`postApiEventTypes`, `getApiEventTypesById`, …). Renaming an operationId renames a frontend method, breaking the build.
- **Changing pagination shape.** Cursor → offset, or vice versa, per endpoint.

API redesigns happen after cutover, in a versioned way.

---

## How we enforce it: four layers

### Layer 1 — OpenAPI spec diff (every PR)

The Rust binary emits its OpenAPI spec at `/api/openapi.json`. The Go binary does the same. We diff them.

**Tool:** `oasdiff` (https://github.com/Tufin/oasdiff) — produces a structured diff between two OpenAPI specs and a breaking-change report.

**CI job** `parity-spec`:

```bash
# Build & start the Rust binary against an ephemeral Postgres.
docker run -d --name rust-fc -p 3001:3000 ghcr.io/flowcatalyst/rust:HEAD
# Build & start the Go binary against an ephemeral Postgres (Go binds 8080).
docker run -d --name go-fc -p 3002:8080 ghcr.io/flowcatalyst/go:HEAD

curl -s localhost:3001/api/openapi.json > /tmp/rust.json
curl -s localhost:3002/api/openapi.json > /tmp/go.json

oasdiff breaking /tmp/rust.json /tmp/go.json --fail-on ERR
```

`oasdiff breaking` reports any breaking change between the specs and exits non-zero. This catches: removed paths, renamed fields, changed required posture, changed enum values, changed status codes, changed operation IDs.

**During the port, this job will fail with a long list of "missing paths in Go" while Go is incomplete.** Mark those exempted in a small allow-list file (`tests/parity/spec-exempt.txt`) per route. The allow-list shrinks to zero by the end of Phase 3.

### Layer 2 — Replay-based contract tests (per subdomain)

In `tools/parityharness/`, a small tool that:

1. Boots both Rust and Go binaries against ephemeral databases (same schema, same seed data).
2. Loads a set of test requests from `tests/parity/requests/<subdomain>/*.yaml`.
3. For each request, fires it at the Rust binary and the Go binary.
4. Compares responses (status, headers, JSON body).
5. Reports any divergence.

Request format:

```yaml
# tests/parity/requests/event-types/create-happy-path.yaml
name: "Create event type, happy path"
seed:
  - sql: "INSERT INTO iam_clients (id, code, ...) VALUES ('clt_test01', 'test', ...);"
request:
  method: POST
  path: /api/event-types
  headers:
    Authorization: "Bearer ${ANCHOR_TOKEN}"
  body: |
    {
      "code": "orders:fulfillment:shipment:shipped",
      "name": "Shipment Shipped",
      "description": "When a shipment leaves"
    }
expect:
  status: 201
  body_shape:
    - id: tsid                        # placeholder type (not compared by value)
```

The harness compares:
- Status: equality.
- Headers: subset (the headers we care about are listed in a config).
- Body: structural diff, with placeholder types (`tsid`, `iso8601-microsecond`, `any-string`) that match a regex pattern instead of exact value.

CI runs the harness on every PR. Per-subdomain tests turn green as that subdomain is ported.

### Layer 3 — Golden tests for JSON marshaling (per type)

In `tests/golden/<package>/<type>.json`, we commit canonical JSON outputs for every public DTO. The Rust binary emits them; we capture them once; the Go binary's tests compare against them.

```go
// internal/platform/eventtype/api_test.go
func TestEventTypeResponseJSON(t *testing.T) {
    et := EventTypeResponse{ /* fixed input */ }
    got, _ := json.Marshal(et)

    want, err := os.ReadFile("../../../../tests/golden/eventtype/EventTypeResponse.json")
    require.NoError(t, err)

    // Use jsondiff for structural diff with ordered-key sensitivity.
    diff, _ := jsondiff.Compare(got, want)
    if diff != jsondiff.FullMatch {
        t.Fatalf("JSON shape divergence:\n%s", diff)
    }
}
```

Updating golden files requires (a) verifying the Rust binary produces the same new output, then (b) committing both the Rust capture and the Go expectation update in the same PR.

### Layer 4 — Frontend codegen-diff

The frontend uses `@hey-api/openapi-ts` to generate TypeScript clients from the OpenAPI spec. We want to be sure that regenerating against the Go server produces the same TypeScript.

CI job `parity-frontend`:

```bash
# Build the Go binary's spec, run openapi-ts against it, diff the output.
curl -s localhost:3002/api/openapi.json > frontend/openapi/spec.json
cd frontend && pnpm run api:generate

# Diff against committed generated client.
git diff --exit-code frontend/src/api/generated/
```

If the diff is non-empty, the Go spec produced different TypeScript than the Rust spec. Investigate before merging.

The frontend's committed generated client is the source of truth. The Vue source code consumes it.

---

## Specific parity hot-spots

### Timestamps

Rust uses `chrono::DateTime<Utc>` which serializes as `2026-05-24T08:30:00.123456Z` (microsecond precision, RFC3339).

In Go, `time.Time` with `time.RFC3339Nano` serializes as `2026-05-24T08:30:00.123456789Z` (**nanosecond** precision). This is a divergence.

**Fix:** truncate to microseconds before marshaling. Implement a custom JSON marshaling on a `Time` newtype:

```go
type Time struct{ time.Time }

func (t Time) MarshalJSON() ([]byte, error) {
    s := t.Truncate(time.Microsecond).UTC().Format("2006-01-02T15:04:05.000000Z")
    return []byte(`"` + s + `"`), nil
}

func (t *Time) UnmarshalJSON(b []byte) error { /* parse same format */ }
```

Use `Time` everywhere the schema has a timestamp. Tedious but mechanical.

### Nullable fields

Rust `Option<T>` with `#[serde(skip_serializing_if = "Option::is_none")]` omits the field entirely when None.

Go's stdlib `encoding/json` has two equivalents:
- `*T` + `omitempty` — omits when nil. **Use this.**
- `T` + `omitempty` — omits when zero-valued. **Avoid** — masks legitimate zeros (e.g., a count that's 0 won't appear).

For optional booleans specifically (where `false` is a legitimate value but you want to omit when "unset"), `*bool` + `omitempty` is necessary.

### Enum values

Rust:
```rust
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum EventTypeStatus {
    Current,
    Archived,
}
```

Serializes to `"CURRENT"` / `"ARCHIVED"`.

Go:
```go
type EventTypeStatus string

const (
    EventTypeStatusCurrent  EventTypeStatus = "CURRENT"
    EventTypeStatusArchived EventTypeStatus = "ARCHIVED"
)
```

Same wire format. Add a `Valid()` method that returns an error for unknown strings (matches the Rust `from_str` lenient-parsing behavior — default to one value rather than erroring).

### Empty collections

Rust `Vec<T>` with `#[serde(default, skip_serializing_if = "Vec::is_empty")]` omits when empty.

Go `[]T` is `null` by default when uninitialized. Use:

```go
type Response struct {
    Items []Item `json:"items,omitempty"`
}
```

`omitempty` on a slice omits when nil or empty (length 0). Matches Rust.

But beware: `[]Item{}` (initialized to empty) with `omitempty` will still be **omitted** in Go. If you want to always emit `[]` (e.g., for list endpoints), drop `omitempty`. Match Rust's posture per-field.

### Error envelope

**Correction (verified against source 2026-05-28):** an earlier version of this
section claimed the envelope key was `code` with a `details` object. That is
**wrong** — and it caused Go to ship `{code, message, details}`. The real Rust
`PlatformError -> ErrorResponse` (`crates/fc-platform/src/shared/error.rs`)
serializes errors as:

```json
{
  "error": "VALIDATION_ERROR",
  "message": "Event type code is required"
}
```

The `error` field carries the error **code** string. The dominant path
(`ErrorResponse`) has **no** `details` field; only the middleware `ApiError`
(`shared/api_common.rs`, used by auth/rate-limit middleware) carries an optional
`details` that is omitted when absent. So in practice the wire shape is
`{ "error", "message" }`.

Go emits the same shape. Centralized in `internal/platform/shared/httperror/`
(legacy chi path) and `internal/platform/shared/httpcompat/` (huma path):

```go
type Envelope struct {
    Code    string                 `json:"error"` // the error CODE string
    Message string                 `json:"message"`
    Details map[string]interface{} `json:"details,omitempty"` // middleware ApiError only
}
```

Status code mapping must match Rust's `PlatformError -> Response` conversion
exactly: Validation→400, Unauthorized/InvalidCredentials/TokenExpired/InvalidToken→401,
Forbidden→403, NotFound→404, Duplicate/BusinessRule/Concurrency→409,
TooManyRequests→429, catch-all→500. **There is no 422 in the Rust mapping.**
Capture the mapping in a test: feed every Rust error variant in, check the
resulting status + envelope.

### Pagination shapes

Rust has two pagination posture across the codebase:
- **Cursor-based** (for high-volume firehose tables — events, dispatch jobs)
- **Offset-based** (for everything else)

Plus the special "size only, no paginator" pattern for some firehose tables. Each list endpoint uses one specific posture. Don't change which one a given endpoint uses.

The shape of the response envelope must match per-endpoint:

```json
// Cursor:
{
  "items": [...],
  "nextCursor": "abc123",
  "hasMore": true
}

// Offset:
{
  "items": [...],
  "totalCount": 1234,
  "page": 1,
  "pageSize": 50,
  "totalPages": 25
}

// Size only:
{
  "items": [...]
}
```

Verify per endpoint by capturing a representative response from the Rust binary and asserting the Go binary produces the same envelope.

### Authentication

JWT shape must match exactly:

- Header: `alg=RS256`, `kid=<key-id>`, `typ=JWT`.
- Claims: `sub`, `iat`, `exp`, `iss`, `scope`, `clients` (array), `roles` (array), `applications` (array), `email`. Exact field names — no `client_ids` vs `clients` substitutions.

JWKS endpoint at `/.well-known/jwks.json` must return the same key set in the same format.

OAuth client_credentials grant endpoint at `/oauth/token` must return the same response shape, same `Cache-Control: no-store` header, same `Content-Type: application/json`.

**Chi-mounted auth/session endpoints (intentionally absent from `api/openapi.lock.json`).**
`api/openapi.lock.json` is generated from the huma-registered platform routes
(`make api-bump`) and CI-gated (`make api-diff`). The auth/session surface is
served by plain `chi` handlers, so it does **not** appear in that lock — by
design, mirroring how the Rust server documents `/auth/*` outside its utoipa
spec. The inventory (O7):

| Method | Path | Purpose |
|---|---|---|
| POST | `/auth/login` | password → session cookie + principal |
| POST | `/auth/refresh` | rotate a refresh token without an existing session |
| GET | `/auth/check-domain` | resolve auth method for an email's domain |
| GET | `/auth/me` | read the current session cookie's principal |
| GET | `/auth/oidc/login`, `/auth/oidc/callback` | OIDC SSO (email-domain path) |
| POST | `/auth/password-reset/request`, `/auth/password-reset/confirm` | unauthenticated password reset (hex SHA-256 tokens) |
| GET | `/auth/password-reset/validate` | check a reset token |
| GET | `/api/me`, `/api/me/applications` | caller identity + accessible applications |
| GET/POST | `/oauth/authorize`, `/oauth/token` | OAuth/OIDC provider surface |
| GET | `/.well-known/openid-configuration`, `/.well-known/jwks.json` | OIDC discovery + JWKS |

**Known behavioral gap — `/oauth/authorize?provider=<idp-id>` (not implemented):**
the Rust server supports a *direct-IDP* entry point on the authorize endpoint
(`oidc_service.get_authorization_url`). A downstream OAuth client of FlowCatalyst
can name an upstream IdP by id to deep-link the user straight into that IdP's
login — e.g. a "Login with Acme SSO" button — skipping the email-domain lookup.
The Go port does **not** implement this branch yet: it returns an OAuth
`server_error` redirect (see `oauthapi/authorize.go`, `TODO(oidc-by-provider)`).

This gap is **not visible in the OpenAPI spec diff** (same path, just an error
response), so it's recorded here. Normal logins are unaffected — the SPA enters
SSO via `/auth/oidc/login?domain=` (the email-domain path), which the Go server
fully supports end-to-end (start → IdP → `/auth/oidc/callback`). The `?provider=`
branch only matters for the *chained* case: a third-party app uses FlowCatalyst
as its OAuth provider **and** wants to pre-select a specific upstream IdP. To
close it: add a bridge method to resolve an IdP by provider id and build its
authorization URL, wire the authorize `?provider=` branch, and complete the
code flow on the callback (the bridge's `AuthCodeURL`/`Exchange` machinery in
`bridge/login_endpoint.go` already exists for the email-domain path).

### HMAC signing sites (audit result)

Audit of the Rust codebase identified **three HMAC-SHA256 signing sites**. Of these, **only one signs the result of JSON serialization**; the others sign raw bytes or plain strings, so JSON library choice is irrelevant.

| Site | What's signed | Risk |
|---|---|---|
| `fc-router/src/mediator.rs` (lines 40–57, 257–271) — webhook delivery | `format!("{ts}{payload}", ts=ISO8601, payload=serde_json::to_string(&MediationPayload))` where `MediationPayload = { messageId: &str }` | **AT-RISK** (one struct, one field) |
| `fc-sdk/src/webhook.rs` (lines 101–136) — subscriber-side verification | Raw inbound bytes (`&[u8]` from the HTTP body) concatenated with timestamp | SAFE |
| `fc-platform/src/scheduler/auth.rs` (lines 42–86) — dispatch job auth token | Plain `dispatch_job_id` string as bytes (no JSON) | SAFE |

**Non-HMAC signing**: JWT issuance (RS256, not HMAC), Argon2id passwords, AES-GCM AEAD tags, WebAuthn fake-credential HMAC — none are payload-signing-over-serialized-JSON.

The single at-risk site signs `{"messageId":"<tsid>"}` — a one-field struct. Every JSON library on the planet emits this identically (no whitespace ambiguity, no field ordering, no number representation, no Unicode escaping concerns). The risk is theoretical.

**Required test vector** (`tests/golden/webhook/mediation-payload.json`):

```json
{
  "message_id": "msg_01HXYZA1BCDEF",
  "timestamp": "2026-05-24T08:30:00.123Z",
  "secret": "test-secret-do-not-use-in-prod",
  "expected_payload_bytes": "{\"messageId\":\"msg_01HXYZA1BCDEF\"}",
  "expected_signed_string": "2026-05-24T08:30:00.123Z{\"messageId\":\"msg_01HXYZA1BCDEF\"}",
  "expected_signature_hex": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}
```

(The `expected_signature_hex` value is illustrative — the actual hex is captured by running the Rust signer once and committing.)

Both `fc-router` (Rust) and `internal/router` (Go) test suites verify that signing this input produces the committed hex. If either side regresses, CI fails. Same test vector is also embedded in `pkg/fcsdk/webhook/testdata/` so consumer SDKs (TypeScript, Laravel) can verify their verifiers against it.

**Headers** (both must match exactly):
- `X-FLOWCATALYST-SIGNATURE: <hex>` (lowercase hex, 64 chars)
- `X-FLOWCATALYST-TIMESTAMP: <ISO8601 with millisecond precision>` — format `%Y-%m-%dT%H:%M:%S%.3fZ` (3 fractional-second digits, not microseconds, not nanoseconds). Note this is **different** from the platform's general timestamp format (microseconds) — the router specifically uses milliseconds here.

### Webhook signatures (legacy section header, kept for cross-linking)

The signed canonical payload format and HMAC computation must be byte-identical. Pin a test vector:

```
# tests/golden/webhook/test-vector.json
{
  "input": {
    "method": "POST",
    "path": "/your/webhook",
    "body": "...",
    "headers": { ... }
  },
  "secret": "shhh-this-is-the-secret-for-the-test-vector",
  "expected_signature": "sha256=abc123def..."
}
```

Both Rust and Go SDK tests assert the test vector produces the expected signature. Test vector is committed; if it ever changes, both sides change in lockstep.

---

## What we WILL allow during the port

These are non-functional divergences that don't break compatibility:

- HTTP/2 vs HTTP/1.1 negotiation defaults.
- Server-side TLS cipher suite ordering.
- `Server` response header value (`flowcatalyst-go` vs `axum/0.8`).
- `Date` response header value (current time).
- Response gzip behavior (both should gzip when the client asks).
- Internal SQL query plans (as long as the result rows are correct).
- Connection pool sizing defaults.
- Log line format (stdout structure isn't part of the API contract).

---

## Cutover-time parity checks

The final pre-cutover gate per binary:

1. Replay 24h of production traffic against staging Go binary. Diff responses against the Rust binary running in parallel.
2. Zero non-allowlisted divergences for at least 24 consecutive hours.
3. Frontend's `pnpm run api:generate` against the Go spec produces a byte-identical `frontend/src/api/generated/` to the committed version.
4. Every Layer-2 contract test in `tests/parity/requests/` is green.
5. Webhook signature test vector passes.
6. JWT issued by Go validates with the Rust JWKS endpoint key set (and vice versa) — keys are shared, not regenerated.

If any of these fails, cutover is delayed.

---

## After cutover

The parity tests don't all get deleted. The contract tests in `tests/parity/` become the regression test bed — they ensure no future change accidentally breaks the public API. The Rust binary stays available for 6 months as a reference; after that, the contract tests stand on their own.

The OpenAPI spec at HEAD is the API contract. The frontend regenerates against it. Any breaking change to the spec requires a version bump (a topic for after cutover).
