# M4 inspection, replay and dashboard validation

Measurements: 2026-11-17; packaging/documentation: 2026-11-18; readiness recheck: 2026-11-20.
Branch: `feat/m4-implementation`.
Implementation commit: `2d545c7`.
Status: **implemented and verified locally; final updated-branch CI pending**. The locked frontend
audit and physical Redis container-restart test pass in the readiness recheck below. M4 remains unmerged.

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
| Documentation links/whitespace | Publication update: 11 Markdown files, 125 local links/anchors valid; diff passed whitespace validation |

The regular Go gates skip opt-in load scenarios unless their environment variables are set. The
bounded load scenario was run separately below. Unit-only CI jobs skip real service tests without URLs;
the dedicated integration/browser jobs configure real PostgreSQL/Redis services.

Runtime pins are React/ReactDOM 19.3.1. Frontend tooling is TypeScript 7.0.2, Vite 8.3.8, Vitest 5.1.0,
Playwright 1.65.1, Biome 2.6.8, Testing Library React 16.3.3, jsdom 31.0.1 and Axe Playwright 4.14.0.
Exact pins and the lockfile are committed. The license check reviews declared package metadata; runtime
license notices are emitted into the embedded assets with normalized line endings/trailing whitespace.

## Acceptance mapping

This table follows the checklist order in `docs/phases/M4.md`. Checked criteria have recorded evidence;
the physical Redis restart criterion is now verified; final updated-branch CI remains unchecked.
The original dependency results above and first CI failure are historical; the recheck is recorded below.

| Criterion | Named evidence | Status |
|---|---|---|
| 1. Auth/ownership, compatible lists, secret-free and factual inspection | `TestM4InspectionAuthOwnershipAndSummary`, `TestM4InspectionBoundsPaginationAndFilters`, `TestM4InspectionTimeoutAndResponseLimit` | Passed |
| 2. Exhausted replay/new budget, monotonic attempts and final expiry | `TestM4ExhaustedReplayAndFinalExpiry` | Passed |
| 3. Replay/state/completion/recovery guards and endpoint/410 races | `TestM4ConcurrentReplayAndStateGuards`, `TestM4ExhaustedReplayAndFinalExpiry`, `TestM4EndpointUpdateProtectsAgainstOld410`, existing delivery/M3 fence regressions | Passed |
| 4. M3 upgrade, terminal-history preservation, counter/cutover guards | `TestM4MigrationPreservesM3AndFencesOldWorkers`, `TestM4CounterCapacityAndEndpointRevisionOverflow` | Passed |
| 5. Redis interruption/restart after replay | `TestM4ReplayRedisInterruptionAndRestore` plus native-race `TestM4ReplayAcrossRedisContainerRestart`; real Docker stop/start, durable dispatch, one delivery and retained history | Passed locally |
| 6. Duplicate-effect UX and replay without an HTTP attempt | `TestM4ReplayPreservesHistoryAndRejectsStaleRequests`; browser retained-history/duplicate-warning and dead-letter replay tests | Passed |
| 7. Cursor traversal, concurrent inserts and live-membership contract | `TestM4InspectionBoundsPaginationAndFilters`; browser endpoint/event/attempt/cycle pagination; guide documents live membership | Passed |
| 8. Exact 24-hour window, response denominator, replay/missing evidence and bounds | `TestM4StatisticsPersistedResponsesAndReplayHistory`, `TestStatisticsExactUTCBoundaries`, `TestM4StatisticsAttemptLimitUnavailable`; endpoint browser statistics/zero state | Passed |
| 9. Frontend/auth/accessibility/text/pagination/poll lifecycle | Eight Vitest tests and seven Chromium tests listed below; Go session/origin tests | Passed |
| 10. M1–M3 integration/race/lint/build/destination/vulnerability regressions | Canonical Go gates with real URLs; existing delivery/signature/SSRF/fence/M3 suites; govulncheck | Passed locally |
| 11. Implemented dashboard screenshot | `web/e2e/dashboard.spec.ts` captures `docs/images/dashboard-m4.png` from synthetic local fixtures; visually inspected | Passed |
| 12. Final M4 branch CI and evidence mapping | Initial push CI passed six jobs; the withdrawn audit is resolved locally. Updated CI includes the physical restart case. | Final CI pending |

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

## Branch publication and CI

