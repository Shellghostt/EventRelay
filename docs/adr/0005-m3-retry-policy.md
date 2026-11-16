# ADR 0005 - M3 retry policy, terminal reasons and recovery

- Status: accepted
- Date: 2026-11-15
- Deciders: Aditya
- Required by: [M3 requirements](../phases/M3.md) ("Decisions to record before implementation")

## Context

M2 retries every failure after a fixed 10 seconds, writes the raw attempt outcome string
(`retryable`, `timeout`, `network`, `non_retryable`) into `dead_letters.reason`, and returns
events recovered from an expired lease to `pending` with an immediate retry. M3 replaces the
fixed delay with the documented schedule, jitter and `Retry-After`, and requires stable terminal
reasons. The M3 requirements list the decisions that must be recorded before dependent code.

## Decisions

1. **Schedule table and exhaustion order.** The nominal wait is indexed by the failed attempt
   number: 1→10s, 2→30s, 3→2m, 4→10m, 5→30m, 6→2h, 7→6h, 8–19→24h. Failed attempts 8 through 19
   share the 24-hour entry. Exhaustion is checked before any wait is computed: a failure on
   attempt `max_attempts` is terminal and never schedules an entry, so the default budget of 8
   consumes exactly the seven distinct waits (31,360s nominal) and the ceiling of 20 makes the
   24-hour entry repeat at most twelve times.

2. **Jitter.** The policy wait is multiplied by a uniform factor in [0.8, 1.2], drawn once per
   scheduling decision. The random source is injected; production passes the standard library
   generator. Jitter applies only to the policy wait, never to a Retry-After-derived wait.

3. **Retry-After parsing and precedence.** The header is parsed as nonnegative integer seconds
   or an HTTP date. Seconds are relative to completion; dates are measured from the recorded
   completion time in UTC. A usable value contributes a minimum wait capped at 24 hours; the
   chosen wait is the larger of the capped header wait and the jittered policy wait, so a zero
   header does not bypass backoff and jitter never shortens a valid header request. Negative,
   malformed, past-date and overflowing values are ignored: the policy wait applies and a fixed
   bounded diagnostic (`retry-after ignored: invalid value`) is recorded — the raw header value
   is never persisted or logged. The cap applies to the header only; a jittered 24-hour entry may
   reach 28.8 hours. Success and non-retryable outcomes ignore the header, and a header is
   honored only when a response exists. Scheduling math runs in the worker against its UTC clock
   captured at completion, and the resulting absolute `next_attempt_at` is persisted once inside
   the fenced completion transaction; the database clock stays authoritative for due-ness and
   lease expiry, and duplicate completions fail the fences without redrawing jitter.

4. **Terminal reason identifiers.** Exactly three values are written to `dead_letters.reason`:
   - `attempts_exhausted` — a retryable-classified failure (including `timeout` and `network`,
     and a lease expired on that attempt) whose attempt number reached `max_attempts`;
   - `non_retryable_response` — the destination returned a response classified non-retryable,
     including 410 Gone and unfollowed 3xx;
   - `non_retryable_policy` — delivery was refused before any HTTP response existed: an invalid
     destination URL or a policy-blocked destination address.
   The M2 habit of writing the outcome string as the reason ends.

5. **Interrupted attempts.** An expired lease consumes its already claimed attempt. Recovery
   closes that attempt row once as `lease_expired` and applies the same attempt budget: a lease
   expiring on the final allowed attempt dead-letters with `attempts_exhausted`. Otherwise
   recovery schedules the next attempt with the same policy as a retryable failure — nominal wait
   for the failed attempt number, jittered, no Retry-After because no response exists. This
   replaces the M2 immediate retry so a worker that repeatedly dies mid-lease cannot tighten the
   cadence against the destination. No uncounted HTTP attempt is created.

6. **HTTP 410 endpoint disable is unchanged.** M2 already disables an endpoint that returned 410
   (ADR 0004, confirmed in `Store.Finish`). The event that received the 410 is dead-lettered
   `non_retryable_response`. Disabled endpoints keep their pending events without dispatch until
   endpoint management arrives in M4. M3 adds no behavior here; this records the existing
   boundary so plan text is not misread as new M3 work.

7. **No production schedule override in M3.** The table, jitter bounds and header cap are code
   constants. No `EVENTRELAY_` variable is added for the schedule, and no demo environment
   variable is invented; a fast demo injects a short test schedule through the retry package in
   tests. A production override contract requires a future ADR (target: M6 operations).

8. **Migration and defaults.** `migrations/0003_m3_retry_policy.sql` normalizes existing
   `dead_letters.reason` values once (`retryable`, `timeout`, `network` → `attempts_exhausted`;
   `non_retryable` → `non_retryable_response`; `attempts_exhausted` unchanged) and adds a CHECK
   constraint limited to the three identifiers. No column changes are needed: `next_attempt_at`,
   `dispatch_after`, and `max_attempts` (default 8, CHECK 1–20) already exist, so existing rows
   keep their schedules and budgets. The migration is forward-only and recorded in
   `schema_migrations` like the others.

## Consequences

- `internal/retry` is added (ER-017, ER-018) and `Store.Finish` / `RecoverExpired` take a
  computed decision instead of a fixed delay (ER-019, ER-020), keeping one fenced writer per
  transition.
- Existing M2 rows remain valid; reason values are normalized exactly once by migration 0003.
- Configuration is unchanged; `docs/PLAN.md` and `phases/M3.md` gain links to this ADR when it
  is accepted, not before.
- Worker and database clocks may skew slightly; that only shifts waits by the skew and does not
  affect fencing, which remains database-side.
