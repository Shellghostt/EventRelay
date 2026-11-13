---
project: EventRelay
slug: eventrelay
owner: shellghostt
status: mvp
version: 0.1.0
language: Go 1.26
license: Apache-2.0
rank: 1
focus: Backend infrastructure
depends_on:
  - PostgreSQL 16
  - Redis 7.2
  - Docker (development only)
created: 2026-11-13
updated: 2026-11-13
---

# EventRelay — Implementation Plan

> **This file is the specification.** If code and this document disagree, the document is wrong only if an
> ADR supersedes it. Update this file in the same pull request that changes reality.

---

## 0. Machine context

Read this block first. It is the fastest way to know what is true about this repository right now.

```yaml
project: EventRelay
status: mvp                      # skeleton | mvp | v1 | maintenance
version: 0.1.0
builds: true
functional: partial              # ingestion works end to end; nothing is delivered yet

implemented:
  - environment configuration with validation and redacted logging
  - structured logging and prometheus metrics on /metrics
  - postgres schema applied on start, recorded in schema_migrations
  - POST /v1/endpoints and GET /v1/endpoints behind a static admin bearer token
  - POST /v1/endpoints/{id}/events with HMAC-SHA256 verification and durable storage
  - GET /v1/events with cursor pagination plus status and endpoint_id filters
  - GET /healthz, GET /readyz (503 when postgres is unreachable)
  - api/openapi.yaml describing exactly the implemented paths
  - unit tests for signature vectors, config and ingest handler
  - integration tests for the store, gated on EVENTRELAY_TEST_DATABASE_URL

not_implemented:
  - redis streams transport
  - delivery workers
  - retry scheduler
  - dead letter queue
  - replay
  - delivery history and attempt endpoints
  - endpoint get, update, enable/disable and delete
  - api keys and multi-tenancy (a single implicit tenant exists)
  - idempotency enforcement (the header is accepted and ignored, see section 9.5)
  - dashboard
  - rate limiting
  - tracing
  - retention job
  - ssrf protection for destination urls

source_of_truth:
  scope_and_design: docs/PLAN.md          # this file
  architecture: docs/ARCHITECTURE.md
  decisions: docs/adr/
  api_contract: api/openapi.yaml
  schema: migrations/
  status: README.md#roadmap

forbidden_claims:
  - "exactly-once delivery"
  - "guaranteed delivery in order" (ordering is opt-in, M6)
  - any throughput or latency number not produced by tests/load or tests/bench
  - any feature listed under not_implemented
  - "production ready"

if_uncertain:
  - check migrations/ and internal/ for the current truth
  - if the plan does not answer it, write an ADR proposing an answer
  - never invent behaviour and never describe planned work in the past tense
```

---

## 1. What this is

EventRelay is self-hosted webhook infrastructure. An application posts an event to EventRelay; EventRelay
verifies it, stores it durably, and delivers it to destination endpoints with retries, replay and
observability.

One-line description: *Self-hosted webhook infrastructure with reliable delivery, event replay, retries and
observability.*

This is not a message broker for internal service-to-service traffic, and not a hosted SaaS.

---

## 2. Problem and users

**Problem.** Every product that emits webhooks rebuilds the same machinery: signature verification,
durable storage, retry with backoff, dead-letter handling, replay after an outage, delivery history,
per-tenant isolation. Doing it badly is invisible until a customer misses an order event.

**Users.**

| User | What they need |
|---|---|
| Platform engineer at a small company | Accept inbound events, deliver to customer endpoints reliably |
| Developer integrating a third party | Inspect what was sent, replay a failed event without a support ticket |
| Ops engineer | Know whether delivery is healthy, and where the backlog is |

**Success looks like.** A user can `docker compose up`, send a signed event, watch three failed attempts
follow the documented backoff schedule, see the event land in the dead-letter queue, fix their endpoint and
replay the event successfully — without reading the source code.

---

## 3. Non-goals

These are decisions, not omissions. Do not implement them without superseding this section.

- **Exactly-once delivery.** Not achievable against arbitrary third-party HTTP endpoints. Never claim it.
- **Being a general message broker.** If you need internal pub/sub, use NATS, Kafka or RabbitMQ.
- **A workflow engine.** No fan-out graphs, no conditional delivery, no transforms in v1.
- **Multi-region replication.** Single region, single writer database, in v1.
- **A hosted service.** Self-hosted only. No billing, no signup.
- **Email or SMS notification channels.** HTTP delivery only.
- **A Kubernetes operator.** Docker Compose and a documented container image are enough.

---

## 4. Scope contract

### v0.1 — MVP (M1–M3)

