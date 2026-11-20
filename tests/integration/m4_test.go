package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

const m4Token = "m4-synthetic-admin-token"

func m4Router(h *testutil.Harness) http.Handler {
	return api.NewRouter(api.Deps{Store: h.Store, Config: config.Config{AdminToken: m4Token},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Publisher: h.Stream})
}

func m4Finish(t *testing.T, h *testutil.Harness, e store.Event, outcome string, status int) store.Lease {
	t.Helper()
	ctx := context.Background()
	l, err := h.Store.Claim(ctx, e.TenantID, e.ID, "fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	decide := func() (retry.Decision, error) {
		return retry.Default.Decide(retry.Input{Outcome: outcome, StatusCode: status,
			FailedAttempt: int(l.Event.CycleAttemptCount), MaxAttempts: int(l.Event.CycleMaxAttempts), CompletedAt: time.Now().UTC()}, func() float64 { return 0.5 })
	}
	if err = h.Store.Finish(ctx, l, store.AttemptResult{Outcome: outcome, Status: status}, decide); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestM4ConcurrentReplayAndStateGuards(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	ctx := context.Background()
	m4Finish(t, h, e, "success", 204)
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent replay accepted %d", accepted)
	}
	router := m4Router(h)
	path := "/v1/events/" + e.ID.String() + "/replay"
	m4Request(t, router, "POST", path, `{"expected_replay_count":1}`, true, 409)
	lease, err := h.Store.Claim(ctx, e.TenantID, e.ID, "active", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m4Request(t, router, "POST", path, `{"expected_replay_count":1}`, true, 409)
	if err = h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = h.SQL.Exec(ctx, `UPDATE endpoints SET enabled=false WHERE id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	m4Request(t, router, "POST", path, `{"expected_replay_count":1}`, true, 409)
	canceled := h.Event(t, ep)
	if _, err = h.SQL.Exec(ctx, `UPDATE events SET status='canceled' WHERE id=$1`, canceled.ID.String()); err != nil {
		t.Fatal(err)
	}
	m4Request(t, router, "POST", "/v1/events/"+canceled.ID.String()+"/replay", `{"expected_replay_count":0}`, true, 409)
	m4Request(t, router, "POST", "/v1/events/"+uuid.NewString()+"/replay", `{"expected_replay_count":0}`, true, 404)
	for _, body := range []string{`{}`, `null`, `{"expected_replay_count":-1}`, `{"expected_replay_count":0,"extra":true}`, `{"expected_replay_count":0} {}`, `{"expected_replay_count":2147483648}`} {
		m4Request(t, router, "POST", path, body, true, 422)
	}
}

func TestM4ExhaustedReplayAndFinalExpiry(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	if _, err := h.SQL.Exec(ctx, `UPDATE endpoints SET max_attempts=2 WHERE id=$1`, ep.ID.String()); err != nil {
		t.Fatal(err)
	}
	e := h.Event(t, ep)
	for i := 0; i < 2; i++ {
		m4Finish(t, h, e, "retryable", 500)
		if _, err := h.SQL.Exec(ctx, `UPDATE events SET next_attempt_at=clock_timestamp() WHERE id=$1`, e.ID.String()); err != nil {
			t.Fatal(err)
		}
	}
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonAttemptsExhausted)
	if _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); err != nil {
		t.Fatal(err)
	}
	var letters int
	if err := h.SQL.QueryRow(ctx, `SELECT count(*) FROM dead_letters WHERE event_id=$1`, e.ID.String()).Scan(&letters); err != nil || letters != 0 {
		t.Fatalf("active DLQ after replay=%d err=%v", letters, err)
	}
	l := m4Finish(t, h, e, "retryable", 500)
	if l.Event.AttemptCount != 3 || l.Event.CycleAttemptCount != 1 {
		t.Fatalf("new cycle counters: %+v", l.Event)
	}
	if remaining := remainingSeconds(t, h, e.ID.String()); remaining < 8 || remaining > 10.5 {
		t.Fatalf("replay did not restart M3 backoff: %fs", remaining)
	}
	if _, err := h.SQL.Exec(ctx, `UPDATE events SET next_attempt_at=clock_timestamp() WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	last, err := h.Store.Claim(ctx, e.TenantID, e.ID, "interrupted-final", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.SQL.Exec(ctx, `UPDATE events SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	counts := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := h.Store.RecoverExpired(ctx, e.TenantID, func(int) time.Time { t.Error("exhausted recovery requested a schedule"); return time.Now() })
			if err != nil {
				t.Error(err)
			}
			counts <- n
		}()
	}
	wg.Wait()
	close(counts)
	var recovered int64
	for n := range counts {
		recovered += n
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d", recovered)
	}
	if err = h.Store.Finish(ctx, last, store.AttemptResult{Outcome: "success", Status: 204}, nil); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("expired completion: %v", err)
	}
	if h.Status(t, e.ID) != store.StatusDeadLettered || countAttempts(t, h, e.ID.String()) != 4 {
		t.Fatal("replay expiry failed to consume its bounded budget")
	}
	assertDeadLetterReason(t, h, e.ID.String(), retry.ReasonAttemptsExhausted)
	cycles, _, err := h.Store.ListCycles(ctx, e.TenantID, e.ID, -1, 50)
	if err != nil || len(cycles) != 1 || cycles[0].TerminalReason == nil || *cycles[0].TerminalReason != retry.ReasonAttemptsExhausted {
		t.Fatalf("lost prior terminal evidence: %+v err=%v", cycles, err)
	}
	if _, err = h.Store.Replay(ctx, e.TenantID, e.ID, 1); err != nil {
		t.Fatal(err)
	}
	m4Finish(t, h, e, "success", 204)
	if h.Status(t, e.ID) != store.StatusDelivered || countAttempts(t, h, e.ID.String()) != 5 {
		t.Fatal("exhausted event did not deliver with monotonic identity")
	}
}

func TestM4ReplayRedisInterruptionAndRestore(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	m4Finish(t, h, e, "success", 204)
	if err := h.Stream.Client.Close(); err != nil {
		t.Fatal(err)
	}
	m4Request(t, m4Router(h), "POST", "/v1/events/"+e.ID.String()+"/replay", `{"expected_replay_count":0}`, true, 202)
	disconnected := worker.Worker{Store: h.Store, Stream: h.Stream, LeaseDuration: time.Second, SweepInterval: 20 * time.Millisecond}
	if err := disconnected.Sweep(ctx); err == nil {
		t.Fatal("Redis interruption was not exercised")
	}
	if h.Status(t, e.ID) != store.StatusPending || countAttempts(t, h, e.ID.String()) != 1 {
		t.Fatal("outage lost replay or created an HTTP attempt")
	}
	restored, err := stream.Open(h.RedisURL)
	if err != nil {
		t.Fatal(err)
	}
	restored.Key = h.Stream.Key
	t.Cleanup(func() { _ = restored.Close() })
	c := delivery.New(time.Second, true)
	w := worker.Worker{Store: h.Store, Stream: restored, Client: c, Concurrency: 1, LeaseDuration: 2 * time.Second, SweepInterval: 20 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	startRegressionWorker(t, &w)
	h.Wait(t, e.ID, store.StatusDelivered, 3*time.Second)
	if countAttempts(t, h, e.ID.String()) != 2 {
		t.Fatal("restored durable dispatcher lost history")
	}
	if calls.Load() != 1 {
		t.Fatalf("replay destination calls=%d", calls.Load())
	}
}

func TestM4InspectionBoundsPaginationAndFilters(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	m4Finish(t, h, e, "success", 204)
	if _, err := h.Store.Replay(ctx, e.TenantID, e.ID, 0); err != nil {
		t.Fatal(err)
	}
	m4Finish(t, h, e, "success", 204)
	router := m4Router(h)
	first := m4Request(t, router, "GET", "/v1/events/"+e.ID.String()+"/attempts?limit=1", "", true, 200)
	var page struct {
		Attempts []store.Attempt `json:"attempts"`
		Next     string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Attempts) != 1 || page.Attempts[0].AttemptNo != 1 || page.Next == "" || page.Attempts[0].ResponseBodyAvailable || page.Attempts[0].ResponseHeadersAvailable || page.Attempts[0].ResponseExcerpt != nil {
		t.Fatal("attempt evidence or page contract incorrect")
	}
	m4Request(t, router, "GET", "/v1/events/"+e.ID.String()+"/attempts?limit=1&cursor="+page.Next, "", true, 200)
	m4Request(t, router, "GET", "/v1/events/"+e.ID.String()+"/cycles?cursor="+page.Next, "", true, 422)
	m4Request(t, router, "GET", "/v1/events/"+uuid.NewString()+"/attempts?cursor="+page.Next, "", true, 422)
	if _, err := h.SQL.Exec(ctx, `UPDATE events SET payload=jsonb_build_object('large',repeat('ü',300000)),last_error=repeat('🦉',4096) WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SQL.Exec(ctx, `UPDATE delivery_attempts SET error=repeat('🦉',4096) WHERE event_id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	detail := m4Request(t, router, "GET", "/v1/events/"+e.ID.String(), "", true, 200)
	if !strings.Contains(detail.Body.String(), `"payload_available":false`) || !strings.Contains(detail.Body.String(), `"payload":null`) || !strings.Contains(detail.Body.String(), `"last_error_truncated":true`) || detail.Body.Len() > 20000 {
		t.Fatal("large detail was not explicitly bounded")
	}
	attempts, _, err := h.Store.ListAttempts(ctx, e.TenantID, e.ID, 0, 50)
	if err != nil || len(attempts) != 2 || attempts[0].Error == nil || len([]rune(*attempts[0].Error)) != 2048 || !attempts[0].ErrorTruncated {
		t.Fatalf("unbounded diagnostic %+v err=%v", attempts, err)
	}
	for i := 0; i < 4; i++ {
		h.Event(t, ep)
	}
	var events struct {
		Events []struct {
			ID uuid.UUID `json:"id"`
		} `json:"events"`
		Next string `json:"next_cursor"`
	}
	f := m4Request(t, router, "GET", "/v1/events?view=summary&limit=2", "", true, 200)
	if err = json.Unmarshal(f.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range events.Events {
		seen[item.ID] = true
	}
	h.Event(t, ep) // A newer insertion must not disturb traversal of older rows.
	cursor := events.Next
	m4Request(t, router, "GET", "/v1/events?view=summary&status=delivered&cursor="+cursor, "", true, 422)
	m4Request(t, router, "GET", "/v1/events?view=summary&cursor="+cursor+"tampered", "", true, 422)
	for cursor != "" {
		w := m4Request(t, router, "GET", "/v1/events?view=summary&limit=2&cursor="+cursor, "", true, 200)
		events.Next = "" // JSON null does not overwrite an existing Go string.
		if err = json.Unmarshal(w.Body.Bytes(), &events); err != nil {
			t.Fatal(err)
		}
		for _, item := range events.Events {
			if seen[item.ID] {
				t.Fatal("duplicate cursor row")
			}
			seen[item.ID] = true
		}
		cursor = events.Next
	}
	if len(seen) != 5 {
		t.Fatalf("cursor skipped an unchanged older row: %d", len(seen))
	}
	for _, q := range []string{"from=bad", "from=2026-11-17T12:00:00Z&to=2026-11-17T11:00:00Z", "view=payload", "limit=201"} {
		m4Request(t, router, "GET", "/v1/events?"+q, "", true, 422)
	}
	window := m4Request(t, router, "GET", "/v1/events?view=summary&from=2020-01-01T00:00:00Z&to=2030-01-01T00:00:00Z&limit=1", "", true, 200)
	if err = json.Unmarshal(window.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	m4Request(t, router, "GET", "/v1/events?view=summary&from=2021-01-01T00:00:00Z&to=2030-01-01T00:00:00Z&cursor="+events.Next, "", true, 422)
	endpointPage := m4Request(t, router, "GET", "/v1/endpoints?limit=1", "", true, 200)
	if !strings.Contains(endpointPage.Body.String(), `"next_cursor"`) {
		t.Fatal("endpoint pagination absent")
	}
	longFrom := "2020-01-01T00:00:00." + strings.Repeat("0", 2048) + "Z"
	longPage := m4Request(t, router, "GET", "/v1/events?view=summary&limit=1&from="+longFrom, "", true, 200)
	if err = json.Unmarshal(longPage.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if events.Next == "" || len(events.Next) > 2048 {
		t.Fatal("filter spelling inflated the cursor beyond its accepted bound")
	}
	m4Request(t, router, "GET", "/v1/events?view=summary&limit=1&from=2020-01-01T00:00:00Z&cursor="+events.Next, "", true, 200)
}

func TestM4InspectionTimeoutAndResponseLimit(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://destination.example")
	e := h.Event(t, ep)
	router := m4Router(h)
	tx, err := h.SQL.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `LOCK TABLE events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	m4Request(t, router, "GET", "/v1/events/"+e.ID.String(), "", true, 503)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("inspection deadline took %s", elapsed)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = h.SQL.Exec(ctx, `INSERT INTO events(tenant_id,endpoint_id,event_type,payload,last_error)
		SELECT $1,$2,'bounded','{}',repeat('🦉',2048) FROM generate_series(1,200)`, ep.TenantID, ep.ID); err != nil {
		t.Fatal(err)
	}
	w := m4Request(t, router, "GET", "/v1/events?view=summary&limit=200", "", true, 503)
	if w.Body.Len() > 1<<20 {
		t.Fatal("response exceeded 1 MiB")
	}
	m4Request(t, router, "GET", "/v1/events?view=summary&limit=50", "", true, 200)
}

func m4Request(t *testing.T, router http.Handler, method, path, body string, auth bool, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer "+m4Token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		sample := w.Body.String()
		if len(sample) > 512 {
			sample = sample[:512]
		}
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, w.Code, want, sample)
	}
	return w
}

func TestM4InspectionAuthOwnershipAndSummary(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://destination.example/hooks")
	e := h.Event(t, ep)
	router := m4Router(h)
	path := "/v1/events/" + e.ID.String()
	m4Request(t, router, "GET", path, "", false, 401)
	detail := m4Request(t, router, "GET", path, "", true, 200)
	var got struct {
		ID               uuid.UUID       `json:"id"`
		Payload          json.RawMessage `json:"payload"`
		CycleMaxAttempts int             `json:"cycle_max_attempts"`
		PayloadAvailable bool            `json:"payload_available"`
		TerminalReason   *string         `json:"terminal_reason"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != e.ID || !got.PayloadAvailable || got.CycleMaxAttempts != 8 || got.TerminalReason != nil || !bytes.Contains(got.Payload, []byte("world")) {
		t.Fatalf("unexpected detail: %+v", got)
	}
	summary := m4Request(t, router, "GET", "/v1/events?view=summary", "", true, 200)
	if strings.Contains(summary.Body.String(), "payload") || strings.Contains(summary.Body.String(), "world") {
		t.Fatal("summary exposed payload")
	}
	legacy := m4Request(t, router, "GET", "/v1/events", "", true, 200)
	if !strings.Contains(legacy.Body.String(), "world") {
		t.Fatal("legacy event list lost its payload contract")
	}
	endpoint := m4Request(t, router, "GET", "/v1/endpoints/"+ep.ID.String(), "", true, 200)
	if strings.Contains(endpoint.Body.String(), "secret") {
		t.Fatal("endpoint detail exposed secrets")
	}
	other := uuid.New()
	if _, err := h.SQL.Exec(context.Background(), "INSERT INTO tenants(id,name) VALUES($1,'other')", other.String()); err != nil {
		t.Fatal(err)
	}
	foreign, err := h.Store.CreateEndpoint(context.Background(), store.CreateEndpointInput{TenantID: other, URL: ep.URL,
		IngestSecret: []byte("foreign-ingest"), DeliverySecret: []byte("foreign-delivery"), MaxAttempts: 8})
	if err != nil {
		t.Fatal(err)
	}
	foreignEvent := h.Event(t, foreign)
	m4Request(t, router, "GET", "/v1/events/"+foreignEvent.ID.String(), "", true, 404)
	m4Request(t, router, "GET", "/v1/endpoints/"+foreign.ID.String(), "", true, 404)
	m4Request(t, router, "GET", "/v1/events/"+uuid.NewString(), "", true, 404)
}

func TestM4ReplayPreservesHistoryAndRejectsStaleRequests(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://destination.example/hooks")
	e := h.Event(t, ep)
	ctx := context.Background()
	lease, err := h.Store.Claim(ctx, e.TenantID, e.ID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != nil {
		t.Fatal(err)
	}
	router := m4Router(h)
	path := "/v1/events/" + e.ID.String() + "/replay"
	m4Request(t, router, "POST", path, `{"expected_replay_count":0}`, true, 202)
	m4Request(t, router, "POST", path, `{"expected_replay_count":0}`, true, 409)
	var count, cycle, replays, attempts, archives int
	if err = h.SQL.QueryRow(ctx, "SELECT attempt_count,cycle_attempt_count,replay_count FROM events WHERE id=$1", e.ID.String()).Scan(&count, &cycle, &replays); err != nil {
		t.Fatal(err)
	}
	if err = h.SQL.QueryRow(ctx, "SELECT count(*) FROM delivery_attempts WHERE event_id=$1", e.ID.String()).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = h.SQL.QueryRow(ctx, "SELECT count(*) FROM delivery_cycles WHERE event_id=$1 AND cycle_no=0 AND final_status='delivered'", e.ID.String()).Scan(&archives); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cycle != 0 || replays != 1 || attempts != 1 || archives != 1 {
		t.Fatalf("replay mutated attempt history: lifetime=%d cycle=%d replays=%d attempts=%d archives=%d", count, cycle, replays, attempts, archives)
	}
	next, err := h.Store.Claim(ctx, e.TenantID, e.ID, "second", time.Minute)
	if err != nil || next.Event.AttemptCount != 2 {
		t.Fatalf("next lifetime claim=%+v err=%v", next, err)
	}
	if err = h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != store.ErrLeaseLost {
		t.Fatalf("old completion accepted after replay: %v", err)
	}
	if err = h.Store.Finish(ctx, next, store.AttemptResult{Outcome: "success", Status: 204}, nil); err != nil {
		t.Fatal(err)
	}
	// Even after a rapid delivery, a retransmitted old request cannot start another cycle.
	m4Request(t, router, "POST", path, `{"expected_replay_count":0}`, true, 409)
	m4Request(t, router, "GET", "/v1/events/"+e.ID.String()+"/cycles", "", true, 200)
}

func TestM4EndpointUpdateProtectsAgainstOld410(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://old.example/hooks")
	e := h.Event(t, ep)
	ctx := context.Background()
	lease, err := h.Store.Claim(ctx, e.TenantID, e.ID, "old-destination", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	router := m4Router(h)
	path := "/v1/endpoints/" + ep.ID.String()
	for _, body := range []string{`{"expected_revision":1,"url":null,"enabled":true}`, `{"expected_revision":1,"max_attempts":2}`, `{"expected_revision":1,"url":"file:///tmp/local"}`, `{"expected_revision":1,"url":"http://user:password@example.test"}`} {
		m4Request(t, router, "PATCH", path, body, true, 422)
	}
	m4Request(t, router, "PATCH", path, `{"expected_revision":1,"url":"https://corrected.example/hooks","enabled":true}`, true, 200)
	m4Request(t, router, "PATCH", path, `{"expected_revision":1,"enabled":false}`, true, 409)
	decide := func() (retry.Decision, error) {
		return retry.Default.Decide(retry.Input{Outcome: "non_retryable", StatusCode: 410, FailedAttempt: 1, MaxAttempts: 8,
			CompletedAt: time.Now().UTC()}, func() float64 { return 0.5 })
	}
	if err = h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "non_retryable", Status: 410}, decide); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var target string
	if err = h.SQL.QueryRow(ctx, "SELECT enabled,url FROM endpoints WHERE id=$1", ep.ID.String()).Scan(&enabled, &target); err != nil {
		t.Fatal(err)
	}
	if !enabled || target != "https://corrected.example/hooks" {
		t.Fatal("old 410 disabled the corrected endpoint")
	}
}
