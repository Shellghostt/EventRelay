# Development tools and memory validation

Base audit: 2026-11-14; M4 validation finalized 2026-11-18. These are development aids, not product features or substitutes for tests.
Read [PLAN.md](PLAN.md), the [M4 requirements](phases/M4.md), implemented [M3 contract](phases/M3.md)
and accepted [ADRs](adr/) first. M4 ADR 0006 is accepted.

## CodeGraph

CodeGraph 1.6.2 is installed and this repository's existing index was refreshed from merged M2.
The previous index was still at M1. Check freshness when switching branches or pulling changes:

```bash
codegraph status
codegraph sync
```

On 2026-11-17, `codegraph status` reported the existing index up to date against merged M3 source.
No new index or generated source was created for M4 preparation. The same existing index was refreshed
for M4 implementation; the final `codegraph sync` reported already up to date.

For indexed code, use the CodeGraph MCP exploration tool with this repository's absolute
`projectPath` and the symbol/flow you need. Read its returned source and callers before edits;
use direct source/config searches for areas the graph does not capture. Unresolved references remain,
so the graph does not prove complete dependency coverage, correctness or test coverage.
Builds and CI do not require the local index. Generated databases/transient files stay ignored through
`.codegraph/.gitignore`; do not commit them. Initialize a new index only when intentionally enabling it.

## MemGuard and runtime profiling

MemGuard is JavaScript/TypeScript static resource analysis. It does not analyze Go/Python, inspect a
running process, or prove there are no leaks. Pin its version, check the reported file count and
diagnostics, and inspect resource findings rather than suppressing them to get a green result.

Use Go allocation/heap profiles and process/container RSS for workers and recovery loops. Compare retained
heap after drain, idle and repeated outage/recovery cycles. The M2 load samples in
[benchmarks/m2-delivery.md](benchmarks/m2-delivery.md) describe finite runs; they do not establish absence
of leaks. MemGuard has no Go coverage. M4 supplements actual browser lifecycle measurements with its
static JS/TS analysis.

For runtime changes, reproduce the suspected workload, record a baseline, repeat the same lifecycle,
then compare live/retained memory after cleanup. Capture allocation rate, heap/RSS, goroutines or
active handles as applicable, and latency/drop metrics. Go benchmarks can collect `-benchmem` and
`-memprofile` for a specific package/workload; inspect profiles with `go tool pprof`. Node supports
heap snapshots/inspector; Python supports `tracemalloc`. Choose the native tool for the process.
Record versions, duration, inputs, commands and limitations in a validation report.
Profile only when the change or suspected fault justifies it; a documentation edit needs link/diff checks.

## M4 implementation validation

M4 frontend checks use pinned Node 22.22.3/npm 10.9.8, TypeScript 7.0.2, Biome 2.6.8, Vitest 5.1.0 and
Playwright 1.65.1. Eight unit tests and seven Chromium tests passed, including cancellation, hidden-tab
pause, stale results, bounded cursor history and repeated navigation. MemGuard 0.2.0 analyzed seven JS/TS
files with no findings or diagnostics; it does not cover all browser/backend behavior.

After five warmup rounds and 60 measured navigations, the latest Chromium retained-heap sample grew
from 3,667,860 to 3,910,380 bytes after forced GC, a 242,520-byte delta. The bounded four-CPU Go load run
delivered 10,000 events with stable post-GC heap samples around 2.4 MiB. The statistics query materialized
100,001 matching rows using `attempts_finished` in 221.701 ms. These finite observations do not establish
production memory/performance guarantees. Versions, commands, bounds and skips are in the
[M4 validation record](testing/m4-inspection-replay-dashboard.md).

Browser teardown now waits for the Go child to complete schema/Redis cleanup before Windows process
termination. The final run left zero browser fixture schemas/keys. Native Windows race uses the working
x64 GCC installation recorded in [M3 audit validation](testing/m3-audit-fixes.md).

## Memory Analyzer

The locally installed analyzer is Eclipse MAT 1.17.0, configured with Java 21 and a 4 GiB analyzer heap.
It reads Java/HPROF heap dumps. This repository has no Java runtime workload; MAT is therefore not an
appropriate gate for it. MAT's own heap setting is not this project's memory budget.
Heap dumps/profiles can contain secrets and user data; keep them local/ignored and use synthetic inputs.

## Handoff between editors or assistants

State the branch and commit, read the phase/ADRs, refresh the graph after source changes, and provide
the exact failing command, output and minimal synthetic fixture. Keep implementation on a feature
branch. A skipped integration test must be reported as skipped, with its missing dependency; it is not
a passing check. Preserve local Docker volumes and use disposable databases for migration tests.