One tenant, multiple endpoints, signed ingestion, durable storage, retries, dead-letter queue, manual
replay via API. No dashboard, no rate limits, no ordering.

### v1.0 — public release (M4–M6)

Adds dashboard, API keys and tenant isolation, idempotency keys, rate limits, replay UI, tracing, load test
report, deployment documentation.

### Post-1.0 — explicitly deferred

Event ordering per endpoint (M6 stretch, behind a flag), payload transforms, batch ingestion, S3 archival,
OpenTelemetry collector deployment guidance, MySQL support.

> Anything not in v0.1 or v1.0 is **not** in the backlog and must not appear in the README roadmap.

---

## 5. Locked decisions

Changing any of these requires a new ADR in `docs/adr/`.

| # | Decision | Rationale |
|---|---|---|
| 1 | Go 1.26 | Long-lived connections, predictable concurrency, static binaries |
| 2 | PostgreSQL is the source of truth | Redis may be flushed; the durable record must survive it |
| 3 | Redis Streams is the transport, not the store | Consumer groups give at-least-once dispatch with minimal code |
| 4 | At-least-once, never exactly-once | Duplicates are made cheap with a stable `event_id` |
| 5 | HMAC-SHA256, timestamped | Standard, symmetric, 10 lines in any language, replay-resistant |
| 6 | One Go module, two binaries | Shared types beat premature deployability |
| 7 | Chi for routing, `pgx` for Postgres | Small surface, no framework, explicit SQL |
| 8 | Compose for development only | A compose file is not a deployment strategy |

**Rejected alternatives** (do not revisit without new evidence):

- Kafka instead of Redis Streams — operational weight unjustified for this scale.
- An ORM — hides the two queries that matter (lease acquisition, retry polling).
- Outbox pattern *in addition to* Redis Streams — redundant; the database already holds the event and the
  publisher poll is the outbox.
- gRPC ingestion — the audience is HTTP webhook senders.

---

## 6. Architecture

```mermaid
flowchart LR
    EXT[External application] -->|POST /v1/endpoints/:id/events| API[Ingest API]
    API --> SIG[Signature verification]
    SIG --> STORE[(PostgreSQL)]
    API -->|XADD| Q[Redis Stream]
    Q -->|consumer group| W[Worker pool]
    STORE --> W
    W --> DEST[Destination endpoint]
    DEST -->|2xx| OK[Mark delivered]
    DEST -->|retryable failure| RS[Retry scheduler]
    RS -->|attempts remaining| Q
    RS -->|exhausted| DLQ[(Dead letter queue)]
    DASH[Dashboard] --> MGMT[Management API]
    MGMT --> STORE
```

### Components

| Component | Responsibility | Must never |
|---|---|---|
| Ingest API | Authenticate, verify signature, validate, persist, publish | Deliver synchronously |
| Store | Owns `events`, `delivery_attempts`, `endpoints`, `api_keys` | Contain delivery logic |
| Stream | Hand work to workers, at-least-once | Be the only copy of an event |
| Worker | Lease an event, POST it, record the attempt, schedule the next one | Mark delivered before reading the response |
| Retry scheduler | Compute the next attempt time, move exhausted events to DLQ | Retry non-retryable status codes |
| Management API | Query, inspect, replay | Mutate event payloads |
| Dashboard | Read-only presentation + replay action | Bypass the management API |

### Flows

**Ingest.** `POST` → resolve endpoint by id → check tenant enabled → verify HMAC → parse `Idempotency-Key` →
`INSERT` event (`status=pending`) → `XADD` to stream → `202 Accepted {event_id}`.

If the insert succeeds and the publish fails, the event is still `pending` and the scheduler sweep picks it
up. A failed publish is never a lost event.

**Delivery.** Worker claims a `pending` event whose `next_attempt_at <= now()` using
`UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING *`, sets `status=in_flight`,
`lease_expires_at=now()+60s`. Posts to the destination. Records an attempt row. Then:

- 2xx → `status=delivered`, `delivered_at=now()`
- retryable → `status=pending`, `attempt_count++`, `next_attempt_at=now()+backoff`
- non-retryable → `status=dead_lettered`, insert `dead_letters`
- exhausted → `status=dead_lettered`

If the worker dies mid-attempt, `lease_expires_at` passes and the sweeper returns the event to `pending`.
This is why at-least-once is honest: the destination may have received the request before the worker died.

**Replay.** `POST /v1/events/:id/replay` → requires the event to be `delivered`, `failed` or
`dead_lettered` → sets `status=pending`, `next_attempt_at=now()`, increments `replay_count`, appends a
`delivery_attempts` row with `attempt_no = attempt_count + 1`. `event_id` never changes. Duplicate
suppression remains the consumer's job.

