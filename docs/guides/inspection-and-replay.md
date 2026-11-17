# M4 inspection, endpoint recovery and replay

M4 is implemented and verified locally on `feat/m4-implementation`; publication/branch CI are pending.
The [phase requirements](../phases/M4.md), accepted [ADR 0006](../adr/0006-m4-replay-and-inspection.md)
and [validation record](../testing/m4-inspection-replay-dashboard.md) hold the contract and evidence.

## Local dashboard

The API embeds the committed React application at `/dashboard/`. A normal Go/Docker build needs no Node.
For fresh disposable development services, the validation environment used PostgreSQL 16 at port 55444
and Redis 7.2 at port 56384. In PowerShell:

```powershell
$env:EVENTRELAY_PG_PORT='55444'
$env:EVENTRELAY_REDIS_PORT='56384'
docker compose -p m4-eventrelay -f deploy/docker-compose.yml up -d postgres redis
$env:GOTOOLCHAIN='go1.26.11'
$env:EVENTRELAY_POSTGRES_DSN='postgres://eventrelay:eventrelay@127.0.0.1:55444/eventrelay?sslmode=disable'
$env:EVENTRELAY_REDIS_URL='redis://127.0.0.1:56384/0'
$env:EVENTRELAY_HTTP_ADDR='127.0.0.1:8080'
$env:EVENTRELAY_ADMIN_TOKEN='local-m4-synthetic-token'
go run ./cmd/eventrelay
```

