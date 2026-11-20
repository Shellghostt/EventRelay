# Changelog

All notable changes to EventRelay are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- M4 inspection, endpoint recovery, replay and an embedded React dashboard (ER-021–ER-025/ER-021A,
  accepted [ADR 0006](docs/adr/0006-m4-replay-and-inspection.md)). Secret-free detail, bounded attempt/cycle
  pages, observed 24-hour statistics, compatible list extensions and guarded replay preserve event identity
  and lifetime history while giving each replay a frozen budget. Migration 0004 widens counters, archives
  cycles and fences old-runtime delivery transitions.
- Pinned frontend source, lockfile, embedded assets/license attribution, canonical Make targets and an
  Ubuntu browser/dependency CI job. [M4 local validation](docs/testing/m4-inspection-replay-dashboard.md)
  records Go/native Windows race, PostgreSQL/Redis, eight frontend unit tests, seven Chromium tests,
  load/heap/query measurements and a real dashboard screenshot. The M4 branch is published; final
  updated-branch CI is pending. The initial Picomatch audit advisory was withdrawn, and the locked
  frontend audit now passes.
- A separate opt-in race test restarts a verified disposable Redis container after accepting an exhausted
  event's replay. It proves durable recovery, one real delivery and preserved event/attempt/terminal
  history. Integration CI runs it against its own Redis service after the regular suite.
- [M3 requirements](docs/phases/M3.md) with phase boundaries, failure cases, ADR prerequisites and
  acceptance gates, plus a [development-tool audit](docs/DEVELOPMENT-TOOLS.md).
- M3 retry policy and dead-letter handling (ER-017–ER-020, [ADR 0005](docs/adr/0005-m3-retry-policy.md)):
  the documented backoff schedule with a repeated 24-hour entry for attempts 8–19 and exhaustion checked
  before scheduling, uniform 0.8–1.2 jitter with an injectable random source, `Retry-After` seconds and
  HTTP-date parsing with a 24-hour cap and bounded diagnostics for unusable values, stable terminal
  reasons (`attempts_exhausted`, `non_retryable_response`, `non_retryable_policy`), expired-lease
  recovery that consumes its attempt and schedules under the same policy, and migration 0003
  normalizing M2 reason values under a CHECK constraint.

- M2 Redis transport, signed HTTP workers, exclusive fenced leases, attempt recording, crash recovery,
  fixed-delay retries, terminal dead-letter records, private-destination protection and worker scaling.
- Explicit trusted-proxy CIDRs with right-to-left X-Forwarded-For validation (ER-037).
- Real PostgreSQL/Redis recovery tests, a four-process Compose load test, and a runnable signed demo.

- The repository tracks CodeGraph's index ignore rules and documents it as an optional local tool
  for code navigation. Nothing in the build or in CI depends on it.

### Security

- M4 browser access uses bounded 30-minute HttpOnly sessions, separate CSRF nonces, exact-origin mutation
  checks, logout revocation, restart invalidation and a same-origin CSP. New inspection reads and response
  sizes are bounded; payload/diagnostic evidence renders as text.
- The request log no longer uses `chi`'s `middleware.RealIP`. That middleware copies
  `X-Forwarded-For` into `r.RemoteAddr` whether or not a proxy set it, so a client could forge the
  address it was logged under. The peer address is now logged in the `remote_addr` field, and
  `client_ip` field considers X-Forwarded-For only from configured trusted proxy CIDRs (ER-037).
- Destination response content is omitted from storage. Outbound requests have bounded headers/body
  reads, use no environment proxy, reject private resolved addresses and never follow redirects.

### Changed

- M4 workers schedule by frozen cycle count/budget while keeping lifetime attempt numbers monotonic.
  Legacy event lists retain payloads/cursors; the dashboard requests summaries. Endpoint pagination is
  opt-in, and new cursors bind route, tenant and canonical parsed time/endpoint filters.
- Docker build context excludes frontend dependencies/test runtime, and the canonical Docker target now
  passes the requested `VERSION` to both binaries.
