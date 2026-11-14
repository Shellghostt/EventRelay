// Package ingest implements the signed event ingestion endpoint.
//
// The handler is deliberately independent of the router and the database so
// that it can be tested with a fake store: signature verification is the part
// that must never regress, and it is easiest to prove with a unit test.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/httpx"
	"github.com/shellghostt/eventrelay/internal/observability"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
)

// EndpointLookup resolves an endpoint, including its secrets.
type EndpointLookup interface {
	EndpointByID(ctx context.Context, id uuid.UUID) (store.Endpoint, error)
}

// EventWriter persists a pending event.
type EventWriter interface {
	InsertEvent(ctx context.Context, tenantID, endpointID uuid.UUID, eventType string, payload []byte, idempotencyKey string) (store.Event, error)
}

type Publisher interface {
	Publish(context.Context, store.Dispatch) error
}

// Handler serves POST /v1/endpoints/{id}/events.
type Handler struct {
	Endpoints       EndpointLookup
	Events          EventWriter
	MaxPayloadBytes int64
	Tolerance       time.Duration
	Now             func() time.Time
	Publisher       Publisher
}

const maxEventTypeLength = 200

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		observability.IngestDuration.Observe(time.Since(start).Seconds())
	}()

	rawID := chi.URLParam(r, "id")
	endpointID, err := uuid.Parse(rawID)
	if err != nil {
		h.reject(w, http.StatusNotFound, "endpoint-not-found", "Endpoint not found",
			"No endpoint exists with that id.")
		return
	}

	maxPayload := h.MaxPayloadBytes
	if maxPayload <= 0 {
		maxPayload = 256 << 10
	}

	// Read the raw body first: the signature covers the exact bytes, so the
	// body must not be decoded or re-encoded before verification.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayload+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.reject(w, http.StatusRequestEntityTooLarge, "payload-too-large", "Payload too large",
				"The event payload exceeds the configured limit.")
			return
		}
		h.reject(w, http.StatusBadRequest, "invalid-body", "Invalid body",
			"The request body could not be read.")
		return
	}
	if int64(len(body)) > maxPayload {
		h.reject(w, http.StatusRequestEntityTooLarge, "payload-too-large", "Payload too large",
			"The event payload exceeds the configured limit.")
		return
	}

	endpoint, err := h.Endpoints.EndpointByID(r.Context(), endpointID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.reject(w, http.StatusNotFound, "endpoint-not-found", "Endpoint not found",
				"No endpoint exists with that id.")
			return
		}
		h.reject(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	if !endpoint.Enabled {
		h.reject(w, http.StatusConflict, "endpoint-disabled", "Endpoint disabled",
			"This endpoint is disabled and does not accept events.")
		return
	}

	now := time.Now
	if h.Now != nil {
		now = h.Now
	}

	signatureHeader := r.Header.Get(hmacsign.HeaderName)
	if signatureHeader == "" {
		h.reject(w, http.StatusUnauthorized, "missing-signature", "Missing signature",
			"The "+hmacsign.HeaderName+" header is required.")
		return
	}

	if err := hmacsign.Verify(endpoint.IngestSecret, signatureHeader, body, now(), h.Tolerance); err != nil {
		switch {
		case errors.Is(err, hmacsign.ErrExpired):
			h.reject(w, http.StatusUnauthorized, "expired-signature", "Expired signature",
				"The signature timestamp is outside the accepted tolerance.")
		case errors.Is(err, hmacsign.ErrMalformed):
			h.reject(w, http.StatusUnauthorized, "invalid-signature", "Invalid signature",
				"The signature header is malformed.")
		default:
			h.reject(w, http.StatusUnauthorized, "invalid-signature", "Invalid signature",
				"The signature does not match the request body.")
		}
		return
	}

	// Only after the signature is valid do we parse the payload.
	var probe map[string]json.RawMessage
	if len(body) == 0 {
		h.reject(w, http.StatusUnprocessableEntity, "invalid-payload", "Invalid payload",
			"The payload must be a JSON object.")
		return
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe == nil {
		h.reject(w, http.StatusUnprocessableEntity, "invalid-payload", "Invalid payload",
			"The payload must be a JSON object.")
		return
	}

	eventType := strings.TrimSpace(r.Header.Get("X-Event-Type"))
	if eventType == "" {
		eventType = "event"
	}
	if len(eventType) > maxEventTypeLength {
		h.reject(w, http.StatusUnprocessableEntity, "invalid-event-type", "Invalid event type",
			"X-Event-Type must be at most 200 characters.")
		return
	}

	// Idempotency-Key is accepted from M5 onwards. Passing an empty value keeps
	// the unique index unused rather than turning a duplicate into a 500.
	event, err := h.Events.InsertEvent(
		r.Context(), endpoint.TenantID, endpoint.ID, eventType, body, "",
	)
	if err != nil {
		h.reject(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	observability.IngestTotal.WithLabelValues("accepted").Inc()
	if h.Publisher != nil {
		publishCtx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		if err := h.Publisher.Publish(publishCtx, store.Dispatch{TenantID: event.TenantID, EventID: event.ID}); err != nil {
			observability.PublishFailures.Inc()
		}
		cancel()
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": event.ID.String(),
		"status":   event.Status,
	})
}

func (h *Handler) reject(w http.ResponseWriter, status int, slug, title, detail string) {
	observability.IngestTotal.WithLabelValues("rejected").Inc()
	httpx.Writer(w, status, slug, title, detail)
}
