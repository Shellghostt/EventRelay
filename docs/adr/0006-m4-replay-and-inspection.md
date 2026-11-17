# ADR 0006 — M4 replay accounting, inspection and browser access

- Status: proposed; documentation only
- Date: 2026-11-17
- Required by: [M4 requirements](../phases/M4.md), ER-021–ER-025 and ER-021A
- Would extend: ADRs 0004/0005 for additional delivery cycles and endpoint management; initial M3 policy remains

## Context

M3 claim increments lifetime `attempt_count`, inserts that numbered attempt and requires the count to be
below endpoint `max_attempts`. Fenced completion/recovery consumes that attempt and can exhaust the event.
The old PLAN replay sketch continued the counter without introducing a new budget, used an absent
`failed` status and suggested appending an attempt in the API. That cannot make an exhausted event claimable
and would bypass the worker's attempt/lease transaction. M2/M3 also disable endpoints on HTTP 410, but
there is no management update path yet. Destination response content has deliberately never been stored.

## Recommended design, pending acceptance

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

## Choices that must be resolved before acceptance

- Exact cycle/audit schema, counter widths, M3 migration mapping and replay response/precondition fields.
  Test the maximum supported histories and reject overflow before any partial transition.
- Worker upgrade procedure: a worker using lifetime budget checks must not claim a replay governed by the
  new cycle budget. Specify a coordinated cutover or an explicit compatibility fence.
- Endpoint revision/lock order, replay vs endpoint disable/update races and whether budget PATCH ships
  in M4. URL updates must retain validation/SSRF behavior without probing the destination.
- Active DLQ versus archived cycle queries, attempt/page/statistics response bounds, and aggregation
  indexes/window limits. The UI must label the chosen success-rate denominator accurately.
- React/TypeScript build/test pins, static-asset delivery and browser session/proxy bootstrap, logout,
  expiry and development-only HTTP behavior. No auth implementation is chosen by this preparation.

## Alternatives considered

- Reset `attempt_count`: makes the current claim check usable, but collides with retained attempt numbers
  and destroys the counter's historical meaning unless schema/fence semantics also change.
- Continue lifetime count with the original maximum: keeps history but leaves exhausted replay pending
  forever. Raising the maximum ad hoc also skips early backoff and changes the endpoint policy.
- Create a new event: gives a fresh budget but breaks stable replay identity and receiver deduplication.
- Put the admin token in frontend storage: simple to scaffold but turns a text-rendering mistake into
  persistent management-token exposure. Prefer bounded server/session or proxy access.

## Consequences and validation

This proposal adds explicit schema/worker compatibility work to ER-022 and endpoint recovery to ER-021A.
It does not authorize changes to accepted ADR files or claim that these features exist. Acceptance needs
reviewed state/concurrency cases, an upgrade test, unchanged M3 policy/fencing regressions, seeded real
statistics and browser security/poll-cleanup checks specified in the M4 phase document.
