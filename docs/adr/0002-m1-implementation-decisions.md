# ADR 0002 - M1 implementation decisions

- Status: accepted
- Date: 2026-11-13
- Deciders: Aditya
- Supersedes: ADR 0001, decision 1 (toolchain version only)

## Context

M1 turned the plan into a working ingestion path. Several details were unspecified, and one assumption in
ADR 0001 turned out to be wrong once real dependencies were selected.

## Decisions

1. **Go 1.26 instead of Go 1.23.** The module graph requires it: `github.com/prometheus/client_golang`
   v1.25.0 declares `go 1.26.0`, `github.com/jackc/pgx/v5` v5.13.0 declares `go 1.25.0`, and
   `github.com/go-chi/chi/v5` v5.5.0 declares `go 1.24`. Pinning every dependency back to versions that
   build on Go 1.23 would mean shipping an end-of-life toolchain and Docker base image. The *language*
   choice in ADR 0001 stands; only the minimum version changes. The `Dockerfile`, CI matrix and docs were
   updated in the same commit.
2. **Single implicit tenant until M5.** Every row already carries `tenant_id`, and migration `0001` seeds
   `00000000-0000-0000-0000-000000000001`. Handlers use that constant, so adding real tenants later is a
   resolution change rather than a schema migration.
3. **A static admin token for management endpoints in M1.** `EVENTRELAY_ADMIN_TOKEN` is compared in constant
   time. It is a single shared secret with no rotation, prefix or audit trail, which is exactly why API keys
   with hashed storage are M5 and why no documentation calls this an authentication system.
4. **UUIDs are scanned as text and parsed in Go.** Queries cast `id::text` and callers use `google/uuid`.
   This avoids registering a pgx UUID codec and keeps the store independent of driver-specific types. The
   cost is one allocation per row, which is irrelevant at this stage.
5. **Migrations run on a separate simple-protocol connection.** Migration files contain multiple statements,
   and the extended protocol accepts one statement per `Exec`. Each file runs in its own transaction and is
   recorded in `schema_migrations`.
6. **Payloads are stored as `jsonb`.** Readers get queryable JSON; the stored document is re-serialised by
   PostgreSQL, so key order and insignificant whitespace are not preserved. Signature verification always
   uses the raw request bytes, so integrity is unaffected.
7. **`Idempotency-Key` is accepted and ignored until M5.** Storing it now would activate the partial unique
   index and turn a legitimate retry into a constraint violation, which the handler would surface as `500`.
   Accepting and ignoring is the honest intermediate state and is documented in PLAN section 9.5.

## Consequences

- A contributor needs Go 1.26+ to build. CI installs it from `go.mod`.
- No handler needs to know about database codecs; the store is the only package that speaks SQL.
- Management authentication is deliberately weak and clearly labelled. Do not deploy M1 as a public service.
- The `events_idempotency` index exists but stays unused, so replaying a request creates a second event.
- When M5 lands, decisions 2, 3 and 7 are replaced by their planned implementations, not extended.
