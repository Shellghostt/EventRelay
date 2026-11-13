package api

import (
	"encoding/json"
	"net/http"
	"strconv"
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
	AttemptCount  int16           `json:"attempt_count"`
	ReplayCount   int16           `json:"replay_count"`
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

	filter := store.EventFilter{
		Status: query.Get("status"),
		Limit:  50,
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

	cursor, err := decodeCursor(query.Get("cursor"))
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-cursor", "Invalid cursor",
			"The cursor is not a value returned by this API.")
		return
	}
	filter.Before = cursor

	events, next, err := s.store.ListEvents(r.Context(), uuid.MustParse(store.DefaultTenantID), filter)
	if err != nil {
		s.log.Error("list events", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, toEventResponse(e))
	}

	response := map[string]any{"events": out}
	if next != nil {
		response["next_cursor"] = encodeCursor(*next)
	} else {
		response["next_cursor"] = nil
	}
	httpx.JSON(w, http.StatusOK, response)
}
