package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/httpx"
	"github.com/shellghostt/eventrelay/internal/store"
)

type eventResponse struct {
	ID            uuid.UUID       `json:"id"`
	EndpointID    uuid.UUID       `json:"endpoint_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	AttemptCount  int32           `json:"attempt_count"`
	ReplayCount   int32           `json:"replay_count"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	ReceivedAt    time.Time       `json:"received_at"`
	DeliveredAt   *time.Time      `json:"delivered_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
}

func toEventResponse(e store.Event) eventResponse {
	return eventResponse{
		ID:            e.ID,
		EndpointID:    e.EndpointID,
		EventType:     e.EventType,
		Payload:       e.Payload,
		Status:        e.Status,
		AttemptCount:  e.AttemptCount,
		ReplayCount:   e.ReplayCount,
		NextAttemptAt: e.NextAttemptAt,
		ReceivedAt:    e.ReceivedAt,
		DeliveredAt:   e.DeliveredAt,
		LastError:     e.LastError,
	}
}

var validStatuses = map[string]bool{
	store.StatusPending:      true,
	store.StatusInFlight:     true,
	store.StatusDelivered:    true,
	store.StatusDeadLettered: true,
	store.StatusCanceled:     true,
}

func (s *server) listEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := query.Get("view")
	if view != "" && view != "summary" {
		writeProblem(w, 422, "invalid-view", "Invalid view", "view must be summary when provided.")
		return
	}
	summary := view == "summary" || strings.HasPrefix(r.URL.Path, "/dashboard/api/")

	filter := store.EventFilter{
		Status: query.Get("status"),
		Limit:  50,
	}
	for _, name := range []string{"from", "to"} {
		if query.Has(name) {
			t, err := time.Parse(time.RFC3339, query.Get(name))
			if err != nil {
				writeProblem(w, 422, "invalid-time-filter", "Invalid time filter", "from/to must be RFC3339 instants.")
				return
			}
			t = t.UTC()
			if name == "from" {
				filter.From = &t
			} else {
				filter.To = &t
			}
		}
	}
	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		writeProblem(w, 422, "invalid-time-filter", "Invalid time range", "from must precede to; the window includes from and excludes to.")
		return
	}

	if filter.Status != "" && !validStatuses[filter.Status] {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-status", "Invalid status filter",
			"status must be one of pending, in_flight, delivered, dead_lettered, canceled.")
		return
	}

	if raw := query.Get("endpoint_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeProblem(w, http.StatusUnprocessableEntity, "invalid-endpoint-id", "Invalid endpoint_id",
				"endpoint_id must be a UUID.")
			return
		}
		filter.EndpointID = &id
	}

	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeProblem(w, http.StatusUnprocessableEntity, "invalid-limit", "Invalid limit",
				"limit must be an integer between 1 and 200.")
			return
		}
		filter.Limit = n
	}

	bound := summary || filter.From != nil || filter.To != nil
	// Bind parsed filters, so equivalent RFC3339 representations share a scope
	// and an arbitrarily long fractional-second spelling cannot inflate a cursor.
	endpointScope, fromScope, toScope := "", "", ""
	if filter.EndpointID != nil {
		endpointScope = filter.EndpointID.String()
	}
	if filter.From != nil {
		fromScope = filter.From.UTC().Format(time.RFC3339Nano)
	}
	if filter.To != nil {
		toScope = filter.To.UTC().Format(time.RFC3339Nano)
	}
	scope := cursorScope(r.URL.Path, map[string]string{"view": view, "status": filter.Status, "endpoint_id": endpointScope, "from": fromScope, "to": toScope})
	encodedCursor := query.Get("cursor")
	var err error
	if bound {
		encodedCursor, err = s.decodeBoundCursor(encodedCursor, scope)
	}
	if err != nil {
		writeProblem(w, 422, "invalid-cursor", "Invalid cursor", "The cursor must match this route and its filters.")
		return
	}
	cursor, err := decodeCursor(encodedCursor)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-cursor", "Invalid cursor",
			"The cursor is not a value returned by this API.")
		return
	}
	filter.Before = cursor

	ctx, cancel := contextWithTimeout(r, store.InspectionTimeout)
	defer cancel()
	var events []store.Event
	var next *store.Cursor
	if summary {
		events, next, err = s.store.ListEventSummaries(ctx, uuid.MustParse(store.DefaultTenantID), filter)
	} else {
		events, next, err = s.store.ListEvents(ctx, uuid.MustParse(store.DefaultTenantID), filter)
	}
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	var nextCursor *string
	if next != nil {
		encoded := encodeCursor(*next)
		if bound {
			encoded = s.encodeBoundCursor(scope, encoded)
		}
		nextCursor = &encoded
	}
	if summary {
		out := make([]eventSummaryResponse, 0, len(events))
		for _, e := range events {
			out = append(out, toEventSummary(e))
		}
		inspectionJSON(w, 200, map[string]any{"events": out, "next_cursor": nextCursor})
		return
	}

	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, toEventResponse(e))
	}

	response := map[string]any{"events": out}
	response["next_cursor"] = nextCursor
	if bound {
		inspectionJSON(w, 200, response)
	} else {
		httpx.JSON(w, http.StatusOK, response)
	}
}
