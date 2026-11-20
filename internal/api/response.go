package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/shellghostt/eventrelay/internal/httpx"
)

func writeProblem(w http.ResponseWriter, status int, slug, title, detail string) {
	httpx.Writer(w, status, slug, title, detail)
}

const maxInspectionResponseBytes = 1 << 20

// Marshal before writing headers, so an oversized response fails explicitly.
func inspectionJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		writeProblem(w, 500, "internal", "Internal error", "")
		return
	}
	if len(encoded)+1 > maxInspectionResponseBytes {
		writeProblem(w, 503, "inspection-response-too-large", "Inspection unavailable", "The response exceeds the 1 MiB inspection limit. Request a smaller page.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(encoded, '\n'))
}

func decodeMutation(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManagementBodyBytes))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "Provide one JSON object with the documented fields.")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "Provide exactly one JSON object.")
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "Provide one JSON object with non-null fields.")
		return false
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			writeProblem(w, 422, "invalid-body", "Invalid request body", "Provided fields must not be null.")
			return false
		}
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(value); err != nil {
		writeProblem(w, 422, "invalid-body", "Invalid request body", "Provide the documented fields and types.")
		return false
	}
	return true
}

// contextWithTimeout derives a context from the request, bounded by d.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
