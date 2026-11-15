package integration

// M3 retry policy and recovery evidence. These tests require real PostgreSQL
// and Redis and skip when the test URLs are unset (see deploy/README.md).

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/migrations"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

// defaultSchedule adapts the production policy for store-level recovery calls.
func defaultSchedule() func(int) time.Time {
	return func(failedAttempt int) time.Time {
		wait, ok := retry.Default.Wait(failedAttempt, rand.Float64)
		if !ok {
			return time.Now()
		}
		return time.Now().Add(wait)
	}
}

func assertDeadLetterReason(t testing.TB, h *testutil.Harness, eventID, want string) {
	t.Helper()
	var reason string
	if err := h.SQL.QueryRow(context.Background(),
		`SELECT reason FROM dead_letters WHERE event_id=$1`, eventID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != want {
		t.Fatalf("dead letter reason = %q; want %q", reason, want)
	}
}

// remainingSeconds reads how long an event's next attempt is away from the
// database clock, so assertions are independent of host/DB clock skew.
func remainingSeconds(t testing.TB, h *testutil.Harness, eventID string) float64 {
	t.Helper()
	var remaining float64
	if err := h.SQL.QueryRow(context.Background(),
		`SELECT EXTRACT(EPOCH FROM (next_attempt_at - clock_timestamp())) FROM events WHERE id=$1`, eventID,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	return remaining
}

func countAttempts(t testing.TB, h *testutil.Harness, eventID string) int {
	t.Helper()
	var n int
	if err := h.SQL.QueryRow(context.Background(),
		`SELECT count(*) FROM delivery_attempts WHERE event_id=$1`, eventID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitFinishedAttempts blocks until the wanted number of attempt rows are
// closed. Attempt rows exist from claim time, so counting them alone would
// race the fenced completion.
func waitFinishedAttempts(t testing.TB, h *testutil.Harness, eventID string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var finished int
		if err := h.SQL.QueryRow(context.Background(),
			`SELECT count(*) FROM delivery_attempts WHERE event_id=$1 AND finished_at IS NOT NULL`, eventID,
		).Scan(&finished); err != nil {
			t.Fatal(err)
		}
		if finished == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("finished attempts = %d; want %d", finished, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func failServer(t *testing.T, status int, extra func(http.ResponseWriter), calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if extra != nil {
			extra(w)
		}
		w.WriteHeader(status)
	}))
}

func TestRetryAfterSecondsDelaysDelivery(t *testing.T) {
	h := testutil.New(t)
	h.Policy = retry.Default
	var calls atomic.Int32
	target := failServer(t, http.StatusTooManyRequests, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "3600")
	}, &calls)
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	if err := h.Stream.Publish(context.Background(), store.Dispatch{TenantID: e.TenantID, EventID: e.ID}); err != nil {
		t.Fatal(err)
	}
	h.Start(t, 1)
	waitFinishedAttempts(t, h, e.ID.String(), 1)
	if got := h.Status(t, e.ID); got != store.StatusPending {
		t.Fatalf("status = %s; want pending", got)
	}
	// "Retry-After: 3600 after failure 1 therefore waits one hour": the capped
	// header wait must beat the jittered 8-12s policy wait.
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 3500 || remaining > 3600 {
		t.Fatalf("next attempt in %vs; want the 3600s header wait", remaining)
	}
	time.Sleep(300 * time.Millisecond)
	if got := countAttempts(t, h, e.ID.String()); got != 1 {
		t.Fatalf("attempts = %d; the header wait must not be cut short", got)
	}
}

func TestInvalidRetryAfterUsesPolicyWaitWithDiagnostic(t *testing.T) {
	h := testutil.New(t)
	h.Policy = retry.Default
	var calls atomic.Int32
	target := failServer(t, http.StatusTooManyRequests, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "soon")
	}, &calls)
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	if err := h.Stream.Publish(context.Background(), store.Dispatch{TenantID: e.TenantID, EventID: e.ID}); err != nil {
		t.Fatal(err)
	}
	h.Start(t, 1)
	waitFinishedAttempts(t, h, e.ID.String(), 1)
	// The bounded diagnostic is recorded; the raw header value is not.
	want := "HTTP 429; retry-after ignored: invalid value"
	var attemptError, lastError string
	if err := h.SQL.QueryRow(context.Background(),
		`SELECT COALESCE(a.error,''), COALESCE(e.last_error,'')
		 FROM delivery_attempts a JOIN events e ON e.id=a.event_id WHERE a.event_id=$1`,
		e.ID.String()).Scan(&attemptError, &lastError); err != nil {
		t.Fatal(err)
	}
	if attemptError != want || lastError != want {
		t.Fatalf("attempt error %q / last_error %q; want %q", attemptError, lastError, want)
	}
	// The policy wait (10s, jittered 0.8-1.2) applies, not the unusable header.
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 6 || remaining > 12.5 {
		t.Fatalf("next attempt in %vs; want the jittered 10s policy wait", remaining)
	}
}

