# ADR 0006 — M4 replay accounting, inspection and browser access

- Status: accepted for `feat/m4-implementation`
- Date: 2026-11-17
- Implementing agent: Codex, following the user's EventRelay M4 implementation request
- Required by: [M4 requirements](../phases/M4.md), ER-021–ER-025 and ER-021A
- Would extend: ADRs 0004/0005 for additional delivery cycles and endpoint management; initial M3 policy remains

## Context

M3 claim increments lifetime `attempt_count`, inserts that numbered attempt and requires the count to be
below endpoint `max_attempts`. Fenced completion/recovery consumes that attempt and can exhaust the event.
The old PLAN replay sketch continued the counter without introducing a new budget, used an absent
`failed` status and suggested appending an attempt in the API. That cannot make an exhausted event claimable
and would bypass the worker's attempt/lease transaction. M2/M3 also disable endpoints on HTTP 410, but
there is no management update path yet. Destination response content has deliberately never been stored.

## Decisions

1. Keep the existing event ID and lifetime attempt numbering. Model an initial delivery cycle and each
   explicit replay with a separate bounded attempt counter/budget. Snapshot endpoint `max_attempts` for
   each cycle; use the cycle's failed attempt number as input to the unchanged M3 retry calculator.
   Archive prior cycle status/reason and the attempt range. This distinguishes delivery history from
   scheduling budget and lets a final interrupted replay attempt exhaust its own cycle.
2. Replay accepts `delivered` or `dead_lettered`, with an enabled owned endpoint and no active lease.
   Require an expected replay version/count in the request. Lock/recheck state and version, advance once,
   record the action and set pending/due state atomically. Stale/concurrent requests return conflict.
   Do not create an HTTP attempt until the next claim. M5 ingest idempotency is unaffected.
3. Current DLQ membership ends when replay commits, while prior terminal evidence remains queryable by
   cycle. A later terminal result records a new historical reason using the same three M3 identifiers.
   Redis publication is best effort after commit; existing durable due-event republication handles loss.
4. Introduce secret-free endpoint GET/PATCH for URL/enabled recovery. Define an endpoint revision and a
   consistent lock/check strategy so old-destination 410 completion cannot disable a corrected revision.
   New max-attempt settings, if exposed, affect future cycles; active-cycle budgets do not shrink silently.
5. Keep existing list/cursor responses compatible. New attempt pages use stable lifetime attempt-number
   ordering with bounded limits. New statistics count actual persisted data in a declared UTC window:
   success / finished HTTP-response attempts, with network/policy/expired-lease counts shown separately.
   No response body/header archive or proactive health probe is added by this design.
6. Use authenticated same-origin browser access with credentials held outside compiled assets and browser
   persistence. Choose a server-side session or an authenticated proxy; cookie mutations require CSRF/
   origin protection. The API retains its static bearer-token contract and tenant scoping.

## Concrete contracts

- Widen lifetime attempt/replay counters and attempt numbers to PostgreSQL `integer`, represented as
  Go `int32`. The maximum is 2,147,483,647, exactly representable in JavaScript. Events gain a current
  cycle attempt count, frozen 1–20 budget, start and nullable finish time. Attempts gain cycle number,
  cycle attempt number and the claimed endpoint management revision. Migration 0004 maps existing
  M3 data to cycle zero with its existing attempt count/budget and observed timestamps; it creates no
  replay or attempt. Missing historical timestamps stay unknown.
- `POST /v1/events/{id}/replay` requires JSON `expected_replay_count`. Only delivered/dead-lettered
  events qualify. Lock the event, then share-lock its owned endpoint. Require enabled, no lease, the
  expected count and enough integer capacity for the entire new budget. Archive the prior completed
  cycle in `delivery_cycles`, remove active `dead_letters` membership, advance once and persist the
  database due time. Return 202. A duplicate/stale request or disabled/nonterminal/counter guard returns
  409; missing/foreign IDs return the same 404. The API does not create or publish an HTTP attempt:
  the durable sweeper publishes the due event independently of Redis availability.
- All delivery/replay transactions lock events before endpoints. Claims share-lock the endpoint before
  committing its URL/revision snapshot; endpoint PATCH locks only that endpoint. PATCH accepts only
  `expected_revision`, `url` and `enabled`, increments the management revision and never edits budgets
  or secrets. Automatic 410 disabling is conditional on the claimed management revision and does not
  advance that operator revision. An explicit subsequent PATCH supersedes old-destination 410 results.
  Disabling retains pending events and existing leases; re-enabling lets durable sweeps resume them.
