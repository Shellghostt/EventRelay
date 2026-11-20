// This loopback-only server seeds synthetic evidence in an isolated schema for
// Playwright. It is not an EventRelay deployment command.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signals)
	defer cancel()
	dsn, redisURL := os.Getenv("EVENTRELAY_TEST_DATABASE_URL"), os.Getenv("EVENTRELAY_TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		return errors.New("browser fixture requires disposable EVENTRELAY_TEST_DATABASE_URL and EVENTRELAY_TEST_REDIS_URL")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:58084")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer admin.Close()
	schema := "er_browser_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn = u.String()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer s.Close()
	if err = s.Migrate(ctx, migrations.FS); err != nil {
		return err
	}
	transport, err := stream.Open(redisURL)
	if err != nil {
		return err
	}
	transport.Key = schema
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, transport.Client.Del(cleanup, transport.Key).Err(), transport.Close())
	}()
	if err = transport.Ensure(ctx); err != nil {
		return err
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	tenant := uuid.MustParse(store.DefaultTenantID)
	fixtures := map[string]string{}
	createEndpoint := func(description string, enabled bool) (store.Endpoint, error) {
		ep, err := s.CreateEndpoint(ctx, store.CreateEndpointInput{TenantID: tenant, URL: target.URL + "/" + strings.ReplaceAll(strings.ToLower(description), " ", "-"), Description: description, IngestSecret: []byte("synthetic-ingest-secret"), DeliverySecret: []byte("synthetic-delivery-secret"), MaxAttempts: 8})
		if err != nil {
			return ep, err
		}
		if !enabled {
			value := false
			ep, err = s.UpdateEndpoint(ctx, tenant, ep.ID, store.EndpointPatch{ExpectedRevision: ep.Revision, Enabled: &value})
		}
		return ep, err
	}
	billing, err := createEndpoint("Billing hooks", true)
	if err != nil {
		return err
	}
	fixtures["endpoint"] = billing.ID.String()
	analytics, err := createEndpoint("Analytics sink", true)
	if err != nil {
		return err
	}
	disabled, err := createEndpoint("Paused sandbox", false)
	if err != nil {
		return err
	}
	fixtures["disabled_endpoint"] = disabled.ID.String()
	empty, err := createEndpoint("Empty destination", true)
	if err != nil {
		return err
	}
	fixtures["empty_endpoint"] = empty.ID.String()
	finish := func(e store.Event, outcome string, status int, terminal bool) error {
		l, err := s.Claim(ctx, tenant, e.ID, "browser-seed", time.Minute)
		if err != nil {
			return err
		}
		decide := func() (retry.Decision, error) {
			if terminal {
				return retry.Decision{Terminal: true, Reason: retry.ReasonNonRetryableResponse}, nil
			}
			return retry.Default.Decide(retry.Input{Outcome: outcome, StatusCode: status, FailedAttempt: int(l.Event.CycleAttemptCount), MaxAttempts: 8, CompletedAt: time.Now().UTC()}, func() float64 { return 0.5 })
		}
		return s.Finish(ctx, l, store.AttemptResult{Outcome: outcome, Status: status, Duration: 5 * time.Millisecond}, decide)
	}
	// Seed completed synthetic history before starting a real replay worker.
	for i := 0; i < 30; i++ {
		ep := billing
		if i%3 == 0 {
			ep = analytics
		}
		e, err := s.InsertEvent(ctx, tenant, ep.ID, []string{"invoice.created", "payment.confirmed", "subscription.updated"}[i%3], []byte(`{"fixture":"synthetic","order":"ER-M4"}`), "")
		if err != nil {
			return err
		}
		if err = finish(e, "success", 204, false); err != nil {
			return err
		}
		if i == 29 {
			fixtures["delivered"] = e.ID.String()
		}
	}
	dead, err := s.InsertEvent(ctx, tenant, billing.ID, "invoice.delivery_failed", []byte(`{"fixture":"synthetic","replay":true}`), "")
	if err != nil {
		return err
	}
	if err = finish(dead, "non_retryable", 400, true); err != nil {
		return err
	}
	fixtures["dead"] = dead.ID.String()
	attack, err := s.InsertEvent(ctx, tenant, billing.ID, `<img src=x onerror="window.__injected=1">`, []byte(`{"text":"<script>window.__injected=1</script>"}`), "")
	if err != nil {
		return err
	}
	if err = finish(attack, "success", 204, false); err != nil {
		return err
	}
	fixtures["hostile"] = attack.ID.String()
	// Keep injection evidence off the initial screenshot while retaining it for tests.
	sql, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer sql.Close()
	if _, err = sql.Exec(ctx, `UPDATE events SET received_at=received_at-interval '1 hour',last_error='<svg onload="window.__injected=1">' WHERE id=$1`, attack.ID); err != nil {
		return err
	}
	history, err := s.InsertEvent(ctx, tenant, billing.ID, "history.example", []byte(`{"fixture":"synthetic"}`), "")
	if err != nil {
		return err
	}
	fixtures["history"] = history.ID.String()
	for i := 0; i < 27; i++ {
		if err = finish(history, "success", 204, false); err != nil {
			return err
		}
		if i < 26 {
			if _, err = s.Replay(ctx, tenant, history.ID, int32(i)); err != nil {
				return err
			}
		}
	}
	if _, err = s.InsertEvent(ctx, tenant, disabled.ID, "sandbox.pending", []byte(`{"fixture":"synthetic"}`), ""); err != nil {
		return err
	}
	client := delivery.New(time.Second, true)
	defer client.Close()
	w := worker.Worker{Store: s, Stream: transport, Client: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Concurrency: 1, LeaseDuration: 2 * time.Second, SweepInterval: 50 * time.Millisecond}
	workerDone := make(chan error, 1)
	go func() { workerDone <- w.Run(ctx) }()
	defer func() {
		cancel()
		if workerErr := <-workerDone; workerErr != nil {
			fmt.Fprintln(os.Stderr, "browser fixture worker:", workerErr)
		}
	}()
	router := api.NewRouter(api.Deps{Store: s, Publisher: transport, Config: config.Config{AdminToken: "m4-browser-synthetic-token", MaxPayloadBytes: 256 << 10, SignatureTolerance: 5 * time.Minute}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__test/fixture", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixtures)
	})
	var shutdown sync.Once
	mux.HandleFunc("POST /__test/shutdown", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204); shutdown.Do(cancel) })
	mux.Handle("/", router)
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	fmt.Println("EventRelay synthetic browser fixture ready at http://127.0.0.1:58084/dashboard/")
	select {
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(stop)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
