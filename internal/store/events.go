package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event statuses. Kept as constants because they appear in SQL check
// constraints and in API filters.
const (
	StatusPending      = "pending"
	StatusInFlight     = "in_flight"
	StatusDelivered    = "delivered"
	StatusDeadLettered = "dead_lettered"
	StatusCanceled     = "canceled"
)

// Event is one accepted payload for one endpoint. The ID never changes, not
// even across replays: that is what makes consumer-side idempotency possible.
type Event struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	EndpointID     uuid.UUID
	EventType      string
	Payload        json.RawMessage
	IdempotencyKey string
	Status         string
	AttemptCount   int16
	ReplayCount    int16
	NextAttemptAt  time.Time
	ReceivedAt     time.Time
	DeliveredAt    *time.Time
	LastError      string
}

const eventColumns = `
	id::text, tenant_id::text, endpoint_id::text, event_type,
	payload::text, COALESCE(idempotency_key, ''), status,
	attempt_count, replay_count, next_attempt_at, received_at,
	delivered_at, COALESCE(last_error, '')`

// InsertEvent persists an event as pending. The caller supplies the endpoint,
// which must already exist and belong to the tenant.
func (s *Store) InsertEvent(ctx context.Context, tenantID, endpointID uuid.UUID, eventType string, payload []byte, idempotencyKey string) (Event, error) {
	const q = `
		INSERT INTO events (tenant_id, endpoint_id, event_type, payload, idempotency_key, status, next_attempt_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, NULLIF($5, ''), 'pending', now())
		RETURNING ` + eventColumns

	var e Event
	var payloadText string
	row := s.pool.QueryRow(ctx, q,
		tenantID.String(), endpointID.String(), eventType, string(payload), idempotencyKey)

	err := row.Scan(
		&e.ID, &e.TenantID, &e.EndpointID, &e.EventType,
		&payloadText, &e.IdempotencyKey, &e.Status,
		&e.AttemptCount, &e.ReplayCount, &e.NextAttemptAt, &e.ReceivedAt,
		&e.DeliveredAt, &e.LastError,
	)
	if err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	e.Payload = json.RawMessage(payloadText)
	return e, nil
}

// Cursor points at a position in the events list. Lists are ordered by
// (received_at DESC, id DESC), so a cursor is that pair.
type Cursor struct {
	ReceivedAt time.Time
	ID         uuid.UUID
}

// EventFilter narrows a list query. Zero values mean "no filter".
type EventFilter struct {
	Status     string
	EndpointID *uuid.UUID
	Limit      int
	Before     *Cursor
}

// ListEvents returns events for a tenant plus the cursor for the next page.
func (s *Store) ListEvents(ctx context.Context, tenantID uuid.UUID, f EventFilter) ([]Event, *Cursor, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	q := `SELECT ` + eventColumns + ` FROM events WHERE tenant_id = $1::uuid`
	args := []any{tenantID.String()}

	if f.Status != "" {
		args = append(args, f.Status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.EndpointID != nil {
		args = append(args, f.EndpointID.String())
		q += fmt.Sprintf(" AND endpoint_id = $%d::uuid", len(args))
	}
	if f.Before != nil {
		args = append(args, f.Before.ReceivedAt, f.Before.ID.String())
		q += fmt.Sprintf(" AND (received_at, id) < ($%d, $%d::uuid)", len(args)-1, len(args))
	}

	args = append(args, limit+1)
	q += fmt.Sprintf(" ORDER BY received_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		var e Event
		var payloadText string
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.EndpointID, &e.EventType,
			&payloadText, &e.IdempotencyKey, &e.Status,
			&e.AttemptCount, &e.ReplayCount, &e.NextAttemptAt, &e.ReceivedAt,
			&e.DeliveredAt, &e.LastError,
		); err != nil {
			return nil, nil, fmt.Errorf("scan event: %w", err)
		}
		e.Payload = json.RawMessage(payloadText)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate events: %w", err)
	}

	var next *Cursor
	if len(events) > limit {
		events = events[:limit]
		last := events[limit-1]
		next = &Cursor{ReceivedAt: last.ReceivedAt, ID: last.ID}
	}
	return events, next, nil
}
