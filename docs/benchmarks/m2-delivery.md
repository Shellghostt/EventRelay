# M2 delivery verification — 2026-11-13

## Four separate worker processes

The final Compose run used a separate API, PostgreSQL 16, Redis 7.2 and four worker containers,
each with `EVENTRELAY_WORKERS=1`, 60-second leases and 5-second sweeps. The load driver ran
in a Go 1.26.11 container on the same Compose network with `--cpus 4`.
The services were not CPU-limited; this is a four-worker measurement, not a four-CPU total deployment.
Docker Desktop's VM had 7.429 GiB available. Host: AMD Ryzen 5 5600H, Windows.

Command and setup are in [deploy/README.md](../../deploy/README.md), under
`TestComposeSustainedDelivery`. The test verifies every outbound signature, stable event ID and attempt
number, then counts durable delivered rows and recorded attempts. Its offered rate is 10,200/min,
with four producers and a bounded 64-entry queue. It fails above 60 seconds, on loss or on duplicates.

| Measurement | Result |
|---|---:|
| Events delivered / receiver calls / recorded attempts | 10,000 / 10,000 / 10,000 |
| Elapsed ingest through final durable delivery | 58.841979971 s |
| Delivery rate | 10,196.80 / min |
| Ingest p50 / p99 | 3.830074 ms / 7.008377 ms |
| Worker 1 memory, late in run → after completion | 8.117 → 7.895 MiB |
| Worker 2 memory, late in run → after completion | 8.004 → 7.996 MiB |
| Worker 3 memory, late in run → after completion | 8.035 → 8.035 MiB |
| Worker 4 memory, late in run → after completion | 7.953 → 7.941 MiB |

Memory is Docker `stats --no-stream` container memory, not a direct RSS measurement. These two samples
do not establish indefinite stability; the independent heap sampling run below checks growth throughout
the offered workload. This run observed no lost events, duplicate deliveries or retries.

## Four workers sharing one process

Hardware: AMD Ryzen 5 5600H host, Windows with Docker Desktop (Linux containers).
The load process was limited to four CPUs with Docker `--cpus 4` and GOMAXPROCS=4.
PostgreSQL 16 and Redis 7.2 were separate containers on the same Docker bridge.
Go 1.26.11. Four independent Worker instances with one delivery goroutine each,
60-second leases, 5-second sweeps; four bounded producers; local HTTP destination.

Run `EVENTRELAY_LOAD=1 go test -v -count=1 -timeout 120s ./tests/load` with
EVENTRELAY_TEST_DATABASE_URL and EVENTRELAY_TEST_REDIS_URL pointing at disposable
services. The harness creates and drops its own schema and Redis key.

| Measurement | Result |
|---|---:|
| Events delivered | 10,000 |
| Recorded attempts | 10,000 |
| Elapsed ingest through final durable delivery | 58.8384 s |
| Delivery rate | 10,197.41 / min |
| Ingest p50 | 2.983254 ms |
| Ingest p99 | 12.624854 ms |
| Heap bytes after GC at 2k / 4k / 6k / 8k / 10k events | 2,218,768 / 2,375,800 / 2,307,640 / 2,325,472 / 2,309,896 |

No lost events, duplicate attempts or sustained heap growth occurred in this run.
This is a one-minute local test, not evidence about indefinite workloads or remote destinations.
The four Worker instances share one process, including the producer and HTTP receiver; this is
not a four-process RSS measurement. Separate integration tests kill an OS worker process,
expire its lease, verify redelivery and reject the old lease's completion.

Earlier attempts exposed two harness limitations: using crash-test timing (50 ms sweeps) floods
dispatch under load, and routing every database round trip through Docker Desktop's host bridge
has much higher latency. A single producer also caps offered load. Those runs did not meet the
target. The final test uses production lease/sweep defaults and bounded parallel producers;
it asserts 10,000 durable deliveries in at most one minute and records memory samples.
