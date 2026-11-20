package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shellghostt/eventrelay/internal/store"
)

type eventSummaryResponse struct {
	ID                 uuid.UUID  `json:"id"`
	EndpointID         uuid.UUID  `json:"endpoint_id"`
	EventType          string     `json:"event_type"`
	Status             string     `json:"status"`
	AttemptCount       int32      `json:"attempt_count"`
	ReplayCount        int32      `json:"replay_count"`
	CycleAttemptCount  int16      `json:"cycle_attempt_count"`
	CycleMaxAttempts   int16      `json:"cycle_max_attempts"`
	CycleStartedAt     time.Time  `json:"cycle_started_at"`
	CycleFinishedAt    *time.Time `json:"cycle_finished_at"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	ReceivedAt         time.Time  `json:"received_at"`
	DeliveredAt        *time.Time `json:"delivered_at"`
	LastError          string     `json:"last_error,omitempty"`
	LastErrorTruncated bool       `json:"last_error_truncated"`
}

func toEventSummary(e store.Event) eventSummaryResponse {
	out := eventSummaryResponse{ID: e.ID, EndpointID: e.EndpointID, EventType: e.EventType, Status: e.Status,
		AttemptCount: e.AttemptCount, ReplayCount: e.ReplayCount, CycleAttemptCount: e.CycleAttemptCount, CycleMaxAttempts: e.CycleMaxAttempts,
		CycleStartedAt: e.CycleStartedAt, CycleFinishedAt: e.CycleFinishedAt, ReceivedAt: e.ReceivedAt, DeliveredAt: e.DeliveredAt,
		LastError: e.LastError, LastErrorTruncated: e.LastErrorTruncated}
	if e.Status == store.StatusPending {
		out.NextAttemptAt = &e.NextAttemptAt
	}
	return out
}

func (s *server) inspectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, 404, "not-found", "Not found", "The requested resource was not found.")
	case errors.Is(err, store.ErrConflict):
		writeProblem(w, 409, "state-conflict", "State conflict", "The resource state or expected version has changed. Refresh before submitting a new action.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeProblem(w, 503, "inspection-timeout", "Inspection unavailable", "The two-second inspection deadline elapsed. Retry the read later.")
	default:
		s.log.Error("inspection operation failed")
		writeProblem(w, 500, "internal", "Internal error", "")
	}
}

func inspectionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeProblem(w, 404, "not-found", "Not found", "The requested resource was not found.")
		return uuid.Nil, false
	}
	return id, true
}

func requestedLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	if !r.URL.Query().Has("limit") {
		return 50, true
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 200 {
		writeProblem(w, 422, "invalid-limit", "Invalid limit", "limit must be an integer between 1 and 200.")
		return 0, false
	}
	return n, true
}

func (s *server) eventDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := inspectionID(w, r)
	if !ok {
		return
	}
	e, err := s.store.EventDetailByID(r.Context(), uuid.MustParse(store.DefaultTenantID), id)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	out := struct {
		eventSummaryResponse
		Payload                  any     `json:"payload"`
		PayloadAvailable         bool    `json:"payload_available"`
		PayloadBytes             int64   `json:"payload_bytes"`
		PayloadUnavailableReason *string `json:"payload_unavailable_reason"`
		TerminalReason           *string `json:"terminal_reason"`
	}{eventSummaryResponse: toEventSummary(e.Event), PayloadAvailable: e.PayloadAvailable, PayloadBytes: e.PayloadBytes, TerminalReason: e.TerminalReason}
	out.LastErrorTruncated = e.LastErrorTruncated
	if e.PayloadAvailable {
		out.Payload = e.Payload
	} else {
		reason := "payload_limit_exceeded"
		out.PayloadUnavailableReason = &reason
	}
	inspectionJSON(w, 200, out)
}

func (s *server) numberedPage(w http.ResponseWriter, r *http.Request, initial int32) (uuid.UUID, int, int32, string, bool) {
	id, ok := inspectionID(w, r)
	if !ok {
		return uuid.Nil, 0, 0, "", false
	}
	limit, ok := requestedLimit(w, r)
	if !ok {
		return uuid.Nil, 0, 0, "", false
	}
	scope := cursorScope(r.URL.Path, nil)
	position, err := s.decodeBoundCursor(r.URL.Query().Get("cursor"), scope)
	after := initial
	if err == nil && position != "" {
		n, parseErr := strconv.ParseInt(position, 10, 32)
		err = parseErr
		after = int32(n)
		if after < 0 {
			err = errInvalidCursor
		}
	}
	if err != nil {
		writeProblem(w, 422, "invalid-cursor", "Invalid cursor", "The cursor must match this route.")
		return uuid.Nil, 0, 0, "", false
	}
	return id, limit, after, scope, true
}

func (s *server) attempts(w http.ResponseWriter, r *http.Request) {
	id, limit, after, scope, ok := s.numberedPage(w, r, 0)
	if !ok {
		return
	}
	items, next, err := s.store.ListAttempts(r.Context(), uuid.MustParse(store.DefaultTenantID), id, after, limit)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 200, map[string]any{"attempts": items, "next_cursor": s.numberCursor(scope, next)})
}

func (s *server) cycles(w http.ResponseWriter, r *http.Request) {
	id, limit, after, scope, ok := s.numberedPage(w, r, -1)
	if !ok {
		return
	}
	items, next, err := s.store.ListCycles(r.Context(), uuid.MustParse(store.DefaultTenantID), id, after, limit)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 200, map[string]any{"cycles": items, "next_cursor": s.numberCursor(scope, next)})
}

func (s *server) numberCursor(scope string, next *int32) *string {
	if next == nil {
		return nil
	}
	encoded := s.encodeBoundCursor(scope, strconv.FormatInt(int64(*next), 10))
	return &encoded
}

func (s *server) endpointDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := inspectionID(w, r)
	if !ok {
		return
	}
	e, err := s.store.EndpointDetail(r.Context(), uuid.MustParse(store.DefaultTenantID), id)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 200, toEndpointResponse(e, false))
}

func (s *server) endpointStats(w http.ResponseWriter, r *http.Request) {
	id, ok := inspectionID(w, r)
	if !ok {
		return
	}
	stats, err := s.store.EndpointStats(r.Context(), uuid.MustParse(store.DefaultTenantID), id)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 200, stats)
}

func (s *server) endpointPage(w http.ResponseWriter, r *http.Request) {
	limit, ok := requestedLimit(w, r)
	if !ok {
		return
	}
	scope := cursorScope(r.URL.Path, nil)
	encoded, err := s.decodeBoundCursor(r.URL.Query().Get("cursor"), scope)
	var before *store.Cursor
	if err == nil {
		before, err = decodeCursor(encoded)
	}
	if err != nil {
		writeProblem(w, 422, "invalid-cursor", "Invalid cursor", "The cursor must match this route.")
		return
	}
	items, next, err := s.store.ListEndpointPage(r.Context(), uuid.MustParse(store.DefaultTenantID), limit, before)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	out := make([]endpointResponse, 0, len(items))
	for _, e := range items {
		out = append(out, toEndpointResponse(e, false))
	}
	var nextCursor *string
	if next != nil {
		c := s.encodeBoundCursor(scope, encodeCursor(*next))
		nextCursor = &c
	}
	inspectionJSON(w, 200, map[string]any{"endpoints": out, "next_cursor": nextCursor})
}

func (s *server) patchEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := inspectionID(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision *int32  `json:"expected_revision"`
		URL              *string `json:"url"`
		Enabled          *bool   `json:"enabled"`
	}
	if !decodeMutation(w, r, &req) {
		return
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision < 1 || (req.URL == nil && req.Enabled == nil) {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "expected_revision and at least one of url/enabled are required.")
		return
	}
	if req.URL != nil {
		trimmed := strings.TrimSpace(*req.URL)
		req.URL = &trimmed
		if err := validateDestinationURL(trimmed); err != nil {
			writeProblem(w, 422, "invalid-url", "Invalid destination URL", err.Error())
			return
		}
	}
	e, err := s.store.UpdateEndpoint(r.Context(), uuid.MustParse(store.DefaultTenantID), id, store.EndpointPatch{ExpectedRevision: *req.ExpectedRevision, URL: req.URL, Enabled: req.Enabled})
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 200, toEndpointResponse(e, false))
}

func (s *server) replay(w http.ResponseWriter, r *http.Request) {
	id, ok := inspectionID(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedReplayCount *int32 `json:"expected_replay_count"`
	}
	if !decodeMutation(w, r, &req) {
		return
	}
	if req.ExpectedReplayCount == nil || *req.ExpectedReplayCount < 0 {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "expected_replay_count must be a nonnegative integer.")
		return
	}
	e, err := s.store.Replay(r.Context(), uuid.MustParse(store.DefaultTenantID), id, *req.ExpectedReplayCount)
	if err != nil {
		s.inspectionError(w, err)
		return
	}
	inspectionJSON(w, 202, toEventSummary(e))
}