func TestExhaustionWritesStableReason(t *testing.T) {
	h := testutil.New(t)
	var calls atomic.Int32
	target := failServer(t, http.StatusInternalServerError, nil, &calls)
	ep := h.Endpoint(t, target.URL)
	if _, err := h.SQL.Exec(context.Background(), `UPDATE endpoints SET max_attempts=2 WHERE id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	e := h.Event(t, ep)
	h.Start(t, 1)
	h.Wait(t, e.ID, store.StatusDeadLettered, 3*time.Second)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d; want exactly the two allowed attempts", calls.Load())
	}
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonAttemptsExhausted)
}

func TestInterruptedFinalAttemptDeadLettersWithReason(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	if _, err := h.SQL.Exec(context.Background(), `UPDATE endpoints SET max_attempts=1 WHERE id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	e := h.Event(t, ep)
	ctx := context.Background()
	if _, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "worker", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := h.Store.RecoverExpired(ctx, ep.TenantID, defaultSchedule()); err != nil {
		t.Fatal(err)
	}
	if got := h.Status(t, e.ID); got != store.StatusDeadLettered {
		t.Fatalf("status = %s; want dead_lettered", got)
	}
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonAttemptsExhausted)
	if got := countAttempts(t, h, e.ID.String()); got != 1 {
		t.Fatalf("attempts = %d; recovery must not create an extra uncounted attempt", got)
	}
	var outcome string
	var unfinished bool
	if err := h.SQL.QueryRow(ctx,
		`SELECT outcome, finished_at IS NULL FROM delivery_attempts WHERE event_id=$1`, e.ID.String(),
	).Scan(&outcome, &unfinished); err != nil {
		t.Fatal(err)
	}
	if outcome != "lease_expired" || unfinished {
		t.Fatalf("attempt outcome %q unfinished=%t; want closed lease_expired", outcome, unfinished)
	}
}

func TestRecoverySchedulesPolicyWait(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	ctx := context.Background()
	if _, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "worker", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := h.Store.RecoverExpired(ctx, ep.TenantID, defaultSchedule()); err != nil {
		t.Fatal(err)
	}
	if got := h.Status(t, e.ID); got != store.StatusPending {
		t.Fatalf("status = %s; want pending", got)
	}
	// Nominal wait for failed attempt 1 is 10s, jittered into [8s, 12s].
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 7 || remaining > 12.5 {
		t.Fatalf("next attempt in %vs; want the jittered 10s policy wait", remaining)
	}
}

func TestDuplicateCompletionDoesNotRedrawSchedule(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	ctx := context.Background()
	lease, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "worker", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var dbNow time.Time
	if err := h.SQL.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	result := store.AttemptResult{Outcome: "retryable", Status: 500, Error: "HTTP 500"}
	var draws atomic.Int32
	decide := func() (retry.Decision, error) {
		return retry.Decide(retry.Input{
			Outcome: result.Outcome, StatusCode: result.Status, FailedAttempt: 1, MaxAttempts: 8, CompletedAt: dbNow,
		}, func() float64 { draws.Add(1); return 0.5 })
	}
	if err := h.Store.Finish(ctx, lease, result, decide); err != nil {
		t.Fatal(err)
	}
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 8 || remaining > 10.5 {
		t.Fatalf("next attempt in %vs; want the persisted ~10s", remaining)
	}
	if err := h.Store.Finish(ctx, lease, result, decide); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("duplicate completion: %v; want ErrLeaseLost", err)
	}
	// The persisted time is untouched by the rejected duplicate.
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 8 || remaining > 10.5 {
		t.Fatalf("next attempt moved to %vs after a rejected duplicate", remaining)
	}
	var scheduled time.Time
	if err := h.SQL.QueryRow(ctx, `SELECT next_attempt_at FROM events WHERE id=$1`, e.ID.String()).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if !scheduled.Equal(dbNow.Add(10*time.Second)) || draws.Load() != 1 {
		t.Fatalf("persisted time = %s, jitter draws = %d; want the original time and one draw", scheduled, draws.Load())
	}
}

func TestConcurrentRecoveryClosesAttemptOnce(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	ctx := context.Background()
	if _, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "worker", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var wg sync.WaitGroup
	var recovered atomic.Int64
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := h.Store.RecoverExpired(ctx, ep.TenantID, defaultSchedule())
			if err != nil {
				t.Error(err)
				return
			}
			recovered.Add(n)
		}()
	}
	wg.Wait()
	if recovered.Load() != 1 {
		t.Fatalf("recovered = %d across two sweepers; want exactly one", recovered.Load())
	}
	if got := countAttempts(t, h, e.ID.String()); got != 1 {
		t.Fatalf("attempts = %d; concurrent sweepers duplicated work", got)
	}
	if got := h.Status(t, e.ID); got != store.StatusPending {
		t.Fatalf("status = %s; want pending", got)
	}
}

// subsetMigrations rebuilds the embedded migrations as a MapFS holding only
// the named files, so a test can stand up a genuine pre-0003 database.
func subsetMigrations(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !keep[e.Name()] {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: body}
	}
	return out
}

