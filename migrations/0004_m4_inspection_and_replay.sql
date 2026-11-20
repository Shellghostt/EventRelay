-- M4: stop/drain M3 binaries before applying this migration. Never reset lifetime
-- attempt identities. Current M3 state becomes cycle zero; no history is invented.
ALTER TABLE endpoints ADD COLUMN revision integer NOT NULL DEFAULT 1 CHECK (revision > 0);
ALTER TABLE events ALTER COLUMN attempt_count TYPE integer;
ALTER TABLE events ALTER COLUMN replay_count TYPE integer;
ALTER TABLE delivery_attempts ALTER COLUMN attempt_no TYPE integer;

ALTER TABLE events ADD COLUMN cycle_attempt_count smallint NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN cycle_max_attempts smallint NOT NULL DEFAULT 8;
ALTER TABLE events ADD COLUMN cycle_started_at timestamptz;
ALTER TABLE events ADD COLUMN cycle_finished_at timestamptz;
UPDATE events e SET cycle_attempt_count=e.attempt_count,
    cycle_max_attempts=p.max_attempts, cycle_started_at=e.received_at,
    cycle_finished_at=CASE WHEN e.status='delivered' THEN e.delivered_at
        WHEN e.status='dead_lettered' THEN (SELECT moved_at FROM dead_letters d WHERE d.event_id=e.id)
        ELSE NULL END
FROM endpoints p WHERE p.id=e.endpoint_id AND p.tenant_id=e.tenant_id;
ALTER TABLE events ALTER COLUMN cycle_started_at SET NOT NULL;
ALTER TABLE events ALTER COLUMN cycle_started_at SET DEFAULT now();
ALTER TABLE events ADD CONSTRAINT events_m4_counts CHECK (
    attempt_count >= 0 AND replay_count >= 0 AND cycle_max_attempts BETWEEN 1 AND 20
    AND cycle_attempt_count BETWEEN 0 AND cycle_max_attempts AND attempt_count >= cycle_attempt_count);
-- M3 has no replay operation. Unexpected externally edited replay state must not
-- be silently mapped into a fabricated cycle history.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM events WHERE replay_count <> 0) THEN
        RAISE EXCEPTION 'M4 upgrade requires unmodified M3 replay counters';
    END IF;
END $$;

ALTER TABLE delivery_attempts ADD COLUMN cycle_no integer NOT NULL DEFAULT 0 CHECK (cycle_no >= 0);
ALTER TABLE delivery_attempts ADD COLUMN cycle_attempt_no smallint;
ALTER TABLE delivery_attempts ADD COLUMN endpoint_revision integer;
UPDATE delivery_attempts SET cycle_attempt_no=attempt_no;
ALTER TABLE delivery_attempts ALTER COLUMN cycle_attempt_no SET NOT NULL;
ALTER TABLE delivery_attempts ADD CONSTRAINT attempts_m4_numbers CHECK (
    attempt_no > 0 AND cycle_attempt_no BETWEEN 1 AND 20 AND (endpoint_revision IS NULL OR endpoint_revision > 0));

CREATE TABLE delivery_cycles (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    cycle_no integer NOT NULL CHECK (cycle_no >= 0),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    replayed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    final_status text NOT NULL CHECK (final_status IN ('delivered','dead_lettered')),
    terminal_reason text CHECK (terminal_reason IN ('attempts_exhausted','non_retryable_response','non_retryable_policy')),
    attempt_count smallint NOT NULL,
    max_attempts smallint NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
    first_attempt_no integer,
    last_attempt_no integer,
    last_error text,
    PRIMARY KEY (event_id, cycle_no),
    CHECK (attempt_count BETWEEN 0 AND max_attempts),
    CHECK ((final_status='dead_lettered' AND terminal_reason IS NOT NULL)
        OR (final_status='delivered' AND terminal_reason IS NULL)),
    CHECK ((attempt_count=0 AND first_attempt_no IS NULL AND last_attempt_no IS NULL)
        OR (attempt_count>0 AND first_attempt_no>0 AND last_attempt_no>=first_attempt_no))
);
CREATE INDEX attempts_finished ON delivery_attempts (finished_at, event_id, attempt_no)
    INCLUDE (outcome,response_status) WHERE finished_at IS NOT NULL;
CREATE INDEX events_endpoint_recent ON events (tenant_id, endpoint_id, received_at DESC, id DESC);
CREATE INDEX endpoints_page ON endpoints (tenant_id, created_at DESC, id DESC);

-- Old workers cannot commit claims/completion/recovery, including replay cycles
-- with a lifetime count below the old budget. The marker is set locally by M4;
-- it is a runtime-compatibility fence, not authentication of a database user.
CREATE FUNCTION require_m4_delivery_runtime() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ((NEW.status IS DISTINCT FROM OLD.status AND (NEW.status='in_flight' OR OLD.status='in_flight'))
        OR NEW.replay_count IS DISTINCT FROM OLD.replay_count)
        AND current_setting('eventrelay.worker_schema_version', true) IS DISTINCT FROM '4' THEN
        RAISE EXCEPTION 'delivery transition requires M4 runtime' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER events_m4_runtime BEFORE UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION require_m4_delivery_runtime();
