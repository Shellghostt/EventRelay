# ADR 0001 - Initial technology and scope decisions

- Status: accepted
- Date: 2026-11-13
- Deciders: Aditya

## Context

EventRelay is a greenfield open-source project. Before any code exists, the expensive mistakes to avoid are
scope creep and undocumented stack choices that a future contributor will try to reverse.

## Decisions

1. **Go 1.23 over TypeScript or Python** - the workload is long-lived connections plus predictable concurrent fan-out; goroutines and channels express the worker model in less code, and a static binary simplifies deployment.
2. **PostgreSQL is the source of truth, Redis Streams is the transport** - the durable record must survive a Redis flush. Redis is a dispatcher, not a storage engine; anything only in Redis is by definition temporary.
3. **At-least-once delivery, never exactly-once** - exactly-once delivery to an arbitrary third-party HTTP endpoint is not achievable. Duplicates are made cheap instead: event_id is stable across every attempt, and the docs never promise otherwise.
4. **HMAC-SHA256 with a timestamped signature** - symmetric, standard, verifiable by any language in about ten lines, and the timestamp gives replay protection without state.
5. **One Go module, two binaries (api, worker)** - shared types matter more than independent deployability at this size. Splitting into separate modules later is mechanical.
6. **Docker Compose is for development only** - a compose file is not a deployment strategy. Production guidance is written and tested at M6.

## Consequences

- The project ships one narrow capability that actually works rather than a broad surface of stubs.
- Contributors have a reference point: changing any decision above requires a superseding ADR, not a
  silent pull request.
- Reversing a decision later is allowed but must be written down with the reason and the migration cost.

## Follow-up

- ADR 0002 is created at M1, recording the concrete schema and interface decisions made while building.