- Stop/drain all M3 API/worker binaries before applying 0004 and starting M4. A database transition
  trigger additionally requires the transaction-local `eventrelay.worker_schema_version=4` marker for
  claims, completion/recovery and replay. M4 sets it inside each relevant transaction. Old binaries
  cannot commit delivery transitions after cutover. This is a compatibility fence, not database-user
  authentication. Existing leases can be recovered under the unchanged M3 expiry policy.
- New reads have a two-second request/database deadline, default 50/max 200 items and a 1-MiB JSON
  response bound. Event detail caps inspected payload at 512 KiB; larger payloads are explicitly
  unavailable, never truncated into misleading JSON. Diagnostics cap at 2,048 characters with a
  truncation flag. Attempts and previous cycles are independently paginated in ascending stable
  lifetime/cycle order. Response bodies/headers remain unavailable. Legacy event/endpoint lists stay
  compatible: the event list currently includes payloads, so M4 adds `view=summary`; the dashboard
  always uses that view. Endpoint pagination is opt-in through limit/cursor parameters.
- New cursors are HMAC-bound to route, tenant, filters and sort position. Existing legacy event cursors
  are unchanged. Optional `from`/`to` filters accept ordered RFC3339 UTC instants; filtered event cursors
  use the new bound format. A changed filter/route or malformed cursor returns 422.
- Endpoint statistics count finished attempts in `[database-now - 24h, database-now)`. Numerator is
  successful attempts with an HTTP response; denominator is all attempts with a response, including
  broken-body responses whose outcome is network. No-response attempts are separate. A zero denominator
  gives null. An indexed query reads at most 100,001 attempts; exceeding 100,000 makes the aggregate
  explicitly unavailable. Deadlines fail visibly. These are observed HTTP attempts, including replay
  cycles, not endpoint probes or unique-event delivery probabilities.
- Serve an embedded React application at `/dashboard/`. Login exchanges the entered admin token for
  a random HttpOnly/SameSite=Strict cookie and a separate in-memory CSRF nonce. Sessions expire after
  30 minutes, are capped at 256, are revoked on logout and are lost on API restart. Login/unsafe requests
  require an exact origin; mutations also require the nonce. An optional `EVENTRELAY_DASHBOARD_ORIGIN`
  pins the origin for TLS proxies; unconfigured plain HTTP is allowed only on loopback hosts. Secure
  cookies apply to HTTPS. The session router allows inspection, endpoint PATCH and replay only; it
  cannot create secrets, ingest events or authorize the legacy bearer API. No admin token/session is
  placed in assets, URLs or browser storage. No distributed sessions or production posture is claimed.
- The client polls after five seconds with at most one active request per resource, aborts on navigation,
  suppresses stale responses, pauses hidden tabs and retains only a bounded current page/cursor history.
  Payload is fetched on explicit detail inspection. Captured metadata is rendered as text under a
  same-origin CSP; no third-party fonts, analytics or active HTML are loaded.
- Pin React/ReactDOM 19.3.1, Vite 8.3.8, TypeScript 7.0.2, Vitest 5.1.0, Playwright 1.65.1,
  Testing Library React 16.3.3 and jsdom 31.0.1. Node 22.22.3 satisfies their declared engines.
  Commit source, lockfile and reproducible embedded assets so Go-only builds/tests work offline from
  a checkout. Frontend CI rebuilds and checks assets for drift, then tests/validates them; generated
  assets are not the reviewed source. Direct/transitive license and vulnerability checks accompany it.

## Alternatives considered

- Reset `attempt_count`: makes the current claim check usable, but collides with retained attempt numbers
  and destroys the counter's historical meaning unless schema/fence semantics also change.
- Continue lifetime count with the original maximum: keeps history but leaves exhausted replay pending
  forever. Raising the maximum ad hoc also skips early backoff and changes the endpoint policy.
- Create a new event: gives a fresh budget but breaks stable replay identity and receiver deduplication.
- Put the admin token in frontend storage: simple to scaffold but turns a text-rendering mistake into
  persistent management-token exposure. Prefer bounded server/session or proxy access.

## Consequences and validation

This ADR extends 0004 for endpoint management/410 revision guarding and 0005 for separate bounded
delivery cycles; it preserves initial retry math, classification, random/clock fences, secrets and
legacy API contracts. Acceptance still needs reviewed state/concurrency cases, an upgrade test,
unchanged M3 regressions, seeded statistics and real-browser checks. An accepted design is not evidence
that implementation or its acceptance gates have passed. Publication/merge are separate decisions.
