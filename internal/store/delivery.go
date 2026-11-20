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

// beginDelivery marks every transition with the post-cutover runtime version.
// The marker is transaction-local; it never leaks into pooled connections.
func (s *Store) beginDelivery(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('eventrelay.worker_schema_version','4',true)`); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

// Claim atomically leases one due event and records the start before any HTTP I/O.
func (s *Store) Claim(ctx context.Context, tenant, id uuid.UUID, worker string, duration time.Duration) (Lease, error) {
	tx, err := s.beginDelivery(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	token := worker + ":" + uuid.NewString()
	// Lock the event before the endpoint in every delivery/replay transaction.
	e, err := scanEvent(tx.QueryRow(ctx, `SELECT `+eventColumns+` FROM events
		WHERE tenant_id=$1::uuid AND id=$2::uuid AND status='pending' AND next_attempt_at<=now()
		AND cycle_attempt_count<cycle_max_attempts AND attempt_count<2147483647
		FOR UPDATE SKIP LOCKED`, tenant.String(), id.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, err
	}
	// Share-lock configuration until the URL/revision snapshot is committed.
	var ep Endpoint
	err = tx.QueryRow(ctx, `SELECT url,delivery_secret,max_attempts,enabled,revision FROM endpoints
		WHERE tenant_id=$1::uuid AND id=$2::uuid FOR SHARE`, tenant.String(), e.EndpointID.String()).Scan(&ep.URL, &ep.DeliverySecret, &ep.MaxAttempts, &ep.Enabled, &ep.Revision)
	if err != nil {
		return Lease{}, err
	}
	ep.ID = e.EndpointID
	ep.TenantID = tenant
	if !ep.Enabled {
		return Lease{}, ErrNotFound
	}
	e, err = scanEvent(tx.QueryRow(ctx, `UPDATE events SET status='in_flight',leased_by=$3,
		lease_expires_at=clock_timestamp()+$4::interval,attempt_count=attempt_count+1,
		cycle_attempt_count=cycle_attempt_count+1 WHERE tenant_id=$1::uuid AND id=$2::uuid
		RETURNING `+eventColumns, tenant.String(), id.String(), token, fmt.Sprintf("%f seconds", duration.Seconds())))
	if err != nil {
		return Lease{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,started_at,outcome,cycle_no,cycle_attempt_no,endpoint_revision)
		VALUES($1::uuid,$2,now(),'in_flight',$3,$4,$5)`, id.String(), e.AttemptCount, e.ReplayCount, e.CycleAttemptCount, ep.Revision)
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
	// PolicyRejected marks a destination address refused before dialing. It
	// preserves the M2 network outcome while selecting the policy terminal reason.
	PolicyRejected bool
}

// Finish commits the attempt and event transition together. Stale workers
// cannot write. For failures, decide runs once after the row lock and all lease
// fences succeed. The caller captures completion time before calling Finish
// and supplies a calculation under its own policy; stale completions never
// redraw jitter. Successful attempts do not call decide and may pass nil.
// The callback must be a bounded calculation without I/O while the row is locked.
func (s *Store) Finish(ctx context.Context, lease Lease, result AttemptResult, decide func() (retry.Decision, error)) error {
	tx, err := s.beginDelivery(ctx)
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
	var decision retry.Decision
	if result.Outcome != "success" {
		if decide == nil {
			return errors.New("store: retry decision is required")
		}
		decision, err = decide()
		if err != nil {
			return fmt.Errorf("store: retry scheduling failed: %w", err)
		}
		if !decision.Terminal && decision.NextAttempt.IsZero() {
			return errors.New("store: retry decision without a next attempt time")
		}
		if decision.Terminal && decision.Reason == "" {
			return errors.New("store: terminal decision without a reason")
		}
	}
	errorText := result.Error
	if decision.Diagnostic != "" {
		if errorText == "" {
			errorText = decision.Diagnostic
		} else {
			errorText = errorText + "; " + decision.Diagnostic
		}
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
		dispatch_after=now(),delivered_at=CASE WHEN $3='delivered' THEN now() ELSE NULL END,
		cycle_finished_at=CASE WHEN $3 IN ('delivered','dead_lettered') THEN now() ELSE NULL END
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
		_, err = tx.Exec(ctx, `UPDATE endpoints SET enabled=false,updated_at=now()
			WHERE tenant_id=$1::uuid AND id=$2::uuid AND revision=$3`, e.TenantID.String(), e.EndpointID.String(), lease.Endpoint.Revision)
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
	tx, err := s.beginDelivery(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.attempt_count,e.cycle_attempt_count,e.cycle_max_attempts FROM events e
		WHERE e.tenant_id=$1::uuid AND e.status='in_flight' AND e.lease_expires_at<=now() ORDER BY e.lease_expires_at LIMIT 256 FOR UPDATE OF e SKIP LOCKED`, tenant.String())
	if err != nil {
		return 0, err
	}
	type expired struct {
		id                         string
		attempt, cycleAttempt, max int
	}
	var expiredRows []expired
	for rows.Next() {
		var x expired
		if err := rows.Scan(&x.id, &x.attempt, &x.cycleAttempt, &x.max); err != nil {
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
		if x.cycleAttempt >= x.max {
			status = StatusDeadLettered
		} else {
			nextAttempt = next(x.cycleAttempt)
			if nextAttempt.IsZero() {
				return 0, fmt.Errorf("store: recovery schedule returned no time for attempt %d", x.attempt)
			}
		}
		_, err = tx.Exec(ctx, `UPDATE events SET status=$3,leased_by=NULL,lease_expires_at=NULL,
			next_attempt_at=CASE WHEN $3='pending' THEN $4::timestamptz ELSE now() END,
			dispatch_after=now(),last_error='worker lease expired',
			cycle_finished_at=CASE WHEN $3='dead_lettered' THEN now() ELSE NULL END
			WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), x.id, status, nextAttempt)
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
