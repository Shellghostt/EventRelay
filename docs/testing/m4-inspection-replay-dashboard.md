# M4 inspection, replay and dashboard validation

Measurements: 2026-11-17; final packaging/documentation checks: 2026-11-18.
Branch: `feat/m4-implementation`.
Implementation commit: `2d545c7`.
Status: **implemented and verified locally; publication/branch CI pending**. The physical Redis
container-restart scenario remains blocked/skipped. M4 is not merged or released.

Baseline: merged M3 `853486150244a008146e5c00b5c2749179b53da2`, followed by the M4 requirements commit
`e214999db8ec4932f8499fbb0b213c0d04a2cd86` and accepted ADR commit `7a06c80`.
Contracts: [M4 requirements](../phases/M4.md), [ADR 0006](../adr/0006-m4-replay-and-inspection.md),
[usage/upgrade guide](../guides/inspection-and-replay.md), [bearer OpenAPI](../../api/openapi.yaml)
and [browser OpenAPI](../../api/dashboard.openapi.yaml).

## Implementation verified

- Migration 0004 widens lifetime/replay/attempt counters, preserves M3 as cycle zero, freezes each
  delivery budget, records attempt cycle/revision metadata and archives completed cycles. A
  transaction-local M4 marker fences old-runtime claims, completion, recovery and replay after cutover.
- Replay requires terminal state, an enabled owned endpoint, no lease, the expected replay count and
  capacity for a whole new budget. One transaction preserves identity/history, archives the old cycle,
  removes active DLQ membership and sets pending/due state. Only workers create HTTP attempts.
- The unchanged M3 retry/expiry policy uses the current cycle count and frozen budget; lifetime attempt
  numbers remain monotonic. Endpoint PATCH requires a revision, and stale HTTP 410 cannot disable a
  corrected revision. URL/enabled changes do not alter active budgets or initiate a destination probe.
- Secret-free inspection/statistics, payload-free list summaries, compatible legacy lists, HMAC-bound
  cursors and UTC time filters have explicit query/page/diagnostic/payload/response bounds.
- The embedded React console includes inspection, endpoint recovery and guarded replay. Bounded
  cookie sessions have origin/CSRF checks, absolute expiry, logout/restart invalidation and a same-origin
  CSP. Polling/navigation/pagination retain bounded resource state and render captured evidence as text.

## Environment and canonical gates

Native Windows amd64; AMD Ryzen 5 5600H (6 cores/12 logical processors). The bounded load test sets
`GOMAXPROCS(4)` internally. Go 1.26.11, GNU Make 4.4.1, golangci-lint 2.15.0, Node 22.22.3/npm 10.9.8.
Fresh Docker project `m4-eventrelay` used PostgreSQL 16 and Redis 7.2, with host ports 55444/56384 and
its own `m4-eventrelay_pgdata`/`m4-eventrelay_redisdata` volumes. Other projects/volumes were untouched.

```powershell
$env:GOTOOLCHAIN='go1.26.11'
$env:EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@127.0.0.1:55444/eventrelay?sslmode=disable'
$env:EVENTRELAY_TEST_REDIS_URL='redis://127.0.0.1:56384/0'
make VERSION=m4-local test
make VERSION=m4-local lint
make VERSION=m4-local build
make VERSION=m4-local frontend-check
make VERSION=m4-local frontend-browser
make VERSION=m4-local frontend-assets-check
```

Native Windows race used the working x64 GCC setup from [M3 audit validation](m3-audit-fixes.md):

```powershell
$env:CGO_ENABLED='1'
$env:CC='C:\Users\Aditya\AppData\Local\Programs\MSYS2-Go-x64\msys64\ucrt64\bin\gcc.exe'
$env:PATH='C:\Users\Aditya\AppData\Local\Programs\MSYS2-Go-x64\msys64\ucrt64\bin;'+$env:PATH
make VERSION=m4-local race
```

| Check | Observed result |
|---|---|
| `make VERSION=m4-local test` with real service URLs | Passed all packages; final integration package 43.635 s |
| `make VERSION=m4-local race`, native Windows x64 GCC | Passed all packages; final integration package 45.269 s |
| `make VERSION=m4-local lint` | gofmt/vet/golangci-lint passed, **0 issues** |
| `make VERSION=m4-local build` | Both Go binaries built using committed assets |
| Native executable/version smoke | `eventrelay.exe` and `worker.exe` both report `m4-local`; `make run` selects the new `.exe` on Windows |
| `make VERSION=m4-local docker` and container version smoke | Go-only image built; both Linux binaries report `m4-local` |
| `make VERSION=m4-local frontend-check` | Type/lint, **8 unit tests**, build, 155-package declared-license check and npm audit passed |
| `make VERSION=m4-local frontend-browser` | **7 Chromium tests passed**, 19.1 s including teardown |
| `make VERSION=m4-local frontend-assets-check` | Rebuild matched staged/tracked embedded assets; no drift |
| `govulncheck ./...` | **No vulnerabilities found** |
| OpenAPI reference/path/schema validation | Both documents valid: **25 operations**, **16 JSON Schema 2020-12 schemas** |
| actionlint 1.7.12 | CI workflow passed validation |
| `memguard run web/src --json --strict`, MemGuard 0.2.0 | **7 JS/TS files**, no issues or diagnostics |
| `codegraph sync` / `codegraph status` | Existing index refreshed/up to date; no new index initialized |
| Documentation links/whitespace | 11 Markdown files: 123 local links/anchors valid; staged diff passed whitespace validation |

