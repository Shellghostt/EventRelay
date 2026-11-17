package integration

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/migrations"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

func TestM4MigrationPreservesM3AndFencesOldWorkers(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.DatabaseURL(t)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := testutil.NewSchema(t, admin)
	dsn = testutil.WithSearchPath(t, dsn, schema)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.Migrate(ctx, subsetMigrations(t, "0001_init.sql", "0002_dispatch.sql", "0003_m3_retry_policy.sql")); err != nil {
		t.Fatal(err)
	}
	sql, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sql.Close)
	tenant := uuid.MustParse(store.DefaultTenantID)
	ep := uuid.New()
	if _, err = sql.Exec(ctx, `INSERT INTO endpoints(id,tenant_id,url,ingest_secret,delivery_secret,max_attempts) VALUES($1,$2,'https://destination.example','ingest','delivery',8)`, ep, tenant); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{retry.ReasonAttemptsExhausted, retry.ReasonNonRetryableResponse, retry.ReasonNonRetryablePolicy} {
		n := 1
		if reason == retry.ReasonAttemptsExhausted {
			n = 8
		}
		id := uuid.New()
		if _, err = sql.Exec(ctx, `INSERT INTO events(id,tenant_id,endpoint_id,event_type,payload,status,attempt_count,last_error) VALUES($1,$2,$3,'upgrade','{"unchanged":true}','dead_lettered',$4,'known M3 diagnostic')`, id, tenant, ep, n); err != nil {
			t.Fatal(err)
		}
		if _, err = sql.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,started_at,finished_at,outcome,response_status) SELECT $1,n,now(),now(),'retryable',500 FROM generate_series(1,$2::int)n`, id, n); err != nil {
			t.Fatal(err)
		}
		if _, err = sql.Exec(ctx, `INSERT INTO dead_letters(event_id,reason) VALUES($1,$2)`, id, reason); err != nil {
			t.Fatal(err)
		}
	}
	delivered, leased := uuid.New(), uuid.New()
	if _, err = sql.Exec(ctx, `INSERT INTO events(id,tenant_id,endpoint_id,event_type,payload,status,attempt_count,delivered_at)
		VALUES($1,$2,$3,'upgrade','{"unchanged":true}','delivered',1,now())`, delivered, tenant, ep); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `INSERT INTO events(id,tenant_id,endpoint_id,event_type,payload,status,attempt_count,leased_by,lease_expires_at)
		VALUES($1,$2,$3,'upgrade','{"unchanged":true}','in_flight',1,'m3-lease',clock_timestamp()-interval '1 second')`, leased, tenant, ep); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{delivered, leased} {
		outcome := "success"
		if id == leased {
			outcome = "in_flight"
		}
		if _, err = sql.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,started_at,outcome) VALUES($1,1,now(),$2)`, id, outcome); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var events, attempts, letters, replays, archives int
	if err = sql.QueryRow(ctx, `SELECT count(*),(SELECT count(*) FROM delivery_attempts),(SELECT count(*) FROM dead_letters),sum(replay_count),(SELECT count(*) FROM delivery_cycles) FROM events`).Scan(&events, &attempts, &letters, &replays, &archives); err != nil {
		t.Fatal(err)
	}
	if events != 5 || attempts != 12 || letters != 3 || replays != 0 || archives != 0 {
		t.Fatalf("upgrade fabricated/lost history: events=%d attempts=%d letters=%d replays=%d archives=%d", events, attempts, letters, replays, archives)
	}
	var unconverted int
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM events WHERE attempt_count<>cycle_attempt_count OR cycle_max_attempts<>8 OR payload<>'{"unchanged":true}'::jsonb`).Scan(&unconverted); err != nil || unconverted != 0 {
		t.Fatalf("cycle-zero conversion=%d err=%v", unconverted, err)
	}
	var unknown int
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM delivery_attempts WHERE endpoint_revision IS NULL`).Scan(&unknown); err != nil || unknown != 12 {
		t.Fatal("migration invented historical endpoint revisions")
	}
	if _, err = sql.Exec(ctx, `UPDATE events SET status='pending',leased_by=NULL,lease_expires_at=NULL WHERE id=$1`, leased); err == nil {
		t.Fatal("old completion/recovery crossed M4 runtime fence")
	}
	if _, err = sql.Exec(ctx, `UPDATE events SET replay_count=replay_count+1 WHERE id=$1`, delivered); err == nil {
		t.Fatal("unmarked replay crossed M4 runtime fence")
	}
	if _, err = s.RecoverExpired(ctx, tenant, func(int) time.Time { return time.Now().Add(10 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE events SET status='in_flight',attempt_count=attempt_count+1 WHERE id=$1`, leased); err == nil {
		t.Fatal("old claim crossed M4 runtime fence after new pool transaction")
	}
	if _, err = s.Replay(ctx, tenant, delivered, 0); err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, tenant, delivered, "m4-after-upgrade", time.Minute)
	if err != nil || l.Event.AttemptCount != 2 || l.Event.CycleAttemptCount != 1 {
		t.Fatalf("upgraded replay claim %+v err=%v", l.Event, err)
	}
	if err = s.Finish(ctx, l, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestM4CounterCapacityAndEndpointRevisionOverflow(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	m4Finish(t, h, e, "success", 204)
	if _, err := h.SQL.Exec(ctx, `UPDATE events SET attempt_count=$2 WHERE id=$1`, e.ID, math.MaxInt32-7); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("insufficient lifetime capacity=%v", err)
	}
	if _, err := h.SQL.Exec(ctx, `UPDATE events SET attempt_count=$2 WHERE id=$1`, e.ID, math.MaxInt32-8); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); err != nil {
		t.Fatalf("whole final budget should fit: %v", err)
	}
	l, err := h.Store.Claim(ctx, e.TenantID, e.ID, "last-capacity", time.Minute)
	if err != nil || l.Event.AttemptCount != math.MaxInt32-7 {
		t.Fatalf("widened attempt count %+v %v", l.Event, err)
	}
	if err = h.Store.Finish(ctx, l, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != nil {
		t.Fatal(err)
	}
	tx, err := h.SQL.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('eventrelay.worker_schema_version','4',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE events SET replay_count=$2 WHERE id=$1`, e.ID, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Store.Replay(ctx, e.TenantID, e.ID, math.MaxInt32); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("replay overflow=%v", err)
	}
	if _, err = h.SQL.Exec(ctx, `UPDATE endpoints SET revision=$2 WHERE id=$1`, ep.ID, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err = h.Store.UpdateEndpoint(ctx, ep.TenantID, ep.ID, store.EndpointPatch{ExpectedRevision: math.MaxInt32, Enabled: &enabled}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("endpoint revision overflow=%v", err)
	}
}

func TestM4StatisticsPersistedResponsesAndReplayHistory(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	m4Finish(t, h, e, "success", 204)
	if _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); err != nil {
		t.Fatal(err)
	}
	m4Finish(t, h, e, "success", 204)
	for _, fixture := range []struct {
		outcome string
		status  int
		policy  bool
	}{{"retryable", 500, false}, {"network", 500, false}, {"network", 0, false}, {"network", 0, true}, {"timeout", 0, false}} {
		x := h.Event(t, ep)
		l, err := h.Store.Claim(ctx, x.TenantID, x.ID, "stats", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		decision := func() (retry.Decision, error) {
			return retry.Default.Decide(retry.Input{Outcome: fixture.outcome, StatusCode: fixture.status, PolicyRejected: fixture.policy, FailedAttempt: 1, MaxAttempts: 8, CompletedAt: time.Now().UTC()}, func() float64 { return 0.5 })
		}
		if err = h.Store.Finish(ctx, l, store.AttemptResult{Outcome: fixture.outcome, Status: fixture.status, PolicyRejected: fixture.policy}, decision); err != nil {
			t.Fatal(err)
		}
		if fixture.policy {
			if _, err = h.Store.Replay(ctx, x.TenantID, x.ID, 0); err != nil {
				t.Fatal(err)
			}
		} // policy reason now lives in a prior cycle.
	}
	expired := h.Event(t, ep)
	if _, err := h.Store.Claim(ctx, expired.TenantID, expired.ID, "expiry-stats", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SQL.Exec(ctx, `UPDATE events SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.RecoverExpired(ctx, expired.TenantID, defaultSchedule()); err != nil {
		t.Fatal(err)
	}
	// An unfinished attempt and attempts clearly outside each UTC window edge.
	inflight := h.Event(t, ep)
	if _, err := h.Store.Claim(ctx, inflight.TenantID, inflight.ID, "unfinished", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SQL.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,cycle_attempt_no,started_at,finished_at,outcome,response_status)
		VALUES($1,10,1,now()-interval '25 hours',now()-interval '24 hours 1 second','success',204),
		($1,11,1,now(),now()+interval '1 minute','success',204)`, e.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := h.Store.EndpointStats(ctx, e.TenantID, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Available || stats.SuccessRate == nil || *stats.SuccessRate != 0.5 || *stats.SuccessHTTPAttempts != 2 || *stats.HTTPResponseAttempts != 4 || *stats.NoResponseAttempts != 4 || *stats.NetworkAttempts != 3 || *stats.PolicyAttempts != 1 || *stats.TimeoutAttempts != 1 || *stats.LeaseExpiredAttempts != 1 || stats.WindowTo.Sub(stats.WindowFrom) != 24*time.Hour {
		t.Fatalf("wrong observed statistics: %+v", stats)
	}
	w := m4Request(t, m4Router(h), "GET", "/v1/endpoints/"+ep.ID.String()+"/stats", "", true, 200)
	var wire store.EndpointStatistics
	if err = json.Unmarshal(w.Body.Bytes(), &wire); err != nil || !wire.Available {
		t.Fatalf("stats API=%s err=%v", w.Body, err)
	}
	empty := h.Endpoint(t, "https://empty.example")
	zero, err := h.Store.EndpointStats(ctx, e.TenantID, empty.ID)
	if err != nil || !zero.Available || zero.SuccessRate != nil || zero.HTTPResponseAttempts == nil || *zero.HTTPResponseAttempts != 0 {
		t.Fatalf("zero denominator %+v err=%v", zero, err)
	}
	if _, err = h.Store.EndpointStats(ctx, uuid.New(), ep.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign stats=%v", err)
	}
}

func TestM4StatisticsAttemptLimitUnavailable(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	if _, err := h.SQL.Exec(ctx, `INSERT INTO delivery_attempts(event_id,attempt_no,cycle_attempt_no,started_at,finished_at,outcome,response_status)
		SELECT $1,n,1,now(),now(),'success',204 FROM generate_series(1,100001)n`, e.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := h.Store.EndpointStats(ctx, e.TenantID, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Available || stats.UnavailableReason == nil || *stats.UnavailableReason != "attempt_limit_exceeded" || stats.SuccessRate != nil || stats.HTTPResponseAttempts != nil {
		t.Fatalf("partial aggregate presented as complete: %+v", stats)
	}
}
