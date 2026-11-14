// Package testutil provides isolated real PostgreSQL and Redis fixtures.
package testutil

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
	"github.com/shellghostt/eventrelay/internal/worker"
	"github.com/shellghostt/eventrelay/migrations"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type Harness struct {
	Store    *store.Store
	SQL      *pgxpool.Pool
	Stream   *stream.Stream
	DSN      string
	RedisURL string
}

func New(t testing.TB) *Harness {
	t.Helper()
	dsn, redisURL := os.Getenv("EVENTRELAY_TEST_DATABASE_URL"), os.Getenv("EVENTRELAY_TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("set EVENTRELAY_TEST_DATABASE_URL and EVENTRELAY_TEST_REDIS_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "er_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn = u.String()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	sql, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sql.Close)
	transport, err := stream.Open(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	transport.Key = schema
	if err = transport.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Client.Del(ctx, transport.Key).Err(); _ = transport.Close() })
	return &Harness{Store: s, SQL: sql, Stream: transport, DSN: dsn, RedisURL: redisURL}
}
func (h *Harness) Endpoint(t testing.TB, target string) store.Endpoint {
	t.Helper()
	ep, err := h.Store.CreateEndpoint(context.Background(), store.CreateEndpointInput{TenantID: uuid.MustParse(store.DefaultTenantID), URL: target, IngestSecret: []byte("ingest-secret"), DeliverySecret: []byte("delivery-secret"), MaxAttempts: 8})
	if err != nil {
		t.Fatal(err)
	}
	return ep
}
func (h *Harness) Event(t testing.TB, ep store.Endpoint) store.Event {
	t.Helper()
	e, err := h.Store.InsertEvent(context.Background(), ep.TenantID, ep.ID, "test", []byte(`{"hello":"world"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func (h *Harness) Start(t testing.TB, n int) {
	h.StartWithTiming(t, n, 2*time.Second, 50*time.Millisecond)
}

func (h *Harness) StartWithTiming(t testing.TB, n int, lease, sweep time.Duration) {
	t.Helper()
	c := delivery.New(time.Second, true)
	w := worker.Worker{Store: h.Store, Stream: h.Stream, Client: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Concurrency: n, LeaseDuration: lease, SweepInterval: sweep, RetryDelay: 50 * time.Millisecond}
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
		case <-time.After(10 * time.Second):
			t.Error("worker shutdown timed out")
		}
		c.Close()
	})
}
func (h *Harness) Wait(t testing.TB, id uuid.UUID, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var got string
		err := h.SQL.QueryRow(context.Background(), `SELECT status FROM events WHERE id=$1`, id.String()).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		if got == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event %s did not reach %s within %s", id, status, timeout)
}
