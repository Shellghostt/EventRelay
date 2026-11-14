# EventRelay M1 and M2 validation — 2026-11-13

Branch: `feat/m2-implementation`. M2 is complete and unreleased. No main-branch changes or release
publication are part of this validation.

## Environment

Windows host (AMD Ryzen 5 5600H), native Go 1.26.11, Docker Desktop Linux containers,
PostgreSQL 16 and Redis 7.2. The final checks use the isolated `m2-eventrelay` Compose project.
Delivery test cases create a private database schema and Redis stream key, then remove them.

## M1 regressions

The existing signing vectors, HTTP validation, configuration, endpoint/event store and pagination tests
passed. Extra regressions cover signed JSON `null` (reject as 422), DSN username/query redaction and
repeatable store queries. Compose now builds from the repository root, concurrent process migrations
are serialized, and the quickstart converts endpoint secrets from base64 before hex-key HMAC signing.

## M2 acceptance

| Requirement | Evidence |
|---|---|
| Idle delivery within one second | `TestIngestThroughDeliveryAndRetry/OK` |
| Killed worker recovers after expiry | `TestKilledWorkerRecoversAfterLeaseExpiry` kills an OS process; one expired attempt and one successful redelivery |
| One delivery per lease | `TestExclusiveLeaseAndStaleCompletion` and `TestDuplicateDispatchAndTenantIsolation` |
| Attempt outcome/duration | Real database assertions in delivery and terminal-failure tests |
| HTTP 500 retries, HTTP 400 dead-letters | Signed ingest through worker/receiver tests |
| Four workers sustain 10,000/min | `TestComposeSustainedDelivery`: 10,000 durable deliveries in 58.84198 s; [report](../benchmarks/m2-delivery.md) |
| Bounded memory | Fixed concurrency/queue/stream/page sizes; stable sampled heap and container memory in the report |

Additional tests cover Redis publish outage and stream deletion, cross-tenant claims, max-attempt limits,
HTTP 410 disabling, atomic rollback of a failed completion write, and lease expiry while waiting for a
database row lock. HTTP tests verify signing, metadata, private/mapped/special-use IP rejection,
timeouts, redirect refusal and terminal classification after a truncated response body.

The documented Python demo ran against the actual Compose API and four worker processes, producing
`status: delivered` and `attempts: [1, 2, 3]` after two HTTP 500 responses. The delivery secret verified
each outbound request. Attempt history remains a SQL query until M4.

## Checks

- Native `go test -count=1 -timeout 120s ./...` against real PostgreSQL/Redis: passed.
- Full Linux `go test -race -count=1 -timeout=120s ./...` against the same services: passed.
- `go vet ./...` and `go build ./...`: passed.
- golangci-lint 2.15.0: zero issues; `govulncheck ./...` on Go 1.26.11: no vulnerabilities found.
- OpenAPI 3.1 schema validation: passed with `openapi-spec-validator`.
- Compose build, four-worker startup and signed demo: passed.
- Four-process load and independent four-CPU in-process load: passed; exact results are committed.

Reproduction commands are in [deploy/README.md](../../deploy/README.md). CI covers unit/race tests on
Windows, Ubuntu and macOS plus real PostgreSQL/Redis integration on Ubuntu. Feature/fix branches now
trigger CI as well as pull requests and main.

## CI recheck — 2026-11-14

The [initial M2 CI run](https://github.com/Shellghostt/EventRelay/actions/runs/43533644607) passed
build/race tests on Windows, Ubuntu and macOS, lint and the vulnerability scan. Its integration job
failed before checkout: PostgreSQL's fixed host port 55432 was already occupied on the runner.

The integration workflow now lets Docker assign free host ports for both services, passes those ports
to the test URLs, and checks PostgreSQL's final TCP listener rather than its temporary startup socket.
Verbose integration output records the tests executed; the job is bounded to ten minutes.

Local revalidation with disposable PostgreSQL 16 and Redis 7.2 on dynamically assigned ports passed
the native Windows full suite, full Linux race suite, `go vet`, golangci-lint 2.15.0 and actionlint 1.7.12.
The Linux race suite exercised ingest/delivery, retries, exclusive leases, killed-worker recovery,
Redis loss, outage recovery, terminal attempts, rollback and tenant isolation. The opt-in load tests
were not rerun; their original results remain in the benchmark report above. Native Windows race
revalidation was unavailable because the installed MinGW compiler only supports 32-bit mode; the
initial remote Windows race job passed. No application data was used by these disposable services.

## Scope and limits

Delivery is at least once; a receiver may process a request before a worker dies. M2 uses fixed 10-second
retries, default 8 attempts and a 5-second sweep. Exponential backoff, jitter, Retry-After, replay, tenancy,
rate limits, delivery metrics and production deployment remain later milestones. A finite local load run
does not guarantee throughput for arbitrary remote endpoints or indefinite stability. Destination response
content is discarded; `response_excerpt` remains NULL. Disabled endpoints keep their pending events.
