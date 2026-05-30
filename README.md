# FlowCatalyst Go

A Go reimplementation of [`flowcatalyst-rust`](../flowcatalyst-rust/) — a multi-tenant event router and webhook delivery platform.

Designed as a **drop-in replacement**: same Postgres schema, same HTTP API contracts, same OpenAPI spec, same frontend. Existing SDK consumers, the Vue frontend, and webhook subscribers will keep working unchanged after cutover.

---

## Status

🚧 **Planning phase complete.** All ten Phase-0 decisions are locked in ([`PLAN.md` §10](./PLAN.md#10-decisions)). Phase 0 scaffolding begins next.

## Read order

1. [**PLAN.md**](./PLAN.md) — master plan: scope, phases, timeline, risks, open questions.
2. [**docs/conventions.md**](./docs/conventions.md) — engineering conventions, ported from the Rust [`CLAUDE.md`](../flowcatalyst-rust/CLAUDE.md). Read before writing any code.
3. [**docs/architecture.md**](./docs/architecture.md) — crate-to-package mapping, module layout, library choices.
4. [**docs/usecase-pattern.md**](./docs/usecase-pattern.md) — the compile-time-sealed UseCase + UnitOfWork pattern, with a fully worked example (event_type/create).
5. [**docs/api-parity.md**](./docs/api-parity.md) — how we guarantee byte-compatibility with the Rust HTTP API.

## How to use this repo

Right now: read the docs, sign off on the open questions in [`PLAN.md` §10](./PLAN.md#10-open-questions-for-the-team), and we begin Phase 0.

After Phase 0:

```sh
# Run all tests (unit + integration via testcontainers)
go test ./...

# Run linters
golangci-lint run

# Run the custom UoW seal analyzer
go vet -vettool=$(which uowseal) ./internal/platform/...

# Run parity tests against a Rust binary at $RUST_FC_URL
go test ./tests/parity/... -rust=$RUST_FC_URL
```

## Operator notes

### Default HTTP port

`fc-server` defaults to `FC_API_PORT=8080`. The Rust `fc-server` defaults
to `3000`. Operators running both side-by-side, or running Go behind
load-balancer rules that hard-code `3000`, should set `FC_API_PORT=3000`
explicitly. The `FC_API_PORT` env var (or its legacy alias `PORT`)
overrides the binary default.

| Service       | Go default          | Rust default        | Override env             |
|---------------|---------------------|---------------------|--------------------------|
| Platform HTTP | `FC_API_PORT=8080`  | `PORT=3000`         | `FC_API_PORT` / `PORT`   |
| fc-dev        | `--api-port 8080`   | `--api-port 8080`   | `FC_API_PORT` / `--api-port` |
| Metrics       | `FC_METRICS_PORT=9090` | `9090`           | `FC_METRICS_PORT`        |
| MCP           | `127.0.0.1:8090`    | `127.0.0.1:3100`    | `FC_MCP_PORT` / `FC_MCP_BIND` |

## Relationship to the Rust codebase

Until cutover, both codebases coexist:

- **Rust** owns: migrations, production traffic, OpenAPI spec source-of-truth.
- **Go** owns: nothing in production initially; grows feature parity domain by domain behind feature flags.

After cutover (per [`PLAN.md` §3](./PLAN.md#3-approach)):

- **Go** owns: migrations, production traffic, OpenAPI spec.
- **Rust** is archived. Retained for 6 months as a reference implementation, then deleted.

## License

Proprietary — FlowCatalyst.
