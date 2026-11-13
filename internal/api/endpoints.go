package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/shellghostt/eventrelay/internal/httpx"
	"github.com/shellghostt/eventrelay/internal/store"
)

const maxManagementBodyBytes = 64 << 10 // 64 KiB

type createEndpointRequest struct {
	URL         string `json:"url"`
	Description string `json:"description"`
	MaxAttempts *int16 `json:"max_attempts"`
}

type endpointResponse struct {
	ID             uuid.UUID `json:"id"`
	URL            string    `json:"url"`
	Description    string    `json:"description"`
	Enabled        bool      `json:"enabled"`
	MaxAttempts    int16     `json:"max_attempts"`
	CreatedAt      time.Time `json:"created_at"`
	IngestSecret   string    `json:"ingest_secret,omitempty"`
	DeliverySecret string    `json:"delivery_secret,omitempty"`
}

func toEndpointResponse(e store.Endpoint, includeSecrets bool) endpointResponse {
	out := endpointResponse{
		ID:          e.ID,
		URL:         e.URL,
		Description: e.Description,
		Enabled:     e.Enabled,
		MaxAttempts: e.MaxAttempts,
		CreatedAt:   e.CreatedAt,
	}
	if includeSecrets {
		out.IngestSecret = base64.StdEncoding.EncodeToString(e.IngestSecret)
		out.DeliverySecret = base64.StdEncoding.EncodeToString(e.DeliverySecret)
	}
	return out
}

func (s *server) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var req createEndpointRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManagementBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-body", "Invalid request body",
			"The body must be a JSON object with a url field: "+err.Error())
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if err := validateDestinationURL(req.URL); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-url", "Invalid destination URL", err.Error())
		return
	}

	maxAttempts := int16(8)
	if req.MaxAttempts != nil {
		maxAttempts = *req.MaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > 20 {
		writeProblem(w, http.StatusUnprocessableEntity, "invalid-max-attempts", "Invalid max_attempts",
			"max_attempts must be between 1 and 20.")
		return
	}

	ingestSecret, err := randomSecret()
	if err != nil {
		s.log.Error("generate ingest secret", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}
	deliverySecret, err := randomSecret()
	if err != nil {
		s.log.Error("generate delivery secret", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	endpoint, err := s.store.CreateEndpoint(r.Context(), store.CreateEndpointInput{
		TenantID:       uuid.MustParse(store.DefaultTenantID),
		URL:            req.URL,
		Description:    strings.TrimSpace(req.Description),
		IngestSecret:   ingestSecret,
		DeliverySecret: deliverySecret,
		MaxAttempts:    maxAttempts,
	})
	if err != nil {
		s.log.Error("create endpoint", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	httpx.JSON(w, http.StatusCreated, toEndpointResponse(endpoint, true))
}

func (s *server) listEndpoints(w http.ResponseWriter, r *http.Request) {
	endpoints, err := s.store.ListEndpoints(r.Context(), uuid.MustParse(store.DefaultTenantID))
	if err != nil {
		s.log.Error("list endpoints", "error", err)
		writeProblem(w, http.StatusInternalServerError, "internal", "Internal error", "")
		return
	}

	out := make([]endpointResponse, 0, len(endpoints))
	for _, e := range endpoints {
		out = append(out, toEndpointResponse(e, false))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"endpoints": out})
}

// validateDestinationURL rejects anything that is not a plain http(s) URL.
// SSRF protection for private ranges arrives with the delivery worker in M2.
func validateDestinationURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > 2048 {
		return errors.New("url must be at most 2048 characters")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("url cannot be parsed")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New("url must include a host")
	}
	if parsed.User != nil {
		return errors.New("url must not contain credentials")
	}
	return nil
}

func randomSecret() ([]byte, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
