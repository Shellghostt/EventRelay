package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

func startRegressionWorker(t *testing.T, w *worker.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker shutdown timed out")
		}
		w.Client.Close()
	})
}

func TestBrokenResponseRetryAfterPersistsDelay(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := testutil.New(t)
			h.Policy = retry.Default
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "100")
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("short"))
			}))
			t.Cleanup(target.Close)
			ep := h.Endpoint(t, target.URL)
			e := h.Event(t, ep)
			h.Start(t, 1)
			waitFinishedAttempts(t, h, e.ID.String(), 1)
			if got := h.Status(t, e.ID); got != store.StatusPending {
				t.Fatalf("status = %s; want pending", got)
			}
			if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 3500 || remaining > 3605 {
				t.Fatalf("next attempt in %vs; want the one-hour header wait", remaining)
			}
			var outcome, attemptError, lastError string
			var responseStatus int
			var excerptAbsent bool
			if err := h.SQL.QueryRow(context.Background(),
				`SELECT a.outcome, a.response_status, a.error, e.last_error, a.response_excerpt IS NULL
				 FROM delivery_attempts a JOIN events e ON e.id=a.event_id WHERE e.id=$1`, e.ID.String(),
			).Scan(&outcome, &responseStatus, &attemptError, &lastError, &excerptAbsent); err != nil {
				t.Fatal(err)
			}
			if outcome != "network" || responseStatus != status || attemptError != "response body read failed" ||
				lastError != attemptError || !excerptAbsent {
				t.Fatalf("outcome=%s status=%d error=%q last_error=%q excerpt absent=%t",
					outcome, responseStatus, attemptError, lastError, excerptAbsent)
			}
		})
	}
}

func TestBlockedDestinationWritesPolicyReason(t *testing.T) {
	h := testutil.New(t)
	var calls, draws atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(target.Close)
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	w := worker.Worker{
		Store: h.Store, Stream: h.Stream, Client: delivery.New(time.Second, false),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Concurrency: 1, LeaseDuration: 2 * time.Second, SweepInterval: 25 * time.Millisecond,
		RandFloat64: func() float64 { draws.Add(1); return 0.5 },
	}
	startRegressionWorker(t, &w)
	h.Wait(t, e.ID, store.StatusDeadLettered, 3*time.Second)
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonNonRetryablePolicy)
	var outcome, attemptError string
	var noResponse bool
	if err := h.SQL.QueryRow(context.Background(),
		`SELECT outcome, error, response_status IS NULL FROM delivery_attempts WHERE event_id=$1`, e.ID.String(),
	).Scan(&outcome, &attemptError, &noResponse); err != nil {
		t.Fatal(err)
	}
	if outcome != "network" || attemptError != "delivery connection failed" || !noResponse ||
		countAttempts(t, h, e.ID.String()) != 1 || calls.Load() != 0 || draws.Load() != 0 {
		t.Fatalf("blocked attempt: outcome=%s error=%q no response=%t HTTP calls=%d jitter draws=%d",
			outcome, attemptError, noResponse, calls.Load(), draws.Load())
	}
}

type completionLogHandler struct{ rejected chan struct{} }

func (h completionLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h completionLogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h completionLogHandler) WithGroup(string) slog.Handler            { return h }
func (h completionLogHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "attempt completion not committed; lease recovery will retry" {
		select {
		case h.rejected <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestStaleWorkerCompletionDoesNotDrawJitter(t *testing.T) {
	h := testutil.New(t)
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			w.WriteHeader(http.StatusInternalServerError)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(target.Close)
	defer releaseOnce.Do(func() { close(release) })
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	var draws atomic.Int32
	handler := completionLogHandler{rejected: make(chan struct{}, 1)}
	w := worker.Worker{
		Store: h.Store, Stream: h.Stream, Client: delivery.New(4*time.Second, true), Log: slog.New(handler),
		Concurrency: 1, LeaseDuration: 5 * time.Second, SweepInterval: 25 * time.Millisecond,
		RandFloat64: func() float64 { draws.Add(1); return 0.5 },
	}
	startRegressionWorker(t, &w)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first attempt did not start")
	}
	// Expire the lease while its old HTTP request is still running.
	if _, err := h.SQL.Exec(context.Background(),
		`UPDATE events SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	h.Wait(t, e.ID, store.StatusPending, 3*time.Second)
	if got := draws.Load(); got != 1 {
		t.Fatalf("recovery drew jitter %d times; want one", got)
	}
	var before, after time.Time
	if err := h.SQL.QueryRow(context.Background(), `SELECT next_attempt_at FROM events WHERE id=$1`, e.ID.String()).Scan(&before); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-handler.rejected:
	case <-time.After(3 * time.Second):
		t.Fatal("old completion was not rejected")
	}
	if err := h.SQL.QueryRow(context.Background(), `SELECT next_attempt_at FROM events WHERE id=$1`, e.ID.String()).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) || draws.Load() != 1 || countAttempts(t, h, e.ID.String()) != 1 {
		t.Fatalf("stale completion: schedule unchanged=%t jitter draws=%d; want one recovery draw only",
			after.Equal(before), draws.Load())
	}
	var outcome string
	if err := h.SQL.QueryRow(context.Background(), `SELECT outcome FROM delivery_attempts WHERE event_id=$1`, e.ID.String()).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "lease_expired" {
		t.Fatalf("stale HTTP completion overwrote recovered outcome: %s", outcome)
	}
}

func TestSweepUsesDefaultsBeforeRun(t *testing.T) {
	for _, injected := range []bool{false, true} {
		name := "defaults"
		if injected {
			name = "injected policy and source"
		}
		t.Run(name, func(t *testing.T) {
			h := testutil.New(t)
			ep := h.Endpoint(t, "https://example.test")
			e := h.Event(t, ep)
			ctx := context.Background()
			if _, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "interrupted", time.Second); err != nil {
				t.Fatal(err)
			}
			if _, err := h.SQL.Exec(ctx,
				`UPDATE events SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, e.ID.String()); err != nil {
				t.Fatal(err)
			}
			w := worker.Worker{Store: h.Store, Stream: h.Stream, LeaseDuration: time.Second, SweepInterval: time.Second}
			var draws atomic.Int32
			if injected {
				w.Policy = retry.Policy{
					JitterLow: 0.5, JitterHigh: 1.5,
					Waits: func(int) (time.Duration, bool) { return 20 * time.Second, true },
				}
				w.RandFloat64 = func() float64 { draws.Add(1); return 0.25 }
			}
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := w.Sweep(ctx); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if h.Status(t, e.ID) != store.StatusPending || countAttempts(t, h, e.ID.String()) != 1 {
				t.Fatal("concurrent direct sweeps did not recover exactly one attempt")
			}
			remaining := remainingSeconds(t, h, e.ID.String())
			if injected {
				if remaining < 13 || remaining > 15.5 || draws.Load() != 1 {
					t.Fatalf("injected recovery: wait=%vs draws=%d; want ~15s and one draw", remaining, draws.Load())
				}
			} else if remaining < 6 || remaining > 12.5 {
				t.Fatalf("default recovery wait=%vs; want the jittered 10s policy", remaining)
			}
		})
	}
}