---

## 7. Data model

PostgreSQL 16. Forward-only migrations in `migrations/`.

```sql
CREATE TABLE tenants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE endpoints (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url               text NOT NULL,
    description       text NOT NULL DEFAULT '',
    ingest_secret     bytea NOT NULL,          -- verifies inbound events from the sender
    delivery_secret   bytea NOT NULL,          -- signs outbound requests to url
    enabled           boolean NOT NULL DEFAULT true,
    max_attempts      smallint NOT NULL DEFAULT 8 CHECK (max_attempts BETWEEN 1 AND 20),
    ordered           boolean NOT NULL DEFAULT false,   -- M6
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    endpoint_id       uuid NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
    event_type        text NOT NULL,
    payload           jsonb NOT NULL,
    idempotency_key   text,
    status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','in_flight','delivered','dead_lettered','canceled')),
    attempt_count     smallint NOT NULL DEFAULT 0,
    replay_count      smallint NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    lease_expires_at  timestamptz,
    leased_by         text,
    received_at       timestamptz NOT NULL DEFAULT now(),
    delivered_at      timestamptz,
    last_error        text
);

CREATE UNIQUE INDEX events_idempotency
    ON events (endpoint_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX events_due ON events (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX events_lease ON events (lease_expires_at)
    WHERE status = 'in_flight';

CREATE INDEX events_recent ON events (tenant_id, received_at DESC);

CREATE TABLE delivery_attempts (
    id                bigserial PRIMARY KEY,
    event_id          uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt_no        smallint NOT NULL,
    started_at        timestamptz NOT NULL,
    finished_at       timestamptz,
    outcome           text NOT NULL,          -- success | retryable | non_retryable | timeout | network
    response_status   integer,
    response_excerpt  text,                   -- first 1 KiB, redacted
    error             text,
    duration_ms       integer
);

CREATE UNIQUE INDEX attempts_unique ON delivery_attempts (event_id, attempt_no);

CREATE TABLE dead_letters (
    event_id    uuid PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    moved_at    timestamptz NOT NULL DEFAULT now(),
    reason      text NOT NULL,
    last_error  text
);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    prefix       text NOT NULL UNIQUE,        -- lookup, not secret
    hash         bytea NOT NULL,              -- argon2id of the full key
    label        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
```

Rules:

- No `SELECT *`. Column lists are explicit.
- Every query that returns tenant data filters by `tenant_id`.
- `payload` is `jsonb` because users inspect it; no schema enforcement beyond "valid JSON object". Because
  it is `jsonb`, the stored document is re-serialised by Postgres: key order and insignificant whitespace are
  not preserved. The signature is always verified against the raw request bytes, so this only affects what
  readers see.
- Payloads over 256 KiB are rejected at ingest with `413`.
- `response_excerpt` is capped at 1 KiB and passes through the redactor before storage.

---

## 8. API surface

Base: `/v1`. Auth: `Authorization: Bearer <api_key>` for management; HMAC header for ingestion.
Errors are RFC 9457 problem documents:

```json
{ "type": "https://eventrelay.dev/errors/invalid-signature", "title": "Invalid signature",
  "status": 401, "detail": "Signature timestamp is older than 300 seconds" }
```

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/v1/endpoints/{id}/events` | HMAC | Ingest one event |
| POST | `/v1/endpoints` | Bearer | Create endpoint, returns both secrets once |
| GET | `/v1/endpoints` | Bearer | List endpoints |
| GET | `/v1/endpoints/{id}` | Bearer | Get endpoint |
| PATCH | `/v1/endpoints/{id}` | Bearer | Update url, enabled, max_attempts |
| DELETE | `/v1/endpoints/{id}` | Bearer | Delete endpoint and its events |
| GET | `/v1/events` | Bearer | Cursor-paginated list, filters: status, endpoint_id, from, to |
| GET | `/v1/events/{id}` | Bearer | Event detail |
| GET | `/v1/events/{id}/attempts` | Bearer | Attempt history |
| POST | `/v1/events/{id}/replay` | Bearer | Re-queue an event |
| POST | `/v1/api-keys` | Bearer | Create key, returns plaintext once |
| DELETE | `/v1/api-keys/{id}` | Bearer | Revoke key |
| GET | `/healthz` | none | Liveness |
| GET | `/readyz` | none | Postgres + Redis reachable |
| GET | `/metrics` | none | Prometheus text format |

Implemented as of M1: the ingest path, `POST`/`GET /v1/endpoints`, `GET /v1/events`, `/healthz`,
`/readyz` and `/metrics`. Every other row in this table is planned; see section 0 for the authoritative
list.

**Ingest request**

```http
POST /v1/endpoints/8a6037e2-0221-46dc-9fa5-505eddf64974/events
X-EventRelay-Signature: t=1763000000,v1=5f2b...
X-Event-Type: order.created
Idempotency-Key: order_8891
Content-Type: application/json

