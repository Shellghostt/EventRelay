# Local delivery demo

Requires Docker Desktop (Linux containers) and Python 3. The Compose file is for development;
production deployment guidance is scheduled for M6.

From the repository root, in PowerShell:

```powershell
$env:EVENTRELAY_ADMIN_TOKEN='dev-admin-token-change-me'
$env:EVENTRELAY_PG_PORT='55437'
$env:EVENTRELAY_REDIS_PORT='56381'
$env:EVENTRELAY_API_PORT='58080'
$env:EVENTRELAY_WORKERS='1'
$env:EVENTRELAY_ALLOW_PRIVATE_TARGETS='true'
docker compose -p m2-eventrelay -f deploy/docker-compose.yml up -d --build --scale worker=4
python tests/demo.py --api http://127.0.0.1:58080
```

On Bash, use `export NAME=value` for the same variables. The receiver listens on port 18081 and verifies
each outbound signature using the delivery secret. It returns HTTP 500 twice, then HTTP 204. After
roughly 20–30 seconds, the script prints `status: delivered` and `attempts: [1, 2, 3]`.
The API and four workers are separate processes. `EVENTRELAY_WORKERS` is concurrency **per process**.

Docker Desktop resolves `host.docker.internal` to the host. On Linux Docker Engine, give the workers a
host-gateway mapping through a local Compose override:

```yaml
services:
  worker:
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

Pass that override with a second `-f` argument. If the receiver port changes, set both `--port` and
`--target` when invoking the demo. The private-target option must be enabled only for local testing.

Read attempts directly until the M4 inspection API is available:

```powershell
docker compose -p m2-eventrelay -f deploy/docker-compose.yml exec -T postgres psql -U eventrelay -c 'SELECT event_id,attempt_no,outcome,response_status,duration_ms FROM delivery_attempts ORDER BY id;'
```

M2 retries after a fixed 10-second delay, plus at most one sweep interval. HTTP 400 dead-letters the event;
HTTP 410 also disables its endpoint. Failed network calls and HTTP 500 retry until `max_attempts`.
Backoff, jitter, Retry-After and replay are later milestones. PostgreSQL remains authoritative when Redis
is unavailable; `/readyz` returns 503, ingestion can still persist events, and dispatch resumes after Redis
returns. Response content is discarded and not exposed in attempt records.

## Validation

Unit/store/transport integration tests, including an actual killed worker process:

```powershell
$env:GOTOOLCHAIN='go1.26.11'
$env:EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@127.0.0.1:55437/eventrelay?sslmode=disable'
$env:EVENTRELAY_TEST_REDIS_URL='redis://127.0.0.1:56381/0'
go test -count=1 -timeout 120s ./...
```

The test harness creates and removes an isolated PostgreSQL schema and Redis key for each delivery test.
The original M1 store test uses the default schema; use a disposable database for the full suite.
The Linux container below supports the race detector when the native Windows C toolchain does not:

```powershell
docker run --rm --network m2-eventrelay_default -v "${PWD}:/src:ro" -v m2-go-mod:/go/pkg/mod -v m2-go-build:/root/.cache/go-build -w /src -e GOTOOLCHAIN=go1.26.11 -e EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@postgres:5432/eventrelay?sslmode=disable' -e EVENTRELAY_TEST_REDIS_URL=redis://redis:6379/0 golang:1.26.11 go test -race -count=1 -timeout=120s ./...
```

To test 10,000 events/min through the running API and four worker processes:

```powershell
docker run --rm --name m2-eventrelay-load --network m2-eventrelay_default --cpus 4 -v "${PWD}:/src:ro" -v m2-go-mod:/go/pkg/mod -v m2-go-build:/root/.cache/go-build -w /src -e GOTOOLCHAIN=go1.26.11 -e EVENTRELAY_LOAD_API_URL=http://api:8080 -e EVENTRELAY_LOAD_TARGET_HOST=m2-eventrelay-load -e EVENTRELAY_LOAD_ADMIN_TOKEN=dev-admin-token-change-me -e EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@postgres:5432/eventrelay?sslmode=disable' golang:1.26.11 go test -v -run '^TestComposeSustainedDelivery$' -count=1 -timeout=120s ./tests/load
```

This test creates one endpoint, verifies all received signatures and IDs, counts durable deliveries and
attempts, then deletes only that endpoint and its dependent test records. The independent in-process load
test (`EVENTRELAY_LOAD=1`, `TestSustainedDelivery`) samples heap growth under a four-CPU limit.
See [measured results](../docs/benchmarks/m2-delivery.md) and [the validation record](../docs/testing/m1-m2-validation.md).

Stop the demo without deleting its data:

```powershell
docker compose -p m2-eventrelay -f deploy/docker-compose.yml down
```
