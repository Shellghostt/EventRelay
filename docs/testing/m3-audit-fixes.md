# M3 audit corrections and validation — 2026-11-15

Branch: `feat/m3-implementation`. The five findings from the independent audit of
`8d02a408a9f9a9a481437b4ec1484a91d251ed8c` are corrected and verified locally. This record was
prepared before committing and pushing the corrections. M3 remains unmerged; remote checks are
tracked in [the branch's CI runs](https://github.com/Shellghostt/EventRelay/actions?query=branch%3Afeat%2Fm3-implementation).
This record supplements the
[original M3 validation](m3-retry-policy.md) and preserves the unsuccessful load runs below.

## Corrections and regression coverage

| Finding | Correction | Evidence |
|---|---|---|
| Truncated HTTP 429/503 loses Retry-After | Preserve response metadata when draining the body fails. Scheduling honors a header from a received retryable status, including a body-read failure classified `network`. | `TestBrokenRetryableResponsePreservesRetryAfter`, `TestDecideRetryAfterForBrokenResponse`, `TestBrokenResponseRetryAfterPersistsDelay` |
| Blocked IPs retry instead of receiving the policy reason | Carry a typed address-rejection cause into an internal `PolicyRejected` marker. The worker selects `non_retryable_policy` before exhaustion while retaining the M2 `network` outcome and bounded error text. | `TestBlockedDestinationAndTimeout`, `TestTransportFailureDoesNotRejectPolicy`, `TestDecidePolicyRejectionBeforeExhaustion`, `TestBlockedDestinationWritesPolicyReason` |
| Stale completion redraws jitter | `Store.Finish` invokes the worker's scheduling callback only after the row lock, token, attempt-number and database wall-clock expiry checks succeed. Completion time is captured before waiting for the lock. | `TestStaleWorkerCompletionDoesNotDrawJitter`, strengthened `TestDuplicateCompletionDoesNotRedrawSchedule` and `TestCompletionWaitingForRowLockRejectsExpiredLease`, `TestWorkerRetryUsesCompletionTimeBeforeRowLock` |
| Migration fixture leaks its schema | Register the admin-pool close before schema cleanup, report schema-drop errors with a finite deadline, and verify the namespace is absent after the migration test's cleanup. | Strengthened `TestMigrationNormalizesM2ReasonsAndEnforcesCheck`, including its post-drop cleanup assertion |
| Direct Sweep panics before Run initializes defaults | Resolve effective policy, random source and logger locally. Run and concurrent direct sweeps do not write defaults into shared worker configuration; injected policy/source values remain effective. | `TestSweepUsesDefaultsBeforeRun` runs two concurrent direct sweeps with both defaults and an injected policy/source |

The six additional integration tests are in [m3_regression_test.go](../../tests/integration/m3_regression_test.go).
They supplement the original eight M3 tests. `TestRetryDecisionFailurePreservesLease` also verifies
that a scheduling error leaves the attempt unfinished and the lease usable, and that success never
invokes the scheduling callback. Unit regressions are in the
[delivery tests](../../internal/delivery/client_test.go) and [policy tests](../../internal/retry/policy_test.go).

The HTTP API, outcome classification, lease/claim SQL, attempt budgets, backoff table, jitter bounds,
header cap and three terminal identifiers retain their existing contracts. No migration or
configuration variable was added. The internal `Store.Finish` scheduling argument is now a callback
returning a decision and error; successful attempts may pass nil.

## Environment

- Go `1.26.11`, matching the module toolchain, on `windows/amd64`; `CGO_ENABLED=1`.
- Native compiler: MSYS2 UCRT64 GCC `16.2.0-7`, target `x86_64-w64-mingw32`, MinGW-w64 CRT/headers
  `14.0.0.r598.ge3d18511f-1`, with `libsynchronization.a`. The earlier 32-bit compiler blocker is resolved.
- GNU Make 4.4.1, golangci-lint 2.15.0, Docker Engine 29.9.2, PostgreSQL 16.16 and Redis 7.2.17.
- Fresh Compose project `er-m3-fixes-9e830b42`: PostgreSQL at port 55439 and Redis at port 56383.
  Tests use private schemas and stream keys with synthetic data.
- Host: Ryzen 5 5600H, 12 logical processors, approximately 16 GB RAM. Docker reports 12 CPUs and
  approximately 7.4 GiB available. Both load runs set `GOMAXPROCS(4)` and use four worker instances;
  the Linux container additionally has a four-CPU quota.
- Jitter tests use counted or fixed sources. Policy tests use fixed UTC completion time
  `2026-11-15T12:00:00Z`; the worker lock-wait test brackets the actual completion time.

## Checks

| Command/check | Result |
|---|---|
| Delivery and retry unit tests | Passed |
| Focused real-service regressions | Passed; integration package 9.270s |
| Concurrency/clock regressions under native `-race -count=3` | Passed; integration package 21.656s |
| `make test` with PostgreSQL/Redis | Passed; all packages, integration 32.936s, command 42.847s |
| `make lint` | Passed; 0 issues, command 35.356s |
| Native Windows `make race` with PostgreSQL/Redis | Passed; all packages, integration 39.846s, command 60.300s |
| `make build` and built API binary's `version` command | Passed; command 46.645s, banner `eventrelay dev` |
| `govulncheck ./...` | No vulnerabilities found |
| Linux bounded load, `golang:1.26.11`, four-CPU quota | Passed; 10,000 deliveries and attempts in 58.842369374s, 10,196.73/min, ingest p50 3.263663ms / p99 29.02386ms |
| Native Windows bounded load after the CPU-heavy game closed | Passed; 10,000 deliveries and attempts in 58.8437507s, 10,196.49/min, ingest p50 4.3345ms / p99 16.2666ms |
| CodeGraph sync and whitespace check | Passed; local index refreshed, `git diff --check` clean |

Post-GC heap samples for Linux were `[2090232 2153568 2251064 2185928 2414320]` bytes, and for the
successful Windows run `[2379272 2445736 2501688 2436112 2467424]` bytes. Both stayed within the
load test's existing 32 MiB growth limit. The measured delivery interval is approximately 58.84s;
the complete test process takes approximately 61s because fixture shutdown follows measurement.

GNU Make emits the existing Windows shell warning `The system cannot find the path specified`
before its targets. Its Git version probe uses Unix redirection, so the build falls back to `dev`.
The target commands above completed with exit code zero. This audit did not change the Makefile.

## Load failures and comparison

The first Windows load run delivered **6,617/10,000** before its existing drain deadline and failed.
A second run used a Go overlay containing the unchanged HEAD versions of the delivery client,
retry policy, store, worker and test harness; it also failed, with **8,355/10,000** delivered.
The overlay preserved the working-tree edits and used the same Go version, services and thresholds.

The host remained at 71% CPU after the baseline test exited, with a game consuming approximately
five CPU cores and only approximately 1.4 GiB free physical memory. These runs do not provide a clean
before/after performance comparison. The fixed code subsequently passed the Linux load run, then
the unchanged Windows load command passed after the user closed the game. Immediately before that
Windows rerun, the game process was absent and host CPU measured 37%. No acceptance threshold,
event count, workload pacing or production code was changed between the failed and successful runs.

## Commands

PowerShell, from the repository root, using a fresh disposable project:

```powershell
$env:GOTOOLCHAIN='go1.26.11'
$env:EVENTRELAY_PG_PORT='55439'
$env:EVENTRELAY_REDIS_PORT='56383'
docker compose -p er-m3-fixes-9e830b42 -f deploy/docker-compose.yml up -d --wait postgres redis
$env:EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@127.0.0.1:55439/eventrelay?sslmode=disable'
$env:EVENTRELAY_TEST_REDIS_URL='redis://127.0.0.1:56383/0'
make test
make lint
make race
make build
govulncheck ./...

go test -race -count=3 -timeout 120s -run '^Test(StaleWorkerCompletionDoesNotDrawJitter|SweepUsesDefaultsBeforeRun|WorkerRetryUsesCompletionTimeBeforeRowLock|DuplicateCompletionDoesNotRedrawSchedule|CompletionWaitingForRowLockRejectsExpiredLease)$' ./tests/integration

$env:EVENTRELAY_LOAD='1'
go test -count=1 -timeout 120s -run '^TestSustainedDelivery$' -v ./tests/load
Remove-Item Env:EVENTRELAY_LOAD
```

The Linux load command used the same services through their Compose network, read-only source
and module-cache mounts, `golang:1.26.11`, `--cpus 4`, `GOMAXPROCS=4`, `EVENTRELAY_LOAD=1`, and
`go test -count=1 -timeout=120s -run '^TestSustainedDelivery$' -v ./tests/load`.

## Limits and cleanup

The checks in this record preceded remote CI; the four-process Compose load test was not run.
These are finite local checks against
synthetic data; they do not establish long-term resource behavior or arbitrary remote-endpoint
throughput. The Linux load result is new evidence for the corrected code; a full Linux race rerun
was not performed because the full native Windows race suite passed.

Cleanup completed for the disposable `er-m3-fixes-9e830b42` project only. Container and volume labels
were verified before removal, and no project containers or volumes remained afterward. The database
contained zero `er_test_%` schemas before shutdown, confirming fixture cleanup across the runs.
Existing Docker projects/data, accepted ADRs and Git remote state were preserved.
