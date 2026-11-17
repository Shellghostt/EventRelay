package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	InspectionTimeout      = 2 * time.Second
	InspectionPayloadLimit = 512 << 10
	DiagnosticLimit        = 2048
	StatisticsAttemptLimit = 100000
)

var ErrConflict = errors.New("store: state or version conflict")

func pageLimit(n int) int {
	if n < 1 || n > 200 {
		return 50
	}
	return n
}

type EventDetail struct {
	Event
	PayloadAvailable   bool
	PayloadBytes       int64
	LastErrorTruncated bool
	TerminalReason     *string
}

// EventDetailByID bounds payload/diagnostics in SQL before transferring them.
func (s *Store) EventDetailByID(ctx context.Context, tenant, id uuid.UUID) (EventDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	columns := strings.Replace(eventColumns, "payload::text", `CASE WHEN octet_length(payload::text)<=$3 THEN payload::text ELSE NULL END`, 1)
	columns = strings.Replace(columns, "COALESCE(last_error, '')", `LEFT(COALESCE(last_error,''),2048)`, 1)
	var out EventDetail
	e, err := scanEvent(s.pool.QueryRow(ctx, `SELECT `+columns+`,octet_length(payload::text),
		char_length(COALESCE(last_error,''))>2048,
		(SELECT reason FROM dead_letters d WHERE d.event_id=events.id)
		FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), id.String(), InspectionPayloadLimit),
		&out.PayloadBytes, &out.LastErrorTruncated, &out.TerminalReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventDetail{}, ErrNotFound
	}
	out.Event = e
	out.PayloadAvailable = e.Payload != nil
	return out, err
}

func (s *Store) requireEvent(ctx context.Context, tenant, id uuid.UUID) error {
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid)`, tenant.String(), id.String()).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

type Attempt struct {
	AttemptNo                int32      `json:"attempt_no"`
	CycleNo                  int32      `json:"cycle_no"`
	CycleAttemptNo           int16      `json:"cycle_attempt_no"`
	EndpointRevision         *int32     `json:"endpoint_revision"`
	StartedAt                time.Time  `json:"started_at"`
	FinishedAt               *time.Time `json:"finished_at"`
	Outcome                  string     `json:"outcome"`
	ResponseStatus           *int       `json:"response_status"`
	DurationMS               *int       `json:"duration_ms"`
	Error                    *string    `json:"error"`
	ErrorTruncated           bool       `json:"error_truncated"`
	ResponseExcerpt          *string    `json:"response_excerpt"`
	ResponseBodyAvailable    bool       `json:"response_body_available"`
	ResponseHeadersAvailable bool       `json:"response_headers_available"`
}

func (s *Store) ListAttempts(ctx context.Context, tenant, id uuid.UUID, after int32, limit int) ([]Attempt, *int32, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	if err := s.requireEvent(ctx, tenant, id); err != nil {
		return nil, nil, err
	}
	limit = pageLimit(limit)
	rows, err := s.pool.Query(ctx, `SELECT a.attempt_no,a.cycle_no,a.cycle_attempt_no,a.endpoint_revision,
		a.started_at,a.finished_at,a.outcome,a.response_status,a.duration_ms,LEFT(a.error,2048),
		COALESCE(char_length(a.error)>2048,false)
		FROM delivery_attempts a JOIN events e ON e.id=a.event_id
		WHERE e.tenant_id=$1::uuid AND e.id=$2::uuid AND a.attempt_no>$3
		ORDER BY a.attempt_no LIMIT $4`, tenant.String(), id.String(), after, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make([]Attempt, 0, limit)
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.AttemptNo, &a.CycleNo, &a.CycleAttemptNo, &a.EndpointRevision, &a.StartedAt,
			&a.FinishedAt, &a.Outcome, &a.ResponseStatus, &a.DurationMS, &a.Error, &a.ErrorTruncated); err != nil {
			return nil, nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *int32
	if len(out) > limit {
		n := out[limit-1].AttemptNo
		next = &n
		out = out[:limit]
	}
	return out, next, nil
}

type DeliveryCycle struct {
	CycleNo            int32      `json:"cycle_no"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	ReplayedAt         time.Time  `json:"replayed_at"`
	FinalStatus        string     `json:"final_status"`
	TerminalReason     *string    `json:"terminal_reason"`
	AttemptCount       int16      `json:"attempt_count"`
	MaxAttempts        int16      `json:"max_attempts"`
	FirstAttemptNo     *int32     `json:"first_attempt_no"`
	LastAttemptNo      *int32     `json:"last_attempt_no"`
	LastError          *string    `json:"last_error"`
	LastErrorTruncated bool       `json:"last_error_truncated"`
}

func (s *Store) ListCycles(ctx context.Context, tenant, id uuid.UUID, after int32, limit int) ([]DeliveryCycle, *int32, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	if err := s.requireEvent(ctx, tenant, id); err != nil {
		return nil, nil, err
	}
	limit = pageLimit(limit)
	rows, err := s.pool.Query(ctx, `SELECT c.cycle_no,c.started_at,c.finished_at,c.replayed_at,c.final_status,
		c.terminal_reason,c.attempt_count,c.max_attempts,c.first_attempt_no,c.last_attempt_no,
		LEFT(c.last_error,2048),COALESCE(char_length(c.last_error)>2048,false)
		FROM delivery_cycles c JOIN events e ON e.id=c.event_id
		WHERE e.tenant_id=$1::uuid AND e.id=$2::uuid AND c.cycle_no>$3
		ORDER BY c.cycle_no LIMIT $4`, tenant.String(), id.String(), after, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make([]DeliveryCycle, 0, limit)
	for rows.Next() {
		var c DeliveryCycle
		if err := rows.Scan(&c.CycleNo, &c.StartedAt, &c.FinishedAt, &c.ReplayedAt, &c.FinalStatus, &c.TerminalReason,
			&c.AttemptCount, &c.MaxAttempts, &c.FirstAttemptNo, &c.LastAttemptNo, &c.LastError, &c.LastErrorTruncated); err != nil {
			return nil, nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *int32
	if len(out) > limit {
		n := out[limit-1].CycleNo
		next = &n
		out = out[:limit]
	}
	return out, next, nil
}

func scanEndpoint(row rowScanner) (Endpoint, error) {
	var e Endpoint
	err := row.Scan(&e.ID, &e.TenantID, &e.URL, &e.Description, &e.Enabled, &e.MaxAttempts, &e.Ordered, &e.CreatedAt, &e.UpdatedAt, &e.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	return e, err
}

// EndpointDetail never reads signing secrets and never probes the URL.
func (s *Store) EndpointDetail(ctx context.Context, tenant, id uuid.UUID) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	return scanEndpoint(s.pool.QueryRow(ctx, `SELECT `+endpointColumns+` FROM endpoints WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), id.String()))
}

