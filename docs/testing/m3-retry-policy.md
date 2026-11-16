# EventRelay M3 validation — retry policy and dead-letter handling — 2026-11-15

Branch: `feat/m3-implementation`. M3 is implemented and verified on this feature branch only.
It is **not merged into `main`**. These results were recorded locally before the branch's initial
push and CI run; remote checks are tracked in
[the branch's CI runs](https://github.com/Shellghostt/EventRelay/actions?query=branch%3Afeat%2Fm3-implementation).

This is the original M3 validation. The [audit correction record](m3-audit-fixes.md) documents
five subsequent fixes, additional regressions, and successful native Windows race and load reruns.

Implementation: ER-017–ER-020 under [ADR 0005](../adr/0005-m3-retry-policy.md). The fixed 10-second
delay is replaced by the documented schedule, jitter and `Retry-After`; fenced completion persists one
computed `next_attempt_at` and one jitter draw; dead letters carry exactly `attempts_exhausted`,
`non_retryable_response` or `non_retryable_policy`; expired-lease recovery consumes its attempt and
schedules under the same policy. [Migration 0003](../../migrations/0003_m3_retry_policy.sql) normalizes
M2 reason values and enforces the taxonomy with a CHECK constraint.

## Environment

- Windows host, Docker Desktop engine 29.9.2 (Linux containers). Disposable Compose project
  `m3-eventrelay` created for this validation: PostgreSQL 16 (`postgres:16-alpine`) on 127.0.0.1:55438
  and Redis 7.2 (`redis:7.2-alpine`) on 127.0.0.1:56382, with fresh volumes. No existing Compose
  project, container or volume was touched or reset. Delivery tests create a private PostgreSQL schema
  and Redis stream key each and drop them afterward.
- Module declares `go 1.26.0` and `toolchain go1.26.11`. Native Windows runs resolved the module
  toolchain automatically; the Linux race run pinned `GOTOOLCHAIN=go1.26.11` inside `golang:1.26.11`.
- GNU Make 4.4.1 (winget `ezwinports.make`) and golangci-lint v2.15.0
  (`go install .../golangci-lint/v2/cmd/golangci-lint@v2.15.0`) were installed on this machine for the
  canonical acceptance targets. `govulncheck` was installed at the latest version for this run.
- Randomness: acceptance tests inject deterministic stub random sources and fixed completion times, so
  they assert bounds and precedence rather than exact sequences; production jitter draws from the
  `math/rand/v2` global source. No seed is recorded because no result depends on one.
- Fast schedules in integration tests are injected test policies as allowed by ADR 0005; the production
  schedule itself is verified at unit level with a fake clock.

## Acceptance evidence (M3 requirements)

| Requirement | Evidence |
|---|---|
| Table entries, attempt limits 1/8/20, exhaustion, seeded jitter boundaries | `TestNominalWaitTable`, `TestNominalTotal` (31,360 s), `TestDecideExhaustionBeforeSchedule` (budgets 1/8/20 and just-below cases), `TestDecideJitterBounds`, `TestDefaultPolicyWaitBounds` |
| Retry-After seconds/date/zero/malformed/past/overflow and precedence with a fake clock | `TestParseRetryAfter`, `TestDecideRetryAfterPrecedence` (3600 s after attempt 1 vs 4.8 h policy wait after attempt 7, zero/negative/malformed/past/overflow, 24 h cap, jitter never shortens the header) |
| Success, 408/425/429, 5xx, other 4xx, 3xx, timeout, transport classification retained | `TestDeliverySignsAndClassifies` (all status classes), `TestBlockedDestinationAndTimeout`, `Classify` contract unchanged; integration: 500 retry, 400 dead-letter, 410 dead-letter + endpoint disable (`TestAttemptLimitAndGoneEndpoint`) |
| Late completion, interrupted final attempt, two concurrent sweepers preserve atomic state | `TestCompletionWaitingForRowLockRejectsExpiredLease`, `TestExclusiveLeaseAndStaleCompletion`, `TestInterruptedFinalAttemptDeadLettersWithReason`, `TestConcurrentRecoveryClosesAttemptOnce` |
| Restart and Redis flush/outage recovery; duplicate entries do not consume attempts | `TestKilledWorkerRecoversAfterLeaseExpiry` (killed OS process), `TestRedisStreamLossRecoversFromPostgres`, `TestPublishOutageStillAcceptsAndRecoversDurableEvent`, `TestDuplicateDispatchAndTenantIsolation` |
| M2 upgrade preserves events, attempts and dead letters; migrations repeatable | `TestMigrationNormalizesM2ReasonsAndEnforcesCheck` (0001+0002 database seeded with M2 rows, then full migration; reasons normalized, row counts intact, CHECK enforced, second run idempotent) + `TestMigrateIsIdempotent` |
| Exhaustion dead-letters with a reason | `TestExhaustionWritesStableReason` (budget 2, two calls), unit exhaustion at attempts 8/20 with default/ceiling budgets |
| Duplicate completion does not redraw jitter or extend a retry | `TestDuplicateCompletionDoesNotRedrawSchedule` (rejected duplicate leaves `next_attempt_at` untouched) |
| Invalid Retry-After records a bounded diagnostic, never the raw header | `TestInvalidRetryAfterUsesPolicyWaitWithDiagnostic` (attempt error and `last_error` exactly `HTTP 429; retry-after ignored: invalid value`) |

## Checks

- `make test` with `EVENTRELAY_TEST_DATABASE_URL` / `EVENTRELAY_TEST_REDIS_URL` against the disposable
  PostgreSQL 16 and Redis 7.2: **passed** (all packages; integration 21.5 s).
- `make lint` (`gofmt -l -w .`, `go vet ./...`, golangci-lint 2.15.0): **0 issues**.
- `govulncheck ./...`: **No vulnerabilities found.**
- Linux race suite in `golang:1.26.11` against the same services:
  `docker run --rm --network m3-eventrelay_default -v "D:/Dev Repo/EventRelay:/src:ro"
  -v m3-go-mod:/go/pkg/mod -v m3-go-build:/root/.cache/go-build -w /src -e GOTOOLCHAIN=go1.26.11
  -e EVENTRELAY_TEST_DATABASE_URL=... -e EVENTRELAY_TEST_REDIS_URL=... golang:1.26.11
  go test -race -count=1 -timeout=120s ./...` — **passed** (all packages; integration 22.1 s).
- Bounded load (in-process, `GOMAXPROCS(4)`, 60 s, `EVENTRELAY_LOAD=1`): **passed** —
  delivered=10000, attempts=10000, elapsed=58.8364346s, rate=10197.76/min, ingest p50=3.4401ms,
  p99=7.1003ms, post-GC heap samples ≈ 2.3–2.5 MB with no growth trend.

## Skips and limits

- **The original native Windows `make race` could not build with the 32-bit compiler.** Exact failure:
  `# runtime/cgo` / `cc1.exe: sorry, unimplemented: 64-bit mode not compiled in` — the installed MinGW
  compiler only supports 32-bit mode, the same limitation recorded in
  [the M1/M2 validation](m1-m2-validation.md). The
  [audit correction record](m3-audit-fixes.md) records the supported 64-bit compiler and successful
  native Windows race and load reruns. CI runs `go test -race` natively on windows-latest.
- The four-process Compose load test (`TestComposeSustainedDelivery`) was **not rerun**; the bounded
  in-process run above is the M3 load evidence, and the recorded M2 Compose result remains M2 evidence.
- CI had not run when this local validation was recorded. Remote results are tracked in the CI link
  above; local runs and the CI matrix/PostgreSQL-Redis integration job remain distinct evidence.
- All local runs are finite, against disposable services with synthetic events. They do not establish
  throughput against arbitrary remote endpoints, long-run leak absence, or production readiness.
- The M3 delivery demo with an injected short schedule (watching exhaustion and dead-letter promotion)
  is exercised by `TestExhaustionWritesStableReason` with an injected 50 ms test policy; no
  production schedule override exists (ADR 0005), so no demo configuration variable was added.