Start `go run ./cmd/worker` in a second shell with the same PostgreSQL/Redis configuration. If a local
receiver uses private/loopback addresses, set `EVENTRELAY_ALLOW_PRIVATE_TARGETS=true` for that worker.
Create an endpoint and signed event using the [README quickstart](../../README.md#quickstart), then open
[http://127.0.0.1:8080/dashboard/](http://127.0.0.1:8080/dashboard/) and enter the configured management token.
These credentials and services are synthetic development examples.

The console shows paginated endpoints and event summaries. Filter by status, endpoint ID or received-time
range, then open an event for its current cycle, next retry, terminal reason, attempt timeline and previous
cycles. Payload stays hidden until selected in detail and renders as text. Attempt response bodies/headers
are explicitly unavailable because M2/M3 never stored them.

Open an endpoint to inspect observed statistics or correct its URL/enabled state. Updates require the
revision returned by the last read. A stale edit returns conflict and needs a refreshed action. Disabling
retains pending events and existing leases; re-enabling lets durable sweeps dispatch them. URL inspection
and PATCH do not initiate a destination probe. Delivery still validates/dials public IPs directly and
does not follow redirects or environment proxies.

## Guarded replay

Replay accepts only `delivered` or `dead_lettered`, without a lease, for an enabled owned endpoint.
The request must carry the current `expected_replay_count`. The API also reserves integer capacity for
the whole next budget. Missing/foreign IDs return 404; stale counts, disabled endpoints, nonterminal
states and exhausted counter capacity return 409.

An accepted replay returns 202 after a PostgreSQL transaction archives the completed cycle, removes
active DLQ membership, advances the count and persists pending/due state. It preserves ID, endpoint,
payload, type, received time and lifetime attempts. Only the next worker claim creates attempt `n+1`.
The new cycle snapshots the endpoint's 1–20 attempt budget and starts the unchanged M3 schedule from
cycle attempt one; expiry consumes its claimed attempt. Historical terminal reasons stay queryable.

The browser asks for confirmation and warns that replaying a delivered event can repeat an effect at
the destination. It shows a conflict once and refreshes; it does not silently retry a replay request.
If a mutation times out, read the current replay count before considering another explicit submission.
Durable sweeps publish the accepted replay independently of API-time Redis availability.

## Bearer API examples

The existing static admin bearer token authorizes `/v1`; browser cookies do not. New operations are in
[api/openapi.yaml](../../api/openapi.yaml), and browser endpoints are in
[api/dashboard.openapi.yaml](../../api/dashboard.openapi.yaml).

```powershell
$relayAPI='http://127.0.0.1:8080'
$relayHeaders=@{Authorization="Bearer $env:EVENTRELAY_ADMIN_TOKEN"}
$relayEventID='replace-with-an-owned-event-uuid'
$relayEvent=Invoke-RestMethod "$relayAPI/v1/events/$relayEventID" -Headers $relayHeaders
Invoke-RestMethod "$relayAPI/v1/events/$relayEventID/attempts?limit=50" -Headers $relayHeaders
Invoke-RestMethod "$relayAPI/v1/events/$relayEventID/cycles?limit=50" -Headers $relayHeaders
$relayReplay=@{expected_replay_count=$relayEvent.replay_count} | ConvertTo-Json -Compress
Invoke-RestMethod "$relayAPI/v1/events/$relayEventID/replay" -Method Post -Headers $relayHeaders -ContentType 'application/json' -Body $relayReplay
```

Read `/v1/endpoints/{id}` for `revision`, then PATCH with `expected_revision` and at least one of `url`
or `enabled`. Only those fields are accepted. M4 does not expose endpoint deletion, signing-secret
rotation or budget edits. New mutations require exactly one bounded JSON object with documented,
non-null fields. Invalid bodies/filters return 422 RFC 9457 problem documents.

## Bounds and statistics

| Resource | Contract |
|---|---|
| New inspection operations | Two-second request/database deadline; visible 503 on timeout |
| JSON responses | At most 1 MiB; 503 before headers/body if the encoded response is too large |
| Endpoint/event/attempt/cycle pages | Default 50, maximum 200; console uses 25-item pages |
| Event detail payload | At most 512 KiB; larger stored payloads return null/unavailable with byte count |
| Diagnostics | At most 2,048 characters, with truncation flag |
| Statistics | At most 100,000 finished matching attempts; materialize 100,001 to detect overflow |
| Browser requests/polling | Four-second client deadline; next poll five seconds after completion |
| Browser retained history | Current resource pages and at most 20 cursor-history entries |

Legacy `/v1/events` retains payloads and its cursor format. The console always requests `view=summary`,
which omits payloads. Legacy `/v1/endpoints` remains unpaginated unless `limit`/`cursor` is requested;
the console uses pagination. Event ordering remains `(received_at DESC,id DESC)`; endpoints use
`(created_at DESC,id DESC)`; attempt/cycle history uses ascending lifetime/cycle numbers. New cursors
bind route, tenant and canonical parsed filters. Optional RFC3339 `from`/`to` normalize to UTC and select
`[from,to)`. Changing filters requires restarting traversal. Status changes and future retention change
live membership; unchanged older rows can be traversed without duplicates/gaps under concurrent inserts.

Endpoint statistics use the database clock for `[now - 24h,now)`. Numerator: finished attempts with
`outcome=success` and an HTTP response. Denominator: all finished attempts with an HTTP response,
including broken-body responses classified as network failures. Replay cycles count as observed
attempts. Network, timeout, policy and lease-expiry counts are also shown; these categories can overlap
the response/no-response counts. A zero response denominator yields null; more than 100,000 finished
matching attempts yields `available=false` and `attempt_limit_exceeded`. Missing response evidence is
never synthesized. These are historical HTTP-response statistics, not endpoint health probes or
unique-event delivery probabilities.

## Browser session and origin

Login exchanges the entered management token for a random HttpOnly/SameSite=Strict cookie scoped to
`/dashboard/` and a separate CSRF nonce retained only in client memory. Sessions have a 30-minute absolute
expiry, a 256-session cap, logout revocation and API-restart invalidation. The restricted session API
authorizes inspection, endpoint PATCH and replay; it cannot create secrets, ingest or authorize `/v1`.

Login and unsafe methods require the exact browser origin; cookie mutations also require
`X-EventRelay-CSRF`. Plain HTTP with an inferred origin is loopback-only. For a TLS proxy, pin
`EVENTRELAY_DASHBOARD_ORIGIN=https://relay.example` and serve `/dashboard/` plus its API on that same
origin. Forwarded headers do not choose this origin. HTTPS uses Secure cookies. Sessions are local to
one API process; distributed sessions and production deployment acceptance remain outside M4.

## M3 to M4 upgrade

1. Stop/drain every M3 API and worker before applying migration 0004. Keep a restorable database backup
   for this forward-only upgrade. Running old and new binaries together on the migrated database is unsupported.
2. Start one M4 API with migration-on-start enabled. The existing advisory migration lock serializes
   application of 0004. Existing events become cycle zero using their observed attempts/budget/timestamps;
   unknown old endpoint revisions and missing terminal timestamps remain unknown. Existing M3 replay
   counters must be zero; unexpected externally edited replay state causes migration failure.
3. Start M4 workers, then resume ingestion. Claims, completion, recovery and replay set transaction-local
   `eventrelay.worker_schema_version=4`. The migration trigger rejects old-runtime delivery transitions.
   Any remaining expired lease is recovered under the same M3 policy within its original cycle budget.
4. Inspect an existing terminal event/attempts, re-enable its endpoint if needed and perform an explicitly
   versioned replay. The next worker claim increments the lifetime attempt number without resetting history.

The migration runner makes repeated application safe by tracking completed migrations; the SQL is not
an ad hoc repeatable script. Rollback to M3 binaries on an M4 database is unsupported. Upgrade/fence,
counter-capacity and replay-history tests are named in the validation record.