func (s *Store) ListEndpointPage(ctx context.Context, tenant uuid.UUID, limit int, before *Cursor) ([]Endpoint, *Cursor, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	limit = pageLimit(limit)
	q := `SELECT ` + endpointColumns + ` FROM endpoints WHERE tenant_id=$1::uuid`
	args := []any{tenant.String()}
	if before != nil {
		q += ` AND (created_at,id)<($2,$3::uuid)`
		args = append(args, before.ReceivedAt, before.ID.String())
	}
	args = append(args, limit+1)
	// There are only two possible arities; values are always bound parameters.
	if before == nil {
		q += ` ORDER BY created_at DESC,id DESC LIMIT $2`
	} else {
		q += ` ORDER BY created_at DESC,id DESC LIMIT $4`
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make([]Endpoint, 0, limit)
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *Cursor
	if len(out) > limit {
		e := out[limit-1]
		next = &Cursor{ReceivedAt: e.CreatedAt, ID: e.ID}
		out = out[:limit]
	}
	return out, next, nil
}

type EndpointPatch struct {
	ExpectedRevision int32
	URL              *string
	Enabled          *bool
}

func (s *Store) UpdateEndpoint(ctx context.Context, tenant, id uuid.UUID, p EndpointPatch) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	e, err := scanEndpoint(tx.QueryRow(ctx, `SELECT `+endpointColumns+` FROM endpoints WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, tenant.String(), id.String()))
	if err != nil {
		return Endpoint{}, err
	}
	if e.Revision != p.ExpectedRevision || e.Revision == math.MaxInt32 || (p.URL == nil && p.Enabled == nil) {
		return Endpoint{}, ErrConflict
	}
	if p.URL != nil {
		e.URL = *p.URL
	}
	if p.Enabled != nil {
		e.Enabled = *p.Enabled
	}
	e, err = scanEndpoint(tx.QueryRow(ctx, `UPDATE endpoints SET url=$3,enabled=$4,revision=revision+1,updated_at=clock_timestamp()
		WHERE tenant_id=$1::uuid AND id=$2::uuid RETURNING `+endpointColumns, tenant.String(), id.String(), e.URL, e.Enabled))
	if err != nil {
		return Endpoint{}, err
	}
	return e, tx.Commit(ctx)
}

// Replay is a durable administrative transition. Only workers create attempts.
func (s *Store) Replay(ctx context.Context, tenant, id uuid.UUID, expected int32) (Event, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	tx, err := s.beginDelivery(ctx)
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var hasLease bool
	e, err := scanEvent(tx.QueryRow(ctx, `SELECT `+strings.Replace(eventColumns, "payload::text", "NULL::text", 1)+`,
		(leased_by IS NOT NULL OR lease_expires_at IS NOT NULL) FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, tenant.String(), id.String()), &hasLease)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, err
	}
	if (e.Status != StatusDelivered && e.Status != StatusDeadLettered) || hasLease || expected != e.ReplayCount || e.ReplayCount == math.MaxInt32 {
		return Event{}, ErrConflict
	}
	var enabled bool
	var budget int16
	if err = tx.QueryRow(ctx, `SELECT enabled,max_attempts FROM endpoints WHERE tenant_id=$1::uuid AND id=$2::uuid FOR SHARE`, tenant.String(), e.EndpointID.String()).Scan(&enabled, &budget); err != nil {
		return Event{}, err
	}
	if !enabled || int64(e.AttemptCount)+int64(budget) > math.MaxInt32 {
		return Event{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_cycles(event_id,cycle_no,started_at,finished_at,final_status,terminal_reason,
		attempt_count,max_attempts,first_attempt_no,last_attempt_no,last_error)
		SELECT id,replay_count,cycle_started_at,cycle_finished_at,status,
		(SELECT reason FROM dead_letters d WHERE d.event_id=events.id),cycle_attempt_count,cycle_max_attempts,
		CASE WHEN cycle_attempt_count>0 THEN attempt_count-cycle_attempt_count+1 END,
		CASE WHEN cycle_attempt_count>0 THEN attempt_count END,last_error FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant.String(), id.String())
	if err != nil {
		return Event{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM dead_letters WHERE event_id IN (SELECT id FROM events WHERE tenant_id=$1::uuid AND id=$2::uuid)`, tenant.String(), id.String()); err != nil {
		return Event{}, err
	}
	e, err = scanEvent(tx.QueryRow(ctx, `UPDATE events SET status='pending',replay_count=replay_count+1,cycle_attempt_count=0,
		cycle_max_attempts=$3,cycle_started_at=clock_timestamp(),cycle_finished_at=NULL,next_attempt_at=clock_timestamp(),
		dispatch_after=clock_timestamp(),delivered_at=NULL,last_error=NULL,leased_by=NULL,lease_expires_at=NULL
		WHERE tenant_id=$1::uuid AND id=$2::uuid RETURNING `+strings.Replace(eventColumns, "payload::text", "NULL::text", 1), tenant.String(), id.String(), budget))
	if err != nil {
		return Event{}, err
	}
	return e, tx.Commit(ctx)
}

type EndpointStatistics struct {
	Available            bool      `json:"available"`
	UnavailableReason    *string   `json:"unavailable_reason"`
	WindowFrom           time.Time `json:"window_from"`
	WindowTo             time.Time `json:"window_to"`
	AttemptLimit         int       `json:"attempt_limit"`
	SuccessHTTPAttempts  *int64    `json:"success_http_attempts"`
	HTTPResponseAttempts *int64    `json:"http_response_attempts"`
	NoResponseAttempts   *int64    `json:"no_response_attempts"`
	NetworkAttempts      *int64    `json:"network_attempts"`
	TimeoutAttempts      *int64    `json:"timeout_attempts"`
	PolicyAttempts       *int64    `json:"policy_attempts"`
	LeaseExpiredAttempts *int64    `json:"lease_expired_attempts"`
	SuccessRate          *float64  `json:"success_rate"`
}

func (s *Store) EndpointStats(ctx context.Context, tenant, id uuid.UUID) (EndpointStatistics, error) {
	return s.endpointStatsAt(ctx, tenant, id, nil)
}

// A fixed window end is used only by package tests; API reads use database time.
func (s *Store) endpointStatsAt(ctx context.Context, tenant, id uuid.UUID, end *time.Time) (EndpointStatistics, error) {
	ctx, cancel := context.WithTimeout(ctx, InspectionTimeout)
	defer cancel()
	if _, err := s.EndpointDetail(ctx, tenant, id); err != nil {
		return EndpointStatistics{}, err
	}
	var out EndpointStatistics
	var total, success, responses, noResponse, network, timeout, policy, expired int64
	err := s.pool.QueryRow(ctx, `WITH w AS MATERIALIZED (SELECT COALESCE($3::timestamptz,clock_timestamp()) AS end_at), sample AS MATERIALIZED (
		SELECT a.outcome,a.response_status,
		COALESCE((d.reason='non_retryable_policy' AND a.cycle_no=e.replay_count AND a.attempt_no=e.attempt_count)
		 OR (c.terminal_reason='non_retryable_policy' AND c.last_attempt_no=a.attempt_no),false) AS policy
		FROM delivery_attempts a JOIN events e ON e.id=a.event_id CROSS JOIN w
		LEFT JOIN dead_letters d ON d.event_id=e.id LEFT JOIN delivery_cycles c ON c.event_id=e.id AND c.cycle_no=a.cycle_no
		WHERE e.tenant_id=$1::uuid AND e.endpoint_id=$2::uuid AND a.finished_at>=w.end_at-interval '24 hours' AND a.finished_at<w.end_at
		ORDER BY a.finished_at,a.event_id,a.attempt_no LIMIT 100001)
		SELECT w.end_at-interval '24 hours',w.end_at,count(sample.outcome),
		count(*) FILTER(WHERE sample.outcome='success' AND sample.response_status IS NOT NULL),
		count(*) FILTER(WHERE sample.response_status IS NOT NULL),
		count(*) FILTER(WHERE sample.outcome IS NOT NULL AND sample.response_status IS NULL),
		count(*) FILTER(WHERE sample.outcome='network'),count(*) FILTER(WHERE sample.outcome='timeout'),
		count(*) FILTER(WHERE sample.policy),count(*) FILTER(WHERE sample.outcome='lease_expired')
		FROM w LEFT JOIN sample ON true GROUP BY w.end_at`, tenant.String(), id.String(), end).Scan(
		&out.WindowFrom, &out.WindowTo, &total, &success, &responses, &noResponse, &network, &timeout, &policy, &expired)
	if err != nil {
		return out, err
	}
	out.AttemptLimit = StatisticsAttemptLimit
	if total > StatisticsAttemptLimit {
		reason := "attempt_limit_exceeded"
		out.UnavailableReason = &reason
		return out, nil
	}
	out.Available = true
	out.SuccessHTTPAttempts = &success
	out.HTTPResponseAttempts = &responses
	out.NoResponseAttempts = &noResponse
	out.NetworkAttempts = &network
	out.TimeoutAttempts = &timeout
	out.PolicyAttempts = &policy
	out.LeaseExpiredAttempts = &expired
	if responses > 0 {
		rate := float64(success) / float64(responses)
		out.SuccessRate = &rate
	}
	return out, nil
}