func seedM2Rows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		// One delivered event with a successful attempt.
		`WITH e AS (INSERT INTO events (tenant_id, endpoint_id, event_type, payload, status, attempt_count, delivered_at)
		 VALUES ('00000000-0000-0000-0000-000000000001'::uuid, (SELECT id FROM endpoints LIMIT 1), 'm2', '{}', 'delivered', 1, now()) RETURNING id)
		 INSERT INTO delivery_attempts (event_id, attempt_no, started_at, finished_at, outcome, response_status, duration_ms)
		 SELECT id, 1, now(), now(), 'success', 204, 5 FROM e`,
		// One exhausted event whose M2 dead letter carries the raw outcome.
		`WITH e AS (INSERT INTO events (tenant_id, endpoint_id, event_type, payload, status, attempt_count, last_error)
		 VALUES ('00000000-0000-0000-0000-000000000001'::uuid, (SELECT id FROM endpoints LIMIT 1), 'm2', '{}', 'dead_lettered', 8, 'HTTP 500') RETURNING id)
		 INSERT INTO delivery_attempts (event_id, attempt_no, started_at, finished_at, outcome, response_status, duration_ms)
		 SELECT id, 8, now(), now(), 'timeout', 500, 5 FROM e`,
		`INSERT INTO dead_letters (event_id, reason, last_error)
		 SELECT id, 'timeout', 'HTTP 500' FROM events WHERE status='dead_lettered' AND attempt_count=8`,
		// One non-retryable event whose M2 dead letter carries the outcome string.
		`WITH e AS (INSERT INTO events (tenant_id, endpoint_id, event_type, payload, status, attempt_count, last_error)
		 VALUES ('00000000-0000-0000-0000-000000000001'::uuid, (SELECT id FROM endpoints LIMIT 1), 'm2', '{}', 'dead_lettered', 1, 'HTTP 400') RETURNING id)
		 INSERT INTO delivery_attempts (event_id, attempt_no, started_at, finished_at, outcome, response_status, duration_ms)
		 SELECT id, 1, now(), now(), 'non_retryable', 400, 5 FROM e`,
		`INSERT INTO dead_letters (event_id, reason, last_error)
		 SELECT id, 'non_retryable', 'HTTP 400' FROM events WHERE status='dead_lettered' AND attempt_count=1`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationNormalizesM2ReasonsAndEnforcesCheck(t *testing.T) {
	dsn := testutil.DatabaseURL(t)
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var schema string
	// Registered before NewSchema: this check runs after the schema drop and
	// before admin.Close, proving the migration fixture cleans up its namespace.
	t.Cleanup(func() {
		if schema == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var exists bool
		if err := admin.QueryRow(cleanupCtx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil {
			t.Error(err)
		} else if exists {
			t.Errorf("migration fixture leaked schema %s", schema)
		}
	})
	schema = testutil.NewSchema(t, admin)
	migrationDSN := testutil.WithSearchPath(t, dsn, schema)

	// Phase 1: an M2 database, before 0003 exists.
	m2, err := pgxpool.New(context.Background(), migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	ctx := context.Background()
	s2, err := store.Open(ctx, migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.Migrate(ctx, subsetMigrations(t, "0001_init.sql", "0002_dispatch.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Exec(ctx, `INSERT INTO endpoints (tenant_id, url, ingest_secret, delivery_secret)
		VALUES ('00000000-0000-0000-0000-000000000001'::uuid, 'https://example.test', 'sec'::bytea, 'sec'::bytea)`); err != nil {
		t.Fatal(err)
	}
	seedM2Rows(t, m2)

	// Phase 2: the M3 upgrade must normalize reasons and keep every row.
	s3, err := store.Open(ctx, migrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if err := s3.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var exhausted, response, letters, attempts, events int
	if err := m2.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE reason='attempts_exhausted'),
		        count(*) FILTER (WHERE reason='non_retryable_response'),
		        (SELECT count(*) FROM dead_letters),
		        (SELECT count(*) FROM delivery_attempts),
		        (SELECT count(*) FROM events)
		 FROM dead_letters`,
	).Scan(&exhausted, &response, &letters, &attempts, &events); err != nil {
		t.Fatal(err)
	}
	if exhausted != 1 || response != 1 || letters != 2 || attempts != 3 || events != 3 {
		t.Fatalf("exhausted=%d response=%d letters=%d attempts=%d events=%d; upgrade changed data",
			exhausted, response, letters, attempts, events)
	}
	// Repeatable.
	if err := s3.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	// The CHECK constraint admits exactly the three identifiers. The delivered
	// event has no dead letter yet, so the insert reaches the constraint.
	if _, err := m2.Exec(ctx,
		`INSERT INTO dead_letters(event_id, reason)
		 VALUES ((SELECT id FROM events WHERE status='delivered' LIMIT 1), 'bogus')`); err == nil {
		t.Fatal("bogus reason accepted; CHECK constraint missing")
	}
	var lettersAfter int
	if err := m2.QueryRow(ctx, `SELECT count(*) FROM dead_letters`).Scan(&lettersAfter); err != nil {
		t.Fatal(err)
	}
	if lettersAfter != 2 {
		t.Fatalf("dead letters = %d after the rejected insert; want 2", lettersAfter)
	}
}
