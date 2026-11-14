-- A durable dispatch deadline bounds republishing and avoids starving later events.
ALTER TABLE events ADD COLUMN dispatch_after timestamptz NOT NULL DEFAULT now();
CREATE INDEX events_dispatch ON events (dispatch_after, next_attempt_at) WHERE status = 'pending';