func TestWorkerRetryUsesCompletionTimeBeforeRowLock(t *testing.T) {
	h := testutil.New(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			w.WriteHeader(http.StatusInternalServerError)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(target.Close)
	defer releaseOnce.Do(func() { close(release) })
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	var draws atomic.Int32
	w := worker.Worker{
		Store: h.Store, Stream: h.Stream, Client: delivery.New(4*time.Second, true),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Concurrency: 1, LeaseDuration: 10 * time.Second, SweepInterval: 25 * time.Millisecond,
		RandFloat64: func() float64 { draws.Add(1); return 0.5 },
	}
	startRegressionWorker(t, &w)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first attempt did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := h.SQL.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	if _, err := locked.Exec(ctx, `UPDATE events SET last_error='completion clock test' WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := locked.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	beforeResponse := time.Now().UTC()
	releaseOnce.Do(func() { close(release) })
	for {
		var waiting bool
		if err := h.SQL.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			 WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%AND leased_by=$3%')`, blockerPID,
		).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	completionUpperBound := time.Now().UTC()
	if got := draws.Load(); got != 0 {
		t.Fatalf("completion drew jitter %d times before acquiring its row lock", got)
	}
	if _, err := locked.Exec(ctx, `SELECT pg_sleep(0.5)`); err != nil {
		t.Fatal(err)
	}
	if err := locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	waitFinishedAttempts(t, h, e.ID.String(), 1)
	var scheduled time.Time
	if err := h.SQL.QueryRow(ctx, `SELECT next_attempt_at FROM events WHERE id=$1`, e.ID.String()).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	// The exact completion occurred after the response was released and before
	// Finish started waiting. Lock contention must not add another half-second.
	if scheduled.Before(beforeResponse.Add(10*time.Second-5*time.Millisecond)) ||
		scheduled.After(completionUpperBound.Add(10*time.Second+5*time.Millisecond)) || draws.Load() != 1 {
		t.Fatalf("schedule=%s completion bounds=[%s,%s] draws=%d; lock wait changed scheduling",
			scheduled, beforeResponse, completionUpperBound, draws.Load())
	}
}

func TestRetryDecisionFailurePreservesLease(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.test")
	e := h.Event(t, ep)
	ctx := context.Background()
	lease, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "worker", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected retry decision failure")
	if err := h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "retryable", Status: 503}, func() (retry.Decision, error) {
		return retry.Decision{}, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("retry decision failure = %v; want the original error", err)
	}
	var outcome string
	var unfinished bool
	if err := h.SQL.QueryRow(ctx, `SELECT outcome, finished_at IS NULL FROM delivery_attempts WHERE event_id=$1`, e.ID.String()).Scan(&outcome, &unfinished); err != nil {
		t.Fatal(err)
	}
	if h.Status(t, e.ID) != store.StatusInFlight || outcome != "in_flight" || !unfinished {
		t.Fatalf("failed decision changed the lease: outcome=%s unfinished=%t", outcome, unfinished)
	}
	// A successful attempt never invokes the scheduling callback.
	if err := h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success", Status: 204}, func() (retry.Decision, error) {
		t.Error("successful completion invoked retry scheduling")
		return retry.Decision{}, failure
	}); err != nil {
		t.Fatal(err)
	}
	if h.Status(t, e.ID) != store.StatusDelivered {
		t.Fatal("the valid completion could not commit after a decision failure")
	}
}