- Readiness checks both PostgreSQL and Redis. Feature and fix branches trigger the CI matrix and
  the PostgreSQL/Redis delivery integration job.
- The repository is published at https://github.com/Shellghostt/EventRelay and the CI workflow has
  executed. The first run on 2026-11-13 passed the build, test and PostgreSQL integration jobs on all
  three operating systems, including `go test -race`; the lint and vulnerability jobs failed on that
  run and are fixed by the entries above.
- The module declares `toolchain go1.26.11`. `actions/setup-go` prefers that directive over the `go`
  directive, so CI installs the patched toolchain instead of exactly Go 1.26.0, which carries three
  known standard-library vulnerabilities.
- The linter is golangci-lint v2.15.0, pinned by version, run by `golangci/golangci-lint-action@v9`.
  The v1 line cannot analyse a Go 1.26 module. Both findings it produced were fixed in the code.

### Fixed

- Endpoint recovery increments a management revision so an old-destination HTTP 410 cannot disable a
  corrected endpoint. Stale replay requests cannot start another cycle after rapid completion.
- Browser teardown waits for server cleanup, propagates cleanup failures and atomically records the Go
  child's exit before Playwright terminates the Windows process tree.
- Native build/run targets use Go's executable suffix so Windows runs the newly built `.exe` instead of
  resolving an older executable beside a file without an extension.
- Truncated retryable responses retain `Retry-After`, and blocked destinations receive
  `non_retryable_policy` while retaining their M2 attempt classification.
- Retry scheduling runs after completion fences without redrawing jitter for stale workers or shifting
  completion time during a row-lock wait. Direct and concurrent sweeps resolve worker defaults safely.
- The migration fixture drops its schema before closing the admin pool, reports cleanup errors and
  verifies the schema is gone. [Audit corrections and validation](docs/testing/m3-audit-fixes.md).
- Integration CI uses Docker-assigned PostgreSQL/Redis host ports and waits for PostgreSQL TCP
  readiness, avoiding runner port conflicts before the delivery tests can start.
- M1 signed JSON null rejection, DSN query/username credential redaction, repeatable integration queries,
  Compose build context and base64-to-hex signing instructions. Migrations serialize concurrent startup.
- Completion checks lease expiry after acquiring the row lock, so lock contention cannot permit a stale
  success. Truncated non-retryable HTTP responses retain their terminal classification.
- The delivery classification test sleeps briefly in its handler: a sub-millisecond local round trip
  floors the measured duration to zero under the ~0.5 ms Windows clock granularity.

### Planned

- M5 tenancy, API keys, rate limits and idempotency; M6 production posture and retention.

## [0.1.0] - 2026-11-13

Milestone M1: ingestion and durability.

### Added

- Event ingestion: `POST /v1/endpoints/{id}/events`, HMAC-SHA256 verification over the raw body, durable
  storage as a `pending` event, `202` with a stable `event_id`.
- Endpoint management: `POST /v1/endpoints` (returns both secrets once) and `GET /v1/endpoints` (never
  returns secrets), protected by a static admin bearer token.
- Event listing: `GET /v1/events` with cursor pagination and `status` / `endpoint_id` filters.
- PostgreSQL schema for the full v1 data model, applied on start and recorded in `schema_migrations`.
- Operational endpoints: `/healthz`, `/readyz` (503 when PostgreSQL is unreachable), `/metrics`.
- Configuration through `EVENTRELAY_*` environment variables with startup validation.
- `pkg/hmacsign`: public signing and verification helpers with published test vectors.
- `api/openapi.yaml` covering the implemented paths only.

### Known limitations

- No delivery. Events remain `pending`; the worker and retry scheduler arrive in M2 and M3.
- `Idempotency-Key` is accepted and ignored, so a retry creates a second event.
- Single implicit tenant; API keys and multi-tenancy are M5.

## [0.0.0] - 2026-11-13

- Initial skeleton. No functionality.
