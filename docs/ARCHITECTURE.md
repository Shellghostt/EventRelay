# EventRelay Architecture

**Status: M2 merged into main; M3 implemented on `feat/m3-implementation`, not merged.** Ingestion,
Redis transport, HTTP delivery and the M3 retry policy exist. Replay, tenancy and the dashboard remain
planned. See [ADR 0004](adr/0004-m2-delivery.md) and [ADR 0005](adr/0005-m3-retry-policy.md).

## Context

Self-hosted webhook infrastructure with reliable delivery, event replay, retries and observability.

Sending a webhook is easy for one request and hard for a million. Every team that emits events
eventually rebuilds the same machinery: retries with backoff, a dead-letter queue, replay, signature
verification, delivery history and per-tenant isolation. Hosted options exist; a self-hosted one that you
can read end to end does not really exist as a mature open-source project. EventRelay is that project.

## Intended shape

```mermaid
flowchart LR
    EXT[External application] -->|POST event| API[Ingest API]
    API --> SIG[Signature verification]
    SIG --> STORE[(PostgreSQL event store)]
    API --> Q[Redis Stream]
    Q --> W[Worker pool]
    STORE --> W
    W --> DEST[Destination endpoint]
    DEST -->|2xx| OK[Mark delivered]
    DEST -->|failure| RS[Retry scheduler]
    RS -->|attempts remaining| Q
    RS -->|exhausted| DLQ[(Dead letter queue)]
    DASH[Admin dashboard] --> MGMT[Management API]
    MGMT --> STORE
```

## Component responsibilities

See [PLAN.md](PLAN.md) section 6 for the responsibility of each box, the request and event flows, and the
failure behaviour of each stage.

The implemented M2 path is API → PostgreSQL commit → bounded Redis stream → worker → signed HTTP.
A lease and its attempt row are committed before HTTP begins. PostgreSQL fences completion with the
random token, attempt number and lease expiry checked after acquiring the row lock. The attempt closes
in the same transaction that marks delivery, schedules a fixed-delay retry or records a dead letter.

Each worker process runs a finite number of delivery goroutines and one sweeper. The sweeper closes
expired attempts and republishes bounded pages of due events. Redis messages contain tenant/event IDs;
they are acknowledged after claim, and missing/duplicate messages are resolved from the durable state.
Redis consumer metadata uses one shared consumer identity; exclusivity comes from PostgreSQL row leases.

HTTP destinations are checked at DNS resolution and the selected IP is dialed directly. No redirects or
environment proxies are followed. Headers, response reads, HTTP time and database operations are bounded.
Response content is discarded. Readiness checks both backing services; liveness is independent of them.

The diagram also shows planned management and dashboard components. M3 replaces the M2 fixed 10-second
retries with the documented schedule, jitter, Retry-After and stable terminal reasons
([ADR 0005](adr/0005-m3-retry-policy.md)). M2 tests and measured limits are recorded in
[testing/m1-m2-validation.md](testing/m1-m2-validation.md); M3 acceptance evidence is recorded in
[testing/m3-retry-policy.md](testing/m3-retry-policy.md).

## What is deliberately absent

See [PLAN.md](PLAN.md) section 4 (non-goals). If a component is not on that diagram, it is not part of the
design.

## M3

M3 (retry policy and dead-letter handling) is implemented on `feat/m3-implementation` under
[ADR 0005](adr/0005-m3-retry-policy.md) with acceptance evidence in
[testing/m3-retry-policy.md](testing/m3-retry-policy.md); it is not merged into `main`. The
[phase requirements](phases/M3.md) hold the contract, and [development tools](DEVELOPMENT-TOOLS.md)
record actual tooling coverage.
