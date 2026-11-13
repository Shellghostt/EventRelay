package store_test

// Integration tests. They require a real PostgreSQL instance and skip when
// EVENTRELAY_TEST_DATABASE_URL is unset:
//
//	docker compose -f deploy/docker-compose.yml up -d postgres
//	EVENTRELAY_TEST_DATABASE_URL='postgres://eventrelay:eventrelay@localhost:55432/eventrelay?sslmode=disable' go test ./internal/store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/migrations"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()

	dsn := os.Getenv("EVENTRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("EVENTRELAY_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestEndpointAndEventRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tenantID := uuid.MustParse(store.DefaultTenantID)

	endpoint, err := s.CreateEndpoint(ctx, store.CreateEndpointInput{
		TenantID:       tenantID,
		URL:            "https://example.test/hook",
		Description:    "integration test",
		IngestSecret:   []byte("0123456789abcdef0123456789abcdef"),
		DeliverySecret: []byte("fedcba9876543210fedcba9876543210"),
		MaxAttempts:    8,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if endpoint.ID == uuid.Nil {
		t.Fatal("endpoint id was not assigned")
	}

	payload := json.RawMessage(`{"order_id":"8891","nested":{"a":1}}`)
	event, err := s.InsertEvent(ctx, tenantID, endpoint.ID, "order.created", payload, "")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if event.Status != store.StatusPending {
		t.Fatalf("status = %q, want pending", event.Status)
	}
	if event.ID == uuid.Nil {
		t.Fatal("event id was not assigned")
	}

	loaded, err := s.EndpointByID(ctx, endpoint.ID)
	if err != nil {
		t.Fatalf("load endpoint: %v", err)
	}
	if string(loaded.IngestSecret) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("ingest secret did not round trip")
	}

	events, next, err := s.ListEvents(ctx, tenantID, store.EventFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if next != nil {
		t.Fatal("did not expect a next cursor for a single page")
	}

	var found bool
	for _, e := range events {
		if e.ID == event.ID {
			found = true
			var decoded map[string]any
			if err := json.Unmarshal(e.Payload, &decoded); err != nil {
				t.Fatalf("payload is not valid JSON: %v", err)
			}
			if decoded["order_id"] != "8891" {
				t.Fatalf("payload content changed: %s", e.Payload)
			}
		}
	}
	if !found {
		t.Fatal("inserted event was not returned by the list")
	}
}

func TestEndpointByIDReturnsNotFound(t *testing.T) {
	s := testStore(t)

	if _, err := s.EndpointByID(context.Background(), uuid.New()); err != store.ErrNotFound {
		t.Fatalf("error = %v, want store.ErrNotFound", err)
	}
}

func TestListEventsCursorPagination(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tenantID := uuid.MustParse(store.DefaultTenantID)

	endpoint, err := s.CreateEndpoint(ctx, store.CreateEndpointInput{
		TenantID:       tenantID,
		URL:            "https://example.test/pagination",
		IngestSecret:   []byte("0123456789abcdef0123456789abcdef"),
		DeliverySecret: []byte("fedcba9876543210fedcba9876543210"),
		MaxAttempts:    8,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := s.InsertEvent(ctx, tenantID, endpoint.ID, "pagination.test", json.RawMessage(`{"i":1}`), ""); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}

	seen := map[uuid.UUID]bool{}
	before := (*store.Cursor)(nil)
	for page := 0; page < 10; page++ {
		events, next, err := s.ListEvents(ctx, tenantID, store.EventFilter{Limit: 2, Before: before})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, e := range events {
			if seen[e.ID] {
				t.Fatalf("event %s returned twice", e.ID)
			}
			seen[e.ID] = true
		}
		if next == nil {
			break
		}
		before = next
	}

	if len(seen) < 5 {
		t.Fatalf("saw %d distinct events, want at least 5", len(seen))
	}
}
