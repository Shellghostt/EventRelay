package integration

import (
	"context"
	"errors"
	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
	"github.com/shellghostt/eventrelay/tests/testutil"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestThroughDeliveryAndRetry(t *testing.T) {
	for _, status := range []int{200, 500, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := testutil.New(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if status == 500 && n > 2 {
					w.WriteHeader(200)
				} else {
					w.WriteHeader(status)
				}
			}))
			defer target.Close()
			ep := h.Endpoint(t, target.URL)
			h.Start(t, 2)
			router := api.NewRouter(api.Deps{Store: h.Store, Publisher: h.Stream, Config: config.Config{AdminToken: "test", SignatureTolerance: time.Minute}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			body := `{"hello":"world"}`
			req := httptest.NewRequest("POST", "/v1/endpoints/"+ep.ID.String()+"/events", strings.NewReader(body))
			req.Header.Set(hmacsign.HeaderName, hmacsign.Header(ep.IngestSecret, time.Now().Unix(), []byte(body)))
			start := time.Now()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 202 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			events, _, err := h.Store.ListEvents(context.Background(), ep.TenantID, store.EventFilter{EndpointID: &ep.ID})
			if err != nil || len(events) != 1 {
				t.Fatalf("%v %#v", err, events)
			}
			wanted := "delivered"
			attempts := 1
			if status == 400 {
				wanted = "dead_lettered"
			}
			if status == 500 {
				attempts = 3
			}
			h.Wait(t, events[0].ID, wanted, 3*time.Second)
			if status == 200 && time.Since(start) > time.Second {
				t.Fatal("idle delivery exceeded 1s")
			}
			var total, finished int
			err = h.SQL.QueryRow(context.Background(), `SELECT count(*),count(duration_ms) FROM delivery_attempts WHERE event_id=$1`, events[0].ID.String()).Scan(&total, &finished)
			if err != nil || total != attempts || finished != attempts || calls.Load() != int32(attempts) {
				t.Fatalf("attempts %d finished %d calls %d error %v", total, finished, calls.Load(), err)
			}
		})
	}
}
func TestExclusiveLeaseAndStaleCompletion(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	ctx := context.Background()
	leases := make(chan store.Lease, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := h.Store.Claim(ctx, e.TenantID, e.ID, "competitor", 80*time.Millisecond)
			if err == nil {
				leases <- l
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(leases)
	if len(leases) != 1 {
		t.Fatalf("%d claims succeeded", len(leases))
	}
	old := <-leases
	time.Sleep(100 * time.Millisecond)
	if _, err := h.Store.RecoverExpired(ctx, e.TenantID); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.Store.Claim(ctx, e.TenantID, e.ID, "replacement", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.Finish(ctx, old, store.AttemptResult{Outcome: "success"}, time.Second); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = h.Store.Finish(ctx, fresh, store.AttemptResult{Outcome: "success"}, time.Second); err != nil {
		t.Fatal(err)
	}
}
func TestRedisStreamLossRecoversFromPostgres(t *testing.T) {
	h := testutil.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	if err := h.Stream.Publish(context.Background(), store.Dispatch{TenantID: e.TenantID, EventID: e.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.Stream.Client.Del(context.Background(), h.Stream.Key).Err(); err != nil {
		t.Fatal(err)
	}
	h.Start(t, 2)
	h.Wait(t, e.ID, "delivered", 3*time.Second)
}

func TestCompletionWaitingForRowLockRejectsExpiredLease(t *testing.T) {
	h := testutil.New(t)
	ep := h.Endpoint(t, "https://example.org")
	e := h.Event(t, ep)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := h.Store.Claim(ctx, e.TenantID, e.ID, "blocked", 400*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := h.SQL.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	if _, err = locked.Exec(ctx, `UPDATE events SET last_error='lock contention' WHERE id=$1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Store.Finish(ctx, lease, store.AttemptResult{Outcome: "success"}, time.Second) }()
	// Prove Finish began before releasing the row lock, rather than relying on
	// goroutine scheduling. PostgreSQL's now() remains that transaction's start.
	for {
		var waiting int
		if err = h.SQL.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%AND leased_by=$3%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = locked.Exec(ctx, `SELECT pg_sleep(0.5)`); err != nil {
		t.Fatal(err)
	}
	if err = locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("expired completion after lock wait: %v", err)
	}
}

func TestWorkerProcess(t *testing.T) {
	if os.Getenv("ER_CRASH_HELPER") != "1" {
		return
	}
	s, err := store.Open(context.Background(), os.Getenv("ER_HELPER_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q, err := stream.Open(os.Getenv("EVENTRELAY_TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	q.Key = os.Getenv("ER_HELPER_KEY")
	c := delivery.New(time.Second, true)
	defer c.Close()
	w := worker.Worker{Store: s, Stream: q, Client: c, Concurrency: 1, LeaseDuration: 2 * time.Second, SweepInterval: 50 * time.Millisecond}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestKilledWorkerRecoversAfterLeaseExpiry(t *testing.T) {
	h := testutil.New(t)
	hit := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hit <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	defer close(release)
	ep := h.Endpoint(t, target.URL)
	e := h.Event(t, ep)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerProcess$")
	cmd.Env = append(os.Environ(), "ER_CRASH_HELPER=1", "ER_HELPER_DSN="+h.DSN, "ER_HELPER_KEY="+h.Stream.Key)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("worker never started attempt")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	h.Start(t, 2)
	h.Wait(t, e.ID, "delivered", 6*time.Second)
	var expired int
	if err := h.SQL.QueryRow(context.Background(), `SELECT count(*) FROM delivery_attempts WHERE event_id=$1 AND outcome='lease_expired'`, e.ID.String()).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 1 || calls.Load() != 2 {
		t.Fatalf("expired=%d calls=%d", expired, calls.Load())
	}
}
