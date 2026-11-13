# ADR 0004 — M2 delivery, recovery and trust boundaries

- Status: accepted for the M2 feature branch
- Date: 2026-11-13
- Supersedes: ADR 0003 decisions 3–4 (forwarded-header handling and client-IP attribution)

PostgreSQL is authoritative. Redis carries tenant/event IDs in a bounded stream; workers acknowledge
dispatch after claiming a row. Expired PostgreSQL leases are recovered even when Redis loses its stream.
`dispatch_after` bounds republication and lets concurrent sweepers page fairly with SKIP LOCKED.
An attempt is inserted on claim, before HTTP I/O. Completion checks the random lease token, attempt
number and database wall-clock expiry after acquiring a row lock. A crashed attempt closes as `lease_expired`; receivers may have
already processed it, so delivery remains at least once. Redis pending entries are reclaimed by age.

M2 requires retry and terminal failure behavior before the M3 policy work. M2 therefore retries after
10 seconds, honors endpoint max_attempts, records terminal dead letters, and disables a 410 endpoint.
Exponential backoff, jitter, Retry-After and DLQ management remain M3. Disabled endpoints retain pending
events without dispatch. They require operator intervention until endpoint management arrives in M4.

HTTP has an explicit timeout shorter than the lease, follows no redirect and uses no environment proxy.
Every resolved IP is checked before dialing the selected IP, closing the DNS-rebinding gap. Private,
loopback, link-local and special-use ranges are blocked unless explicitly opted out for development.
Response bodies are read to a 1 KiB limit and discarded; response_excerpt stays NULL. Omitting content
is the only reliable redaction for arbitrary destination responses. Network errors omit URLs and bodies.

Only explicit EVENTRELAY_TRUSTED_PROXIES CIDRs enable X-Forwarded-For interpretation. The chain is walked
right to left to the first untrusted hop. Invalid chains fall back to the peer. Other forwarding headers
are ignored. remote_addr is always the original peer; client_ip is separate. Trusting a proxy assumes
that proxy appends or replaces the header correctly.

All processes may migrate during development startup; a PostgreSQL advisory lock serializes migrations.
Worker cancellation stops intake, cancels HTTP and gives the final fenced write a five-second deadline.
Uncommitted completions recover through leases. Production migration operations remain an M6 concern.
