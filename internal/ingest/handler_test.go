package ingest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/ingest"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
)

var (
	fixedTime = time.Unix(1763000000, 0)
	secret    = []byte("whsec_test")
	tenantID  = uuid.MustParse(store.DefaultTenantID)
)

type fakeEndpoints struct {
	endpoint store.Endpoint
	err      error
}

func (f *fakeEndpoints) EndpointByID(_ context.Context, _ uuid.UUID) (store.Endpoint, error) {
	return f.endpoint, f.err
}

type fakeEvents struct {
	inserted []store.Event
	payload  []byte
	eventTyp string
	err      error
}

func (f *fakeEvents) InsertEvent(_ context.Context, tenant, endpoint uuid.UUID, eventType string, payload []byte, _ string) (store.Event, error) {
	if f.err != nil {
		return store.Event{}, f.err
	}
	f.eventTyp = eventType
	f.payload = append([]byte(nil), payload...)
	e := store.Event{
		ID:         uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		TenantID:   tenant,
		EndpointID: endpoint,
		EventType:  eventType,
		Payload:    json.RawMessage(payload),
		Status:     store.StatusPending,
		ReceivedAt: fixedTime,
	}
	f.inserted = append(f.inserted, e)
	return e, nil
}

type harness struct {
	router    http.Handler
	endpoints *fakeEndpoints
	events    *fakeEvents
}

func newHarness(t *testing.T, mutate func(*store.Endpoint)) *harness {
	t.Helper()

	ep := store.Endpoint{
		ID:           uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		TenantID:     tenantID,
		URL:          "https://example.test/hook",
		IngestSecret: secret,
		Enabled:      true,
		MaxAttempts:  8,
	}
	if mutate != nil {
		mutate(&ep)
	}

	fe := &fakeEndpoints{endpoint: ep}
	fe2 := &fakeEvents{}

	h := &ingest.Handler{
		Endpoints:       fe,
		Events:          fe2,
		MaxPayloadBytes: 1024,
		Tolerance:       5 * time.Minute,
		Now:             func() time.Time { return fixedTime },
	}

	r := chi.NewRouter()
	r.Method(http.MethodPost, "/v1/endpoints/{id}/events", h)

	return &harness{router: r, endpoints: fe, events: fe2}
}

func (h *harness) do(t *testing.T, id uuid.UUID, body string, timestamp int64, signWith []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/endpoints/"+id.String()+"/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Type", "order.created")

	signingBody := []byte(body)
	if signWith != nil {
		req.Header.Set(hmacsign.HeaderName, hmacsign.Header(signWith, timestamp, signingBody))
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestIngestAcceptsValidSignedEvent(t *testing.T) {
	h := newHarness(t, nil)
	body := `{"order_id":"8891","total":4200}`

	rec := h.do(t, h.endpoints.endpoint.ID, body, fixedTime.Unix(), secret)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		EventID string `json:"event_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.EventID == "" || resp.Status != store.StatusPending {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(h.events.inserted) != 1 {
		t.Fatalf("inserted %d events, want 1", len(h.events.inserted))
	}
	if string(h.events.payload) != body {
		t.Fatalf("stored payload = %s, want %s", h.events.payload, body)
	}
	if h.events.eventTyp != "order.created" {
		t.Fatalf("event type = %q", h.events.eventTyp)
	}
}

func TestIngestRejectsTamperedBody(t *testing.T) {
	h := newHarness(t, nil)

	rec := h.do(t, h.endpoints.endpoint.ID, `{"order_id":"0000"}`, fixedTime.Unix(), secret)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first request should be valid, got %d", rec.Code)
	}

	// Same header, different body.
	req := httptest.NewRequest(http.MethodPost, "/v1/endpoints/"+h.endpoints.endpoint.ID.String()+"/events",
		strings.NewReader(`{"order_id":"8891"}`))
	req.Header.Set(hmacsign.HeaderName, hmacsign.Header(secret, fixedTime.Unix(), []byte(`{"order_id":"0000"}`)))

	rec = httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, "invalid-signature") {
		t.Fatalf("problem type missing: %s", got)
	}
	if len(h.events.inserted) != 1 {
		t.Fatalf("tampered event was stored")
	}
}

func TestIngestRejectsMissingSignature(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.do(t, h.endpoints.endpoint.ID, `{"a":1}`, fixedTime.Unix(), nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestIngestRejectsExpiredSignature(t *testing.T) {
	h := newHarness(t, nil)
	stale := fixedTime.Add(-10 * time.Minute)

	rec := h.do(t, h.endpoints.endpoint.ID, `{"a":1}`, stale.Unix(), secret)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expired-signature") {
		t.Fatalf("expected expired-signature problem, got %s", rec.Body.String())
	}
}

func TestIngestRejectsUnknownEndpoint(t *testing.T) {
	h := newHarness(t, func(e *store.Endpoint) { e.ID = uuid.New() })
	h.endpoints.err = store.ErrNotFound

	rec := h.do(t, h.endpoints.endpoint.ID, `{"a":1}`, fixedTime.Unix(), secret)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestIngestRejectsDisabledEndpoint(t *testing.T) {
	h := newHarness(t, func(e *store.Endpoint) { e.Enabled = false })

	rec := h.do(t, h.endpoints.endpoint.ID, `{"a":1}`, fixedTime.Unix(), secret)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestIngestRejectsOversizedPayload(t *testing.T) {
	h := newHarness(t, nil)
	body := `{"data":"` + strings.Repeat("a", 2000) + `"}`

	rec := h.do(t, h.endpoints.endpoint.ID, body, fixedTime.Unix(), secret)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestIngestRejectsInvalidJSON(t *testing.T) {
	h := newHarness(t, nil)
	body := `not-json`

	rec := h.do(t, h.endpoints.endpoint.ID, body, fixedTime.Unix(), secret)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if len(h.events.inserted) != 0 {
		t.Fatalf("invalid payload was stored")
	}
}

func TestIngestRejectsNonObjectPayload(t *testing.T) {
	h := newHarness(t, nil)

	rec := h.do(t, h.endpoints.endpoint.ID, `[1,2,3]`, fixedTime.Unix(), secret)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestIngestDefaultsEventType(t *testing.T) {
	h := newHarness(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/endpoints/"+h.endpoints.endpoint.ID.String()+"/events",
		strings.NewReader(`{"a":1}`))
	req.Header.Set(hmacsign.HeaderName, hmacsign.Header(secret, fixedTime.Unix(), []byte(`{"a":1}`)))

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if h.events.eventTyp != "event" {
		t.Fatalf("event type = %q, want default", h.events.eventTyp)
	}
}
