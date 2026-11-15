package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shellghostt/eventrelay/internal/retry"
)

// Lease fences completion by an unguessable token and attempt number.
type Lease struct {
	Event    Event
	Endpoint Endpoint
	Token    string
}

var ErrLeaseLost = errors.New("delivery lease expired or replaced")

// Claim atomically leases one due event and records the start before any HTTP I/O.
func (s *Store) Claim(ctx context.Context, tenant, id uuid.UUID, worker string, duration time.Duration) (Lease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	token := worker + ":" + uuid.NewString()
	q := `UPDATE events SET status='in_flight', leased_by=$3,
		lease_expires_at=now()+$4::interval, attempt_count=attempt_count+1
		WHERE id = (SELECT e.id FROM events e JOIN endpoints p ON p.id=e.endpoint_id AND p.tenant_id=e.tenant_id
		WHERE e.tenant_id=$1::uuid AND e.id=$2::uuid AND e.status='pending' AND e.next_attempt_at<=now()
		AND p.enabled AND e.attempt_count < p.max_attempts FOR UPDATE OF e SKIP LOCKED)
		RETURNING ` + eventColumns
	var e Event
	var payload string
	err = tx.QueryRow(ctx, q, tenant.String(), id.String(), token, fmt.Sprintf("%f seconds", duration.Seconds())).Scan(
		&e.ID, &e.TenantID, &e.EndpointID, &e.EventType, &payload, &e.IdempotencyKey, &e.Status,
		&e.AttemptCount, &e.ReplayCount, &e.NextAttemptAt, &e.ReceivedAt, &e.DeliveredAt, &e.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	e.Payload = []byte(payload)
	// Read through this transaction to avoid needing a second pool connection.
	var ep Endpoint
	err = tx.QueryRow(ctx, `SELECT url,delivery_secret,max_attempts FROM endpoints WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), e.EndpointID.String()).Scan(&ep.URL, &ep.DeliverySecret, &ep.MaxAttempts)
	if err != nil {
		return Lease{}, err
	}
	ep.ID = e.EndpointID
	ep.TenantID = tenant
	ep.Enabled = true
	_, err = tx.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,started_at,outcome) VALUES($1::uuid,$2,now(),'in_flight')`, id.String(), e.AttemptCount)
	if err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	return Lease{Event: e, Endpoint: ep, Token: token}, nil
}

type AttemptResult struct {
	Outcome  string
	Status   int
	Error    string
	Duration time.Duration
	// RetryAfter is the raw Retry-After header of a retryable response. The
	// caller parses it and never persists or logs the raw value.
	RetryAfter string
}

// Finish commits the attempt and event transition together. Stale workers
// cannot write. The caller computes the retry decision under its own policy
// and clock; this persists it exactly once inside the fences, so duplicate
// completions and sweeps can neither redraw jitter nor extend a scheduled
// retry (ADR 0005).
func (s *Store) Finish(ctx context.Context, lease Lease, result AttemptResult, decision retry.Decision) error {
	if result.Outcome == "success" && decision.Terminal {
		return errors.New("store: success outcome marked terminal")
	}
	if result.Outcome != "success" && !decision.Terminal && decision.NextAttempt.IsZero() {
		return errors.New("store: retry decision without a next attempt time")
	}
	if decision.Terminal && decision.Reason == "" {
		return errors.New("store: terminal decision without a reason")
	}
	errorText := result.Error
	if decision.Diagnostic != "" {
		if errorText == "" {
			errorText = decision.Diagnostic
		} else {
			errorText = errorText + "; " + decision.Diagnostic
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	e := lease.Event
	var found string
	err = tx.QueryRow(ctx, `SELECT id::text FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid
		AND status='in_flight' AND leased_by=$3 AND attempt_count=$4 FOR UPDATE`, e.TenantID.String(), e.ID.String(), lease.Token, e.AttemptCount).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	// Check database wall time after acquiring the row lock. now() is the
	// transaction's start time, and even a SELECT target may be evaluated before
	// its FOR UPDATE lock wait. A blocked completion must not accept an old lease.
	var live bool
	if err = tx.QueryRow(ctx, `SELECT lease_expires_at>clock_timestamp() FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid`, e.TenantID.String(), e.ID.String()).Scan(&live); err != nil {
		return err
	}
	if !live {
		return ErrLeaseLost
	}
	status := StatusPending
	if result.Outcome == "success" {
		status = StatusDelivered
	} else if decision.Terminal {
		status = StatusDeadLettered
	}
	_, err = tx.Exec(ctx, `UPDATE delivery_attempts SET finished_at=now(),outcome=$3,response_status=NULLIF($4,0),error=NULLIF($5,''),duration_ms=$6 WHERE event_id=$1::uuid AND attempt_no=$2`, e.ID.String(), e.AttemptCount, result.Outcome, result.Status, errorText, result.Duration.Milliseconds())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE events SET status=$3,leased_by=NULL,lease_expires_at=NULL,last_error=NULLIF($4,''),
		next_attempt_at=CASE WHEN $3='pending' THEN $5::timestamptz ELSE now() END,
		dispatch_after=now(),delivered_at=CASE WHEN $3='delivered' THEN now() ELSE NULL END
		WHERE tenant_id=$1::uuid AND id=$2::uuid`, e.TenantID.String(), e.ID.String(), status, errorText, decision.NextAttempt)
	if err != nil {
		return err
	}
	if status == StatusDeadLettered {
		_, err = tx.Exec(ctx, `INSERT INTO dead_letters(event_id,reason,last_error) VALUES($1::uuid,$2,NULLIF($3,'')) ON CONFLICT(event_id) DO UPDATE SET reason=excluded.reason,last_error=excluded.last_error,moved_at=now()`, e.ID.String(), decision.Reason, errorText)
		if err != nil {
			return err
		}
	}
	if result.Status == 410 {
		_, err = tx.Exec(ctx, `UPDATE endpoints SET enabled=false,updated_at=now() WHERE tenant_id=$1::uuid AND id=$2::uuid`, e.TenantID.String(), e.EndpointID.String())
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type Dispatch struct {
	TenantID uuid.UUID
	EventID  uuid.UUID
}

// Due claims a bounded page for publication. Redis loss is recovered from these rows.
func (s *Store) Due(ctx context.Context, tenant uuid.UUID, limit int, interval time.Duration) ([]Dispatch, error) {
	rows, err := s.pool.Query(ctx, `UPDATE events SET dispatch_after=now()+$3::interval
		WHERE id IN (SELECT e.id FROM events e JOIN endpoints p ON p.id=e.endpoint_id AND p.tenant_id=e.tenant_id
		WHERE e.tenant_id=$1::uuid AND e.status='pending' AND e.next_attempt_at<=now() AND e.dispatch_after<=now()
		AND p.enabled ORDER BY e.dispatch_after,e.id LIMIT $2 FOR UPDATE OF e SKIP LOCKED)
		RETURNING tenant_id::text,id::text`, tenant.String(), limit, fmt.Sprintf("%f seconds", interval.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Dispatch, 0, limit)
	for rows.Next() {
		var d Dispatch
		if err := rows.Scan(&d.TenantID, &d.EventID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecoverExpired closes incomplete attempts and makes their events eligible
// again. An expired lease consumes its already claimed attempt: the attempt
// closes once as lease_expired, the same attempt budget applies, and no extra
// uncounted HTTP attempt is created. The caller supplies the schedule for the
// next attempt; it is consulted at most once per recovered row inside the same
// transaction, so concurrent sweepers cannot redraw it (ADR 0005).
func (s *Store) RecoverExpired(ctx context.Context, tenant uuid.UUID, next func(failedAttempt int) time.Time) (int64, error) {
	if next == nil {
		return 0, errors.New("store: recovery schedule is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.attempt_count,p.max_attempts FROM events e JOIN endpoints p ON p.id=e.endpoint_id AND p.tenant_id=e.tenant_id
		WHERE e.tenant_id=$1::uuid AND e.status='in_flight' AND e.lease_expires_at<=now() ORDER BY e.lease_expires_at LIMIT 256 FOR UPDATE OF e SKIP LOCKED`, tenant.String())
	if err != nil {
		return 0, err
	}
	type expired struct {
		id           string
		attempt, max int
	}
	var expiredRows []expired
	for rows.Next() {
		var x expired
		if err := rows.Scan(&x.id, &x.attempt, &x.max); err != nil {
			rows.Close()
			return 0, err
		}
		expiredRows = append(expiredRows, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, x := range expiredRows {
		_, err = tx.Exec(ctx, `UPDATE delivery_attempts SET outcome='lease_expired',finished_at=now(),error='worker lease expired',duration_ms=LEAST(2147483647,EXTRACT(EPOCH FROM(now()-started_at))*1000)::integer WHERE event_id=$1::uuid AND attempt_no=$2 AND outcome='in_flight'`, x.id, x.attempt)
		if err != nil {
			return 0, err
		}
		status := StatusPending
		nextAttempt := time.Time{}
		if x.attempt >= x.max {
			status = StatusDeadLettered
		} else {
			nextAttempt = next(x.attempt)
			if nextAttempt.IsZero() {
				return 0, fmt.Errorf("store: recovery schedule returned no time for attempt %d", x.attempt)
			}
		}
		_, err = tx.Exec(ctx, `UPDATE events SET status=$3,leased_by=NULL,lease_expires_at=NULL,
			next_attempt_at=CASE WHEN $3='pending' THEN $4::timestamptz ELSE now() END,
			dispatch_after=now(),last_error='worker lease expired' WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), x.id, status, nextAttempt)
		if err != nil {
			return 0, err
		}
		if status == StatusDeadLettered {
			_, err = tx.Exec(ctx, `INSERT INTO dead_letters(event_id,reason) VALUES($1::uuid,$2) ON CONFLICT(event_id) DO NOTHING`, x.id, retry.ReasonAttemptsExhausted)
			if err != nil {
				return 0, err
			}
		}
	}
	return int64(len(expiredRows)), tx.Commit(ctx)
}