The user authorized the feature-branch push on 2026-11-18 (local date). Commit
`a176d83f95796dfdbf4338407d0f933f63035ead` was pushed to `origin/feat/m4-implementation` with upstream tracking.
[Push CI 44153798480](https://github.com/Shellghostt/EventRelay/actions/runs/44153798480) completed with
six successful jobs and one failure:

| Job | Result |
|---|---|
| Build and test — Ubuntu, Windows and macOS (three jobs) | Passed vet, `go test -race -count=1 ./...` and build |
| Integration tests — PostgreSQL and Redis | Passed real store/delivery tests under `-race` |
| Lint | Passed |
| Vulnerability scan | `govulncheck` passed |
| Dashboard source, browser and dependency checks | Type/lint/eight unit tests/build/license checks passed; npm audit failed. Asset drift and Chromium checks were skipped after that failure. |

The initial failing command was `npm audit --audit-level=moderate`. It reported five high-severity entries rooted
in Picomatch 4.0.7, used by development tools Vite/Vitest through Picomatch, Tinyglobby and Fdir.
[GHSA-fc5q-39xc-vjpf](https://github.com/advisories/GHSA-fc5q-39xc-vjpf) covers single-branch repeated
extglob ReDoS; its metadata was updated at `2026-11-17T15:56:44Z`. A local `npm --prefix web audit --json`
recheck reproduced the failure. At that publication check, the npm registry listed 4.0.7 as the
latest Picomatch release and the advisory had no first patched version. The
[proposed upstream fix](https://github.com/micromatch/picomatch/pull/254) remained unmerged.
The advisory was subsequently withdrawn; see the readiness recheck below.

This record identifies the implementation/publication run above. Documentation-only follow-ups run the
same workflow; [current branch runs](https://github.com/Shellghostt/EventRelay/actions?query=branch%3Afeat%2Fm4-implementation)
show their separate results. No M4 PR, merge or release was performed during the initial publication.

## 2026-11-20 readiness recheck

GitHub's advisory API reports GHSA-fc5q-39xc-vjpf as **Withdrawn Advisory**, with
`withdrawn_at=2026-11-18T15:38:56Z` and `updated_at=2026-11-19T16:07:41Z`.
The official npm registry still publishes Picomatch 4.0.7; a locked reinstall and
`npm audit --audit-level=moderate` now report **zero vulnerabilities**. Dependency pins, the lockfile,
embedded assets and the audit threshold have no changes. This resolves the obsolete advisory failure
through the corrected upstream advisory data.

`TestM4ReplayAcrossRedisContainerRestart` requires an explicit full container ID, checks Redis 7.2,
running state and the published loopback port against the fixture URL, and uses bounded Docker commands.
It runs separately from the ordinary suite. CI supplies its own Redis service ID through
`EVENTRELAY_TEST_REDIS_CONTAINER`; unset means a reported skip, while an invalid requested target fails.

The test exhausts a one-attempt event, accepts its replay, publishes a notification, physically stops
Redis and verifies the API's pending replay survives with no new HTTP attempt or destination call.
After a physical start, it removes only the fixture's stream notification/group to require PostgreSQL
republication even with Redis AOF. Recovery delivers once with unchanged ID, endpoint, type, payload
and receive time; lifetime attempts are 1 then 2, and the old exhausted attempt/cycle/reason remain intact.
Cleanup restores the verified container on failure and drops only the harness's schema/stream.

Native Windows `-race` passed the physical test in **2.93 s** (package **4.052 s**). Docker's start times
changed from `2026-11-20T15:09:12.599482264Z` to `2026-11-20T15:12:24.595973553Z`; the stopped state and
failed Redis ping/sweep were observed between them. Final counters were replay 1, lifetime attempts 2,
one retained prior cycle and one real destination call.

The fresh project was `m4-eventrelay-restart-20261120`, with PostgreSQL/Redis ports **55446/56386** and
new project volumes. Its containers/network were removed with Compose `down`, retaining those volumes.
Audit JSON, raw logs and the rechecked synthetic screenshot stay in ignored `bin/`.
Local `make test` passed all packages (integration **43.411 s**), `make lint`
reported **0 issues**, locked `make frontend-check` passed eight unit tests and all type/build/license/
audit gates, and all seven Chromium cases passed in **23.4 s**. Rebuilding preserved committed assets.
Native `make race` passed all packages (integration **47.183 s**); `make build` produced both binaries
with CGO disabled, and `govulncheck ./...` reported **No vulnerabilities found**. Actionlint 1.7.12
accepted the updated workflow. Final updated-branch GitHub results remain pending publication.

Reproduce the separate physical test against disposable services:

```powershell
$env:EVENTRELAY_PG_PORT='55446'
$env:EVENTRELAY_REDIS_PORT='56386'
docker compose -p m4-eventrelay-restart-20261120 -f deploy/docker-compose.yml up -d postgres redis
$env:GOTOOLCHAIN='go1.26.11'
$raceCompilerDir=Join-Path $env:LOCALAPPDATA 'Programs/MSYS2-Go-x64/msys64/ucrt64/bin'
$env:CGO_ENABLED='1'
$env:CC=Join-Path $raceCompilerDir 'gcc.exe'
$env:CXX=Join-Path $raceCompilerDir 'g++.exe'
$env:PATH="$raceCompilerDir;$env:PATH"
$env:EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@127.0.0.1:55446/eventrelay?sslmode=disable'
$env:EVENTRELAY_TEST_REDIS_URL='redis://127.0.0.1:56386/0'
$env:EVENTRELAY_TEST_REDIS_CONTAINER=docker compose -p m4-eventrelay-restart-20261120 -f deploy/docker-compose.yml ps -q redis
go test -race -count=1 -timeout 90s -run '^TestM4ReplayAcrossRedisContainerRestart$' -v ./tests/integration
```

## Cleanup and remaining validation limits

Two earlier browser runs left synthetic schema/Redis fixtures when Windows terminated the server process
tree before deferred cleanup finished. Their contents were inspected and only those identified fixtures
were removed. The server now retains signal handling through HTTP shutdown, joins cleanup failures into
its return error, and emits an atomic ignored completion marker only after its Go child exits. Global
teardown waits for that marker and requires exit code zero. The final seven-test run left **zero browser
fixture schemas and zero browser Redis keys**.

Automatic approval review rejected the combined Docker Redis stop/start plus HTTP replay experiment
with **"blocked by policy"**, giving no further reason. None of that command ran; that experiment did not stop Redis.
The real Redis client disconnect/reconnect integration test passed at that time. The physical container
scenario remained skipped during the original run. On 2026-11-20 the user explicitly requested this
validation; the new isolated, target-checked physical restart test above executed and passed.

The four-process Compose 10k load test was not rerun for M4; the committed M2 result remains M2 evidence.
Initial remote Linux/macOS/Windows Go checks and integration CI passed as recorded above. Frontend
asset/browser checks did not run in that initial failed workflow. The updated branch reruns the full
frontend job and separate physical restart case; final results remain pending until execution completes.

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