{"order_id":"8891","total":4200}
```

`202 Accepted` → `{ "event_id": "634ceb6d-8184-49cc-8fcb-073954f783b1", "status": "pending" }`
`200 OK` (idempotent repeat) → same body, no new event
`401` bad signature · `404` unknown endpoint · `413` payload too large · `422` invalid JSON · `429` rate limited

Pagination is cursor-based (`?limit=50&cursor=<opaque>`), never offset. List responses include `next_cursor`.

---

## 9. Core algorithms and invariants

### 9.1 Signature scheme (both directions)

```
signed_payload = "v1:" + timestamp + ":" + raw_request_body
signature      = lower_hex( HMAC-SHA256( secret, signed_payload ) )
header         = "X-EventRelay-Signature: t=" + timestamp + ",v1=" + signature
```

Verification rules, in order:

1. Parse `t` and every `v1=` entry. Reject if no `v1` present.
2. Reject if `abs(now - t) > 300 seconds`.
3. Recompute over the **raw body bytes**, before any JSON parsing.
4. Compare with `hmac.Equal` (constant time) against each candidate. Accept on first match.
5. Two secrets may be active during rotation; the header may carry two `v1=` values.

Outbound requests add `X-EventRelay-Event-Id`, `X-EventRelay-Attempt`, and `User-Agent: EventRelay/0.1`.

### 9.2 Retry schedule

Base delays, indexed by failed attempt number:

| Failed attempt | Delay |
|---|---|
| 1 | 10 s |
| 2 | 30 s |
| 3 | 2 min |
| 4 | 10 min |
| 5 | 30 min |
| 6 | 2 h |
| 7 | 6 h |
| 8 | 24 h |

- Jitter: `delay * random(0.8, 1.2)`.
- `Retry-After` (seconds or HTTP date) overrides the base delay when it is **larger**, capped at 24 h.
- `max_attempts` is per endpoint, default 8, hard ceiling 20.
- Default total retry window ≈ 39 h for 8 attempts.

### 9.3 Outcome classification

| Condition | Classification |
|---|---|
| 2xx | success |
| 408, 425, 429 | retryable |
| 5xx | retryable |
| network error, DNS failure, TLS error | retryable |
| timeout (client deadline 10 s) | retryable |
| 410 Gone | non-retryable, disable endpoint |
| other 4xx | non-retryable |
| 3xx | non-retryable (redirects are not followed) |

### 9.4 Invariants

1. A stored event is never deleted except by endpoint deletion or retention policy.
2. `event_id` is immutable and identical across every attempt, replay and duplicate.
3. `delivery_attempts` is append-only. Never update an attempt row. An attempt without `finished_at` is an
   interrupted attempt.
4. Delivery never begins before the event is committed to Postgres.
5. A worker holds a lease for at most 60 s and renews it only while the HTTP call is in flight.
6. Every outbound request carries the same `event_id`, even on replay.
7. Nothing in the delivery path is allowed to block on Redis. Redis being down degrades throughput, not
   correctness.
8. Duplicate delivery is a documented possibility. Duplicate *loss* is a bug.

### 9.5 Idempotency (M5)

`Idempotency-Key` is scoped per endpoint, retained 24 h. A repeat insert hits the partial unique index; the
handler catches the conflict and returns the existing event. Never return `500` for a duplicate.

**Current behaviour (M1):** the header is accepted for forward compatibility and ignored. No key is stored,
so a repeat request creates a second event with a new `event_id`. The unique index exists in the schema but
stays unused. Do not describe ingest as idempotent until this section is implemented and its acceptance
criterion is ticked.

### 9.6 Ordering (M6, opt-in)

`endpoints.ordered = true` means: one in-flight event per endpoint, and event `N+1` is not claimed before
`N` is `delivered` or `dead_lettered`. This introduces head-of-line blocking — document it. Ordering is not
global, not per tenant, and not available for events that have been replayed out of sequence.

### 9.7 Rate limiting (M5)

Token bucket per tenant in Redis: `ingest_rate` (default 500/s, burst 1000) and `delivery_concurrency`
(default 50). Exceeding ingest returns `429` with `Retry-After`. If Redis is unavailable, rate limiting
fails open and increments `eventrelay_ratelimit_degraded_total`. Failing closed would turn a cache outage
into an outage of the product.

---

## 10. Repository layout

```
eventrelay/
├── cmd/eventrelay/         API binary
├── cmd/worker/             worker binary
├── internal/ingest/        handlers, signature verification, validation
├── internal/delivery/      HTTP client, outcome classification
├── internal/store/         pgx queries, transactions
├── internal/retry/         backoff, scheduling, DLQ promotion
├── internal/auth/          API keys, tenant resolution
├── internal/observability/ metrics, tracing, logging
├── pkg/hmacsign/           public sign/verify helpers
├── api/openapi.yaml        contract, only implemented paths
├── migrations/             forward-only SQL
├── web/                    React dashboard (M4)
├── deploy/                 compose, deployment notes
├── tests/integration/      real Postgres + Redis
├── tests/load/             sustained throughput
├── tests/bench/            micro benchmarks
└── docs/                   plan, architecture, ADRs
```

---

## 11. Configuration

Environment variables, prefix `EVENTRELAY_`. No config file in v1.

| Variable | Default | Notes |
|---|---|---|
| `EVENTRELAY_HTTP_ADDR` | `:8080` | API listen address |
| `EVENTRELAY_POSTGRES_DSN` | required | `postgres://user:pass@host:5432/db` |
| `EVENTRELAY_REDIS_URL` | required | `redis://host:6379/0` |
| `EVENTRELAY_WORKERS` | `8` | delivery goroutines per process |
| `EVENTRELAY_LEASE_SECONDS` | `60` | event lease duration |
| `EVENTRELAY_MAX_PAYLOAD_BYTES` | `262144` | 256 KiB |
| `EVENTRELAY_SIGNATURE_TOLERANCE_SECONDS` | `300` | replay window |
| `EVENTRELAY_HTTP_TIMEOUT_SECONDS` | `10` | outbound client timeout |
| `EVENTRELAY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `EVENTRELAY_LOG_FORMAT` | `json` | `json` or `text` |
| `EVENTRELAY_SWEEP_INTERVAL_SECONDS` | `5` | sweeper tick for due events and expired leases |
| `EVENTRELAY_RETENTION_DAYS` | `30` | delivered events older than this are pruned |
| `EVENTRELAY_OTEL_EXPORTER` | `none` | `none` or `otlp` |
| `EVENTRELAY_OTEL_ENDPOINT` | empty | collector address |

Never log secrets, full payloads (unless `EVENTRELAY_LOG_PAYLOADS=true` in development) or `Authorization`
headers.

---

## 12. Milestones

Each milestone ends with a tag, a CHANGELOG entry, and a demo that a stranger can run.

### M1 — Ingestion and durability

**Deliverable:** `POST /v1/endpoints/{id}/events` verifies HMAC, persists the event, returns `202`.
Management endpoints for creating and listing endpoints. Health and readiness. OpenAPI for these paths.

**Acceptance criteria**

- [x] Signed request with a valid signature returns `202` and a row exists in `events`
- [x] Tampered body returns `401` and writes nothing
- [x] Timestamp older than 300 s returns `401`
- [x] Missing `v1` in header returns `401`
- [x] Unknown endpoint returns `404`; oversized payload returns `413`
- [ ] `go test -race ./...` green; integration tests use a real Postgres
- [x] `/readyz` returns `503` when Postgres is down
- [x] `api/openapi.yaml` validates and documents every implemented path

Status note: everything except the race run is verified on a local Windows machine against PostgreSQL 16.
`go test ./...` passes and the integration tests run against a real database; `-race` needs cgo and the local
toolchain cannot build it, so it is only proven when CI runs. Integration tests skip when
`EVENTRELAY_TEST_DATABASE_URL` is unset.

**Demo:** `docker compose up`, curl a signed event, `psql` shows the row.

### M2 — Transport and workers

**Deliverable:** events published to a Redis Stream; a worker pool claims leases, delivers over HTTP, and
records attempts. Horizontal scaling via `docker compose up --scale worker=4`.

**Acceptance criteria**

- [ ] A pending event is delivered within 1 s of ingest on an idle system
- [ ] Worker killed mid-flight (`SIGKILL`) → event reappears after lease expiry, no loss
- [ ] Two workers compete for the same event and exactly one delivers it per lease
- [ ] `delivery_attempts` contains one row per attempt with outcome and duration
- [ ] Destination returning 500 causes a retry; destination returning 400 causes dead-letter
- [ ] 4 workers sustain 10,000 events/min in `tests/load` with no unbounded memory growth

**Demo:** a local endpoint that fails twice then succeeds; the attempt list shows three attempts.

### M3 — Retry policy and dead-letter queue

**Deliverable:** the documented backoff schedule, `Retry-After` support, DLQ with reason, sweeper for due
events and expired leases.

**Acceptance criteria**

- [ ] Backoff matches the table ±20% jitter, verified by a unit test with a fake clock
- [ ] `Retry-After: 3600` delays the next attempt by one hour
- [ ] Attempt 8 failure moves the event to `dead_letters` with a reason
- [ ] Sweeper is idempotent: running it twice does not double-schedule
- [ ] Non-retryable 4xx skips remaining attempts

**Demo:** `EVENTRELAY_` override of the schedule to seconds, watch an event exhaust and land in DLQ.

### M4 — Inspection, replay and dashboard

**Deliverable:** event list/detail/attempt endpoints, replay, and a React dashboard showing endpoint
health, recent events, attempt timelines and a replay button.

**Acceptance criteria**

- [ ] Replay sets `status=pending`, keeps `event_id`, appends attempt `n+1`
- [ ] Replay of an `in_flight` event returns `409`
- [ ] Dashboard shows delivery success rate over the last 24 h from real data
- [ ] Cursor pagination survives inserts during iteration (no duplicates, no gaps for older rows)
- [ ] Dashboard has one screenshot in the README, taken from a real local run

### M5 — Tenancy, keys, limits, idempotency

**Deliverable:** tenants, API keys (hashed, prefix lookup), per-tenant rate limits, idempotency keys, key
rotation with two active secrets.

**Acceptance criteria**

- [ ] A key from tenant A cannot read tenant B's events (integration test proves it)
- [ ] Revoked keys return `401` immediately
- [ ] Duplicate `Idempotency-Key` returns `200` with the original `event_id`
- [ ] Ingest above the limit returns `429` with `Retry-After`
- [ ] Redis down: ingestion still succeeds (fail-open) and the degraded metric increments
- [ ] Secrets are never returned after creation; only the prefix is listed

### M6 — Production posture

**Deliverable:** load test report with real numbers, OpenTelemetry traces, backpressure handling, ordering
option, deployment guide, retention job, v1.0.0 tag.

**Acceptance criteria**

- [ ] `tests/load` report committed under `docs/benchmarks/` with hardware described
- [ ] Traces span ingest → publish → attempt with the same `event_id`
- [ ] Sustained ingest above capacity produces `429`, not OOM
- [ ] Ordered endpoint delivers strictly in sequence, documented with its latency cost
- [ ] Deployment guide tested on a clean VM
- [ ] `CHANGELOG.md` has v1.0.0 and every feature is marked complete in the README roadmap

---

## 13. Backlog

PR-sized units, ordered. `ER-` prefix for issue titles.

| ID | Task | Depends | Milestone |
|---|---|---|---|
| ER-001 | Chi router, config loader, structured logging | — | M1 |
| ER-002 | `migrations/0001_init.sql` + migration runner | — | M1 |
| ER-003 | `pkg/hmacsign` with test vectors from the spec | — | M1 |
| ER-004 | `POST /v1/endpoints` and secret generation | ER-002 | M1 |
| ER-005 | Ingest handler: validation, size limit, 202 | ER-003, ER-004 | M1 |
| ER-006 | `GET /v1/events` with cursor pagination | ER-005 | M1 |
| ER-007 | `/healthz`, `/readyz`, `/metrics` | ER-001 | M1 |
| ER-008 | OpenAPI document for M1 paths | ER-005 | M1 |
| ER-009 | Integration test harness (testcontainers or compose) | ER-002 | M1 |
| ER-010 | Redis Stream publisher + consumer group setup | ER-005 | M2 |
| ER-011 | Lease acquisition query with `SKIP LOCKED` | ER-010 | M2 |
| ER-012 | Delivery client with timeout and classification | ER-011 | M2 |
| ER-013 | Attempt recorder | ER-012 | M2 |
| ER-014 | Worker process and graceful shutdown | ER-012 | M2 |
| ER-015 | Lease expiry sweeper | ER-011 | M2 |
| ER-016 | Kill -9 recovery integration test | ER-015 | M2 |
| ER-017 | Backoff calculator with injectable clock | ER-013 | M3 |
| ER-018 | `Retry-After` parsing (seconds and HTTP date) | ER-017 | M3 |
| ER-019 | DLQ promotion and `dead_letters` writes | ER-017 | M3 |
| ER-020 | Due-event sweeper | ER-017 | M3 |
| ER-021 | `GET /v1/events/:id` and `/attempts` | ER-006 | M4 |
| ER-022 | Replay endpoint with state guards | ER-021 | M4 |
| ER-023 | Dashboard scaffolding and API client | ER-021 | M4 |
| ER-024 | Endpoint health and event list views | ER-023 | M4 |
| ER-025 | Attempt timeline and replay button | ER-024 | M4 |
| ER-026 | API key creation, hashing, prefix lookup | ER-004 | M5 |
| ER-027 | Tenant scoping middleware and leak tests | ER-026 | M5 |
| ER-028 | Idempotency key handling | ER-005 | M5 |
| ER-029 | Redis token bucket rate limiter | ER-026 | M5 |
| ER-030 | Secret rotation with two active signatures | ER-003 | M5 |
| ER-031 | OpenTelemetry traces across ingest and delivery | ER-012 | M6 |
| ER-032 | Backpressure: bounded queue, 429 on saturation | ER-029 | M6 |
| ER-033 | Ordered delivery behind a flag | ER-011 | M6 |
| ER-034 | Load test scenarios and report | ER-014 | M6 |
| ER-035 | Retention job for delivered events | ER-002 | M6 |
| ER-036 | Deployment guide and production checklist | ER-030 | M6 |

---

## 14. Testing strategy

| Layer | Scope | Tooling |
|---|---|---|
| Unit | backoff math, signature parsing, classification, pagination encoding | `go test`, table-driven |
| Integration | HTTP → Postgres → Redis → HTTP, real containers | `go test`, compose, `-race` |
| Chaos | worker `SIGKILL`, Redis restart, Postgres failover | scripted compose scenarios |
| Load | sustained ingest and delivery | Go load generator in `tests/load` |
| Contract | OpenAPI matches handlers | `openapi-diff` in CI |

**Performance targets** (measured on 4 vCPU / 8 GB, documented with the run):

- Ingest: ≥ 2,000 events/s sustained for 5 minutes, p99 < 50 ms
- Enqueue → first delivery attempt: p99 < 250 ms
- Delivery throughput: ≥ 10,000 events/min with 4 workers
- Crash test: 10,000 events, kill a worker every 10 s, zero events lost, duplicates allowed and counted
- Idle memory: < 150 MB per process

A performance claim without a committed report is not a performance claim.

---

## 15. Observability

**Metrics** (Prometheus naming, all with `eventrelay_` prefix):

```
eventrelay_events_ingested_total{result}
eventrelay_ingest_duration_seconds{quantile}
eventrelay_events_delivered_total{endpoint,status_class}
eventrelay_delivery_attempts_total{outcome}
eventrelay_delivery_duration_seconds{endpoint}
eventrelay_retries_scheduled_total{attempt}
eventrelay_dead_letters_total{reason}
eventrelay_dlq_size{tenant}
eventrelay_stream_lag{stream,group}
eventrelay_worker_inflight
eventrelay_leases_expired_total
eventrelay_ratelimit_degraded_total
```

**Logs:** JSON, one line per attempt with `event_id`, `attempt`, `outcome`, `status_code`, `duration_ms`.
Never payload contents at `info`.

**Traces:** `ingest.receive` → `ingest.verify` → `event.persist` → `stream.publish` → `delivery.attempt`,
all carrying `eventrelay.event_id` as a span attribute.

**Alerts** (documented in `deploy/`, not implemented in code):
DLQ size rising for 15 min · stream lag > 1,000 for 5 min · delivery error rate > 5% for 10 min.

---

## 16. Security and threat model

| Threat | Mitigation |
|---|---|
| Forged events | HMAC over raw body with per-endpoint secret, 300 s window |
| Replay of a captured request | Timestamp window + optional `Idempotency-Key` |
| SSRF via endpoint URL | Block link-local, loopback and metadata ranges by default; `EVENTRELAY_ALLOW_PRIVATE_TARGETS=true` to opt out, documented as dangerous |
| Secret leakage in logs | Secrets never logged; header redaction in the HTTP middleware |
| Cross-tenant read | Every query filters by `tenant_id`; leak tests in M5 |
| Timing attack on signature | `hmac.Equal` only |
| API key theft | Argon2id at rest, prefix lookup, revocation, `last_used_at` visibility |
| Response body exfiltration | 1 KiB cap + redaction before storage |
| Unbounded resource use | Payload cap, per-tenant rate limit, bounded worker queue |
| Dependency compromise | `govulncheck` in CI, `go.sum`, pinned base images |

Non-goals for security: no WAF, no IP allowlisting in v1, no mutual TLS to destinations.

---

## 17. Deployment and quickstart

**Development**

```bash
docker compose -f deploy/docker-compose.yml up -d postgres redis
make bootstrap
make run
```

**Production (v1.0):** container images built by CI, Postgres 16 with `pg_stat_statements`, Redis 7.2 with
AOF, at least two API replicas behind a load balancer, workers scaled independently. Migrations run as a
separate job, never on API startup in production. Backups: `pg_dump` daily plus WAL archiving.

**Operations runbook** (written at M6): how to raise worker count, how to drain, how to replay a day of
events, how to restore from backup.

---

## 18. Open-source standards

- **License:** Apache-2.0 (patent grant matters for infrastructure).
- **Commits:** Conventional Commits. **Releases:** SemVer, tagged by CI.
- **CI:** build, `go vet`, `go test -race`, lint, `govulncheck`, OpenAPI validation. `main` is always green.
- **Issues:** `bug`, `enhancement`, `good first issue`, `adr`, `docs`. Every `good first issue` lists the
  files to touch and the test to write.
- **ADRs:** one per irreversible decision, never edited after acceptance.
- **Docs:** README, CONTRIBUTING, SECURITY, CHANGELOG, `docs/PLAN.md`, `docs/ARCHITECTURE.md`,
  `docs/benchmarks/`.
- **No vanity metrics.** No star counts, no "trusted by", no fake logos.

---

## 19. Risks and open questions

| Risk | Impact | Mitigation |
|---|---|---|
| Retry storm after an outage | Destination overload, self-DoS | Jitter, `Retry-After`, per-endpoint concurrency cap |
| Postgres becomes the bottleneck | Ingest latency | Batch insert path, partitioning by month at 50M rows |
| Redis loss | Delivery stall | Events remain `pending` in Postgres; sweeper republishes |
| Duplicate deliveries confuse customers | Support load | Documentation with a copy-paste idempotency example |
| Scope creep into a workflow engine | Never ships | Section 3 is enforceable in review |
| SSRF blocks legitimate internal endpoints | Adoption friction | Explicit, documented opt-out |

**Open questions** (answer with an ADR before the relevant milestone):

1. Should secrets be encrypted at rest with a KMS key, or is Postgres disk encryption sufficient for v1?
2. Does replay reset `attempt_count`, or continue it? *Current answer: continue, so history stays linear.*
3. Is per-endpoint ordered delivery worth the head-of-line blocking in v1.0, or should it stay post-1.0?

---

## 20. Anti-hallucination contract

This section exists because AI assistants and future maintainers routinely describe planned work as done.

**Facts you may rely on:**

- The repository builds and the two binaries print their version.
- There is no HTTP server, no database connection, no queue connection.
- Every file in `internal/` and `pkg/` is currently a placeholder directory.
- `api/openapi.yaml` has an empty `paths:` object and must stay that way until a path is implemented.

**Rules:**

1. Do not write "EventRelay delivers webhooks". It does not. Write "will deliver" or omit it.
2. Do not add a badge, benchmark or screenshot that was not produced by this repository.
3. When you implement something, update **all** of: this file's `implemented`/`not_implemented` lists,
   `README.md` roadmap, and `CHANGELOG.md`. In the same pull request.
4. Never describe at-least-once as exactly-once. Never write "guaranteed" about delivery ordering.
5. If you are unsure whether a behaviour exists, `grep` for it. If it is not in the code, it does not exist.
6. Prefer deleting a stale claim over adding a disclaimer.

**Glossary — use these words, not synonyms:**

| Term | Meaning |
|---|---|
| tenant | Billing/ownership boundary. Has many endpoints. |
| endpoint | A destination URL plus two secrets (ingest, delivery). |
| event | One immutable payload accepted for one endpoint. Has one `event_id` forever. |
| attempt | One HTTP request made while trying to deliver an event. Append-only. |
| delivery | The overall lifecycle of an event from ingest to `delivered` or `dead_lettered`. |
| replay | Re-queueing an existing event. Does not create a new event. |
| dead letter | An event that exhausted attempts or hit a non-retryable status. |

Never use "message", "job", "webhook" and "delivery" interchangeably. A webhook is the integration; an
event is the record; a delivery is the process.

---

## 21. Handoff protocol

For a new maintainer or a new AI session, in order:

1. Read this file, sections 0, 3, 4 and 9. Budget 10 minutes.
2. Run `make build`, `./bin/eventrelay version`, `make test`. Compare the result against section 0's
   `functional` field. If they disagree, the code is newer than this document; update the document.
3. Run `git log --oneline -20` and read the last merged PR description.
4. Check the milestone table in section 12 and the checkboxes inside the current milestone. Those checkboxes
   are the definition of done.
5. Read `docs/adr/` before proposing a design change.
6. Pick the next unchecked item in the current milestone, in backlog order. Do not skip ahead to a later
   milestone, even if it looks more interesting.
7. Before opening a PR: `make lint`, `make test`, update status fields, add the CHANGELOG line.

**When in doubt:** the code is truth for "what exists", this document is truth for "what is intended", and
an ADR is the only way to change the second one.