The regular Go gates skip opt-in load scenarios unless their environment variables are set. The
bounded load scenario was run separately below. Unit-only CI jobs skip real service tests without URLs;
the dedicated integration/browser jobs configure real PostgreSQL/Redis services.

Runtime pins are React/ReactDOM 19.3.1. Frontend tooling is TypeScript 7.0.2, Vite 8.3.8, Vitest 5.1.0,
Playwright 1.65.1, Biome 2.6.8, Testing Library React 16.3.3, jsdom 31.0.1 and Axe Playwright 4.14.0.
Exact pins and the lockfile are committed. The license check reviews declared package metadata; runtime
license notices are emitted into the embedded assets with normalized line endings/trailing whitespace.

## Acceptance mapping

This table follows the checklist order in `docs/phases/M4.md`. Checked criteria have local evidence;
the physical Redis restart and final branch CI criteria stay unchecked.

| Criterion | Named evidence | Status |
|---|---|---|
| 1. Auth/ownership, compatible lists, secret-free and factual inspection | `TestM4InspectionAuthOwnershipAndSummary`, `TestM4InspectionBoundsPaginationAndFilters`, `TestM4InspectionTimeoutAndResponseLimit` | Passed |
| 2. Exhausted replay/new budget, monotonic attempts and final expiry | `TestM4ExhaustedReplayAndFinalExpiry` | Passed |
| 3. Replay/state/completion/recovery guards and endpoint/410 races | `TestM4ConcurrentReplayAndStateGuards`, `TestM4ExhaustedReplayAndFinalExpiry`, `TestM4EndpointUpdateProtectsAgainstOld410`, existing delivery/M3 fence regressions | Passed |
| 4. M3 upgrade, terminal-history preservation, counter/cutover guards | `TestM4MigrationPreservesM3AndFencesOldWorkers`, `TestM4CounterCapacityAndEndpointRevisionOverflow` | Passed |
| 5. Redis interruption/restart after replay | `TestM4ReplayRedisInterruptionAndRestore` disconnects/reconnects the real Redis client and proves durable replay dispatch/history. Physical container restart did not run. | Partial; checkbox open |
| 6. Duplicate-effect UX and replay without an HTTP attempt | `TestM4ReplayPreservesHistoryAndRejectsStaleRequests`; browser retained-history/duplicate-warning and dead-letter replay tests | Passed |
| 7. Cursor traversal, concurrent inserts and live-membership contract | `TestM4InspectionBoundsPaginationAndFilters`; browser endpoint/event/attempt/cycle pagination; guide documents live membership | Passed |
| 8. Exact 24-hour window, response denominator, replay/missing evidence and bounds | `TestM4StatisticsPersistedResponsesAndReplayHistory`, `TestStatisticsExactUTCBoundaries`, `TestM4StatisticsAttemptLimitUnavailable`; endpoint browser statistics/zero state | Passed |
| 9. Frontend/auth/accessibility/text/pagination/poll lifecycle | Eight Vitest tests and seven Chromium tests listed below; Go session/origin tests | Passed |
| 10. M1–M3 integration/race/lint/build/destination/vulnerability regressions | Canonical Go gates with real URLs; existing delivery/signature/SSRF/fence/M3 suites; govulncheck | Passed locally |
| 11. Implemented dashboard screenshot | `web/e2e/dashboard.spec.ts` captures `docs/images/dashboard-m4.png` from synthetic local fixtures; visually inspected | Passed |
| 12. Final M4 branch CI and evidence mapping | Local evidence mapping exists; branch is unpublished and its CI has not run | Pending; checkbox open |

New integration tests are in [m4_test.go](../../tests/integration/m4_test.go) and
[m4_upgrade_stats_test.go](../../tests/integration/m4_upgrade_stats_test.go). The exact UTC-boundary test
is in [inspection_window_test.go](../../internal/store/inspection_window_test.go).

Additional bounds coverage proves payload over 512 KiB is explicitly unavailable, diagnostics truncate
with a flag, in-flight finish/response fields remain null, malformed/changed-route/filter cursors fail,
long fractional-second filter spellings generate bounded canonical cursors, and the response/deadline
limits fail visibly. Migration tests preserve M3 counts/attempts/all three reasons, reject old delivery
transitions without the marker, preserve unknown historical fields and safely repeat the migration runner.

## Browser and session evidence

The seven real Chromium tests are:

1. Authenticated overview, endpoint/event pagination, keyboard access, Axe checks and real screenshot.
2. Endpoint inspection, zero-denominator statistics and guarded re-enable.
3. Attempt/cycle pages, retained replay history and duplicate-effect warning.
4. Dead-letter replay, delivered state/history and payload/diagnostic text injection.
5. Navigation cancellation/stale suppression and readable mobile layout.
6. A stale replay shown once, cross-origin cookie mutation rejection and cookie exclusion from `/v1`.
7. Repeated navigation with retained-heap measurement after GC.

Eight unit tests cover same-origin cookie/CSRF requests, one-shot conflict handling, one active poll,
five-second scheduling after completion, navigation abort/stale suppression, hidden-tab pause/resume,
unmount cleanup, stale/401 states, manual refresh cancellation and the 20-entry cursor bound.

`TestDashboardSessionOriginCSRFRevocationAndRestart`, `TestDashboardAbsoluteExpiryAndBoundedSessions`,
`TestDashboardTLSProxyAndUnconfiguredRemoteHTTP` and `TestDashboardOriginValidation` cover origin/nonce,
logout/restart revocation, absolute expiry, the 256-session cap and TLS-proxy/loopback configuration.

The screenshot is [docs/images/dashboard-m4.png](../images/dashboard-m4.png), captured at 1440 × 1700
from the implemented embedded dashboard. Its endpoint/event data is synthetic; it is not a mock design.
Mobile screenshots and raw browser heap data remain ignored under `web/test-results/`.

## Load, retained heap and statistics query

The M4 `TestSustainedDelivery` run used signed HTTP ingestion, real PostgreSQL/Redis and in-process
workers, with four Go CPUs and a 60-second bound. Reproduce with the test URLs above:

```powershell
$env:EVENTRELAY_LOAD='1'
go test -v -run '^TestSustainedDelivery$' -count=1 -timeout=120s ./tests/load
Remove-Item Env:EVENTRELAY_LOAD
```

Observed M4 measurement:

```text
delivered=10000 attempts=10000 elapsed=58.8367187s rate_per_min=10197.71
ingest_p50=3.2323ms ingest_p99=7.365ms
heap_after_gc=[2413680 2444208 2467120 2450696 2553576]
```

The final browser lifecycle run measured five warmup rounds, then 20 rounds/60 navigations, collecting
garbage before each retained-heap reading:

```text
beforeBytes=3667860 afterBytes=3910380 deltaBytes=242520
```

The browser test requires a positive initial sample and a delta below 4 MiB; the observed delta was
about 237 KiB. These finite measurements demonstrate the tested bounds and do not prove absence of
leaks or the M6 sustained-ingest/production budgets. The load run is an in-process regression, not a
new four-process Compose result.

The actual statistics query was extracted from `internal/store/inspection.go`, run against 100,001
synthetic finished attempts and profiled with `EXPLAIN (ANALYZE, BUFFERS)`. The transaction was rolled
back. [The recorded plan](m4-statistics-query-plan.txt) used `attempts_finished`, materialized 100,001
matching rows and completed in **221.701 ms**. The API returns unavailable at this size instead of a
partial success rate. This one-endpoint local profile is not evidence for every production data distribution.

## Cleanup and publication limits

Two earlier browser runs left synthetic schema/Redis fixtures when Windows terminated the server process
tree before deferred cleanup finished. Their contents were inspected and only those identified fixtures
were removed. The server now retains signal handling through HTTP shutdown, joins cleanup failures into
its return error, and emits an atomic ignored completion marker only after its Go child exits. Global
teardown waits for that marker and requires exit code zero. The final seven-test run left **zero browser
fixture schemas and zero browser Redis keys**.

Automatic approval review rejected the combined Docker Redis stop/start plus HTTP replay experiment
with **"blocked by policy"**, giving no further reason. None of that command ran; that experiment did not stop Redis.
The real Redis client disconnect/reconnect integration test passed. The physical container-restart
acceptance scenario remains skipped/open and was not attempted through a workaround.

The four-process Compose 10k load test was not rerun for M4; the committed M2 result remains M2 evidence.
No M4 Linux/macOS remote run or branch CI is claimed. The Ubuntu frontend/browser job, existing Go
OS matrix, real integration job, lint and vulnerability jobs are prepared for authorized publication.
No push, PR or merge was performed for this M4 feature.

On resuming work on 2026-11-18, Docker Desktop and the existing M4 test containers were already stopped.
Docker Desktop was started for the final image build; the M4 PostgreSQL/Redis containers remain stopped,
with their data volumes retained. The recorded integration/browser/cleanup results are from 2026-11-17.
Retained test containers can be started with:

```powershell
docker compose -p m4-eventrelay -f deploy/docker-compose.yml start postgres redis
```

To stop/remove only this project's containers after using them, retaining data:

```powershell
docker compose -p m4-eventrelay -f deploy/docker-compose.yml down
```
