package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
	"github.com/shellghostt/eventrelay/tests/testutil"
)

func TestPublishOutageStillAcceptsAndRecoversDurableEvent(t *testing.T) {
	h := testutil.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	// Reserve an unreachable port without depending on the machine's service list.
	unavailable := httptest.NewServer(http.NotFoundHandler())
	address := strings.TrimPrefix(unavailable.URL, "http://")
	unavailable.Close()
	broken, err := stream.Open("redis://" + address + "/0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broken.Close() }()
	router := api.NewRouter(api.Deps{Store: h.Store, Publisher: broken, RedisPing: broken.Ping,
		Config: config.Config{AdminToken: "test", SignatureTolerance: time.Minute}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis outage readiness: %d", ready.Code)
	}
	body := `{"outage":true}`
	req := httptest.NewRequest("POST", "/v1/endpoints/"+ep.ID.String()+"/events", strings.NewReader(body))
	req.Header.Set(hmacsign.HeaderName, hmacsign.Header(ep.IngestSecret, time.Now().Unix(), []byte(body)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("publish outage rejected durable event: %d %s", rec.Code, rec.Body)
	}
	events, _, err := h.Store.ListEvents(context.Background(), ep.TenantID, store.EventFilter{EndpointID: &ep.ID})
	if err != nil || len(events) != 1 {
		t.Fatalf("durable insert: %v %#v", err, events)
	}
	h.Start(t, 2)
	h.Wait(t, events[0].ID, "delivered", 3*time.Second)
}

func TestAttemptLimitAndGoneEndpoint(t *testing.T) {
	for _, status := range []int{500, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := testutil.New(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer target.Close()
			ep := h.Endpoint(t, target.URL)
			ctx := context.Background()
			if _, err := h.SQL.Exec(ctx, `UPDATE endpoints SET max_attempts=2 WHERE id=$1`, ep.ID.String()); err != nil {
				t.Fatal(err)
			}
			e := h.Event(t, ep)
			h.Start(t, 2)
			h.Wait(t, e.ID, "dead_lettered", 3*time.Second)
			want := 2
			if status == 410 {
				want = 1
			}
			var attempts, terminal, excerpts int
			if err := h.SQL.QueryRow(ctx, `SELECT count(*),count(finished_at),count(response_excerpt) FROM delivery_attempts WHERE event_id=$1`, e.ID.String()).Scan(&attempts, &terminal, &excerpts); err != nil {
				t.Fatal(err)
			}
			var letters int
			if err := h.SQL.QueryRow(ctx, `SELECT count(*) FROM dead_letters WHERE event_id=$1`, e.ID.String()).Scan(&letters); err != nil {
				t.Fatal(err)
			}
			if attempts != want || terminal != want || calls.Load() != int32(want) || letters != 1 || excerpts != 0 {
				t.Fatalf("attempts=%d finished=%d calls=%d dead_letters=%d excerpts=%d", attempts, terminal, calls.Load(), letters, excerpts)
			}
			if status == 410 {
				got, err := h.Store.EndpointByID(ctx, ep.ID)
				if err != nil || got.Enabled {
					t.Fatalf("410 endpoint still enabled: %+v %v", got, err)
				}
				pending := h.Event(t, ep)
				if _, err = h.Store.Claim(ctx, ep.TenantID, pending.ID, "disabled", time.Second); err != store.ErrNotFound {
					t.Fatalf("disabled endpoint claim: %v", err)
				}
			}
		})
	}
}

func TestFailedCompletionRollsBackAttemptAndRecovers(t *testing.T) {
	h := testutil.New(t)
	ctx := context.Background()
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	lease, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "first", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Fail the second write in Finish, after the attempt update has succeeded.
	if _, err = h.SQL.Exec(ctx, `CREATE FUNCTION fail_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='delivered' THEN RAISE EXCEPTION 'injected completion failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = h.SQL.Exec(ctx, `CREATE TRIGGER fail_completion BEFORE UPDATE ON events FOR EACH ROW EXECUTE FUNCTION fail_completion()`); err != nil {
		t.Fatal(err)
	}
	if err = h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success", Duration: time.Millisecond}, time.Second); err == nil {
		t.Fatal("injected completion failure was ignored")
	}
	var outcome string
	var unfinished bool
	if err = h.SQL.QueryRow(ctx, `SELECT outcome,finished_at IS NULL FROM delivery_attempts WHERE event_id=$1`, e.ID.String()).Scan(&outcome, &unfinished); err != nil {
		t.Fatal(err)
	}
	if outcome != "in_flight" || !unfinished {
		t.Fatalf("attempt write was not rolled back: %s unfinished=%t", outcome, unfinished)
	}
	if _, err = h.SQL.Exec(ctx, `DROP TRIGGER fail_completion ON events`); err != nil {
		t.Fatal(err)
	}
	if _, err = h.SQL.Exec(ctx, `UPDATE events SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Store.RecoverExpired(ctx, ep.TenantID); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.Store.Claim(ctx, ep.TenantID, e.ID, "replacement", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.Finish(ctx, fresh, store.AttemptResult{Outcome: "success"}, time.Second); err != nil {
		t.Fatal(err)
	}
	h.Wait(t, e.ID, "delivered", time.Second)
}

func TestDuplicateDispatchAndTenantIsolation(t *testing.T) {
	h := testutil.New(t)
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	ctx := context.Background()
	if _, err := h.Store.Claim(ctx, uuid.New(), e.ID, "wrong-tenant", time.Second); err != store.ErrNotFound {
		t.Fatalf("cross-tenant claim: %v", err)
	}
	for i := 0; i < 20; i++ {
		if err := h.Stream.Publish(ctx, store.Dispatch{TenantID: e.TenantID, EventID: e.ID}); err != nil {
			t.Fatal(err)
		}
	}
	h.Start(t, 2)
	h.Wait(t, e.ID, "delivered", 3*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for {
		length, err := h.Stream.Client.XLen(ctx, h.Stream.Key).Result()
		if err != nil {
			t.Fatal(err)
		}
		if length == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("duplicate dispatch entries did not drain")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate dispatch delivered %d times", calls.Load())
	}
}
