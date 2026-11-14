# Changelog

All notable changes to EventRelay are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- M2 Redis transport, signed HTTP workers, exclusive fenced leases, attempt recording, crash recovery,
  fixed-delay retries, terminal dead-letter records, private-destination protection and worker scaling.
- Explicit trusted-proxy CIDRs with right-to-left X-Forwarded-For validation (ER-037).
- Real PostgreSQL/Redis recovery tests, a four-process Compose load test, and a runnable signed demo.

- The repository tracks CodeGraph's index ignore rules and documents it as an optional local tool
  for code navigation. Nothing in the build or in CI depends on it.

### Security

- The request log no longer uses `chi`'s `middleware.RealIP`. That middleware copies
  `X-Forwarded-For` into `r.RemoteAddr` whether or not a proxy set it, so a client could forge the
  address it was logged under. The peer address is now logged in the `remote_addr` field, and
  `client_ip` field considers X-Forwarded-For only from configured trusted proxy CIDRs (ER-037).
- Destination response content is omitted from storage. Outbound requests have bounded headers/body
  reads, use no environment proxy, reject private resolved addresses and never follow redirects.

### Changed

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

- Integration CI uses Docker-assigned PostgreSQL/Redis host ports and waits for PostgreSQL TCP
  readiness, avoiding runner port conflicts before the delivery tests can start.
- M1 signed JSON null rejection, DSN query/username credential redaction, repeatable integration queries,
  Compose build context and base64-to-hex signing instructions. Migrations serialize concurrent startup.
- Completion checks lease expiry after acquiring the row lock, so lock contention cannot permit a stale
  success. Truncated non-retryable HTTP responses retain their terminal classification.

### Planned

- M3: exponential backoff, jitter, Retry-After and full dead-letter queue policy.

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
