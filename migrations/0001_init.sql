-- 0001_init.sql
-- The complete v1 schema. Later migrations are additive only.

CREATE TABLE IF NOT EXISTS tenants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS endpoints (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    url               text NOT NULL,
    description       text NOT NULL DEFAULT '',
    ingest_secret     bytea NOT NULL,
    delivery_secret   bytea NOT NULL,
    enabled           boolean NOT NULL DEFAULT true,
    max_attempts      smallint NOT NULL DEFAULT 8 CHECK (max_attempts BETWEEN 1 AND 20),
    ordered           boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS endpoints_tenant ON endpoints (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS events (
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

CREATE UNIQUE INDEX IF NOT EXISTS events_idempotency
    ON events (endpoint_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS events_due ON events (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS events_lease ON events (lease_expires_at)
    WHERE status = 'in_flight';

CREATE INDEX IF NOT EXISTS events_recent ON events (tenant_id, received_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id                bigserial PRIMARY KEY,
    event_id          uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempt_no        smallint NOT NULL,
    started_at        timestamptz NOT NULL,
    finished_at       timestamptz,
    outcome           text NOT NULL,
    response_status   integer,
    response_excerpt  text,
    error             text,
    duration_ms       integer
);

CREATE UNIQUE INDEX IF NOT EXISTS attempts_unique ON delivery_attempts (event_id, attempt_no);

CREATE TABLE IF NOT EXISTS dead_letters (
    event_id    uuid PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    moved_at    timestamptz NOT NULL DEFAULT now(),
    reason      text NOT NULL,
    last_error  text
);

CREATE TABLE IF NOT EXISTS api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    prefix       text NOT NULL UNIQUE,
    hash         bytea NOT NULL,
    label        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- M1 has a single implicit tenant. Multi-tenancy arrives in M5; this row is
-- the anchor that existing data will keep.
INSERT INTO tenants (id, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'default')
ON CONFLICT (id) DO NOTHING;
