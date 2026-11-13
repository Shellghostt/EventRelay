// Package api wires the HTTP surface of EventRelay.
package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/ingest"
	"github.com/shellghostt/eventrelay/internal/observability"
	"github.com/shellghostt/eventrelay/internal/store"
)

// Deps is everything the router needs.
type Deps struct {
	Store  *store.Store
	Config config.Config
	Log    *slog.Logger
}

type server struct {
	store *store.Store
	cfg   config.Config
	log   *slog.Logger
}

// NewRouter builds the complete handler tree.
//
// Route groups:
//   - /healthz, /readyz, /metrics: unauthenticated operational endpoints.
//   - /v1/endpoints (management): bearer token.
//   - /v1/endpoints/{id}/events (ingest): HMAC signature, no bearer token.
func NewRouter(d Deps) http.Handler {
	s := &server{store: d.Store, cfg: d.Config, log: d.Log}

	r := chi.NewRouter()
	// The peer address is logged as it arrives. Forwarded headers such as
	// X-Forwarded-For are chosen by the client, so they are never used to
	// attribute a request to an IP address; trusting them would let a caller
	// forge its own address in the logs.
	r.Use(middleware.RequestID)
	r.Use(s.observe)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", s.ready)
	r.Method(http.MethodGet, "/metrics", observability.MetricsHandler())

	ingestHandler := &ingest.Handler{
		Endpoints:       d.Store,
		Events:          d.Store,
		MaxPayloadBytes: d.Config.MaxPayloadBytes,
		Tolerance:       d.Config.SignatureTolerance,
		Now:             time.Now,
	}
	r.Method(http.MethodPost, "/v1/endpoints/{id}/events", ingestHandler)

	management := chi.NewRouter()
	management.Use(requireAdminToken(d.Config.AdminToken))
	management.Post("/v1/endpoints", s.createEndpoint)
	management.Get("/v1/endpoints", s.listEndpoints)
	management.Get("/v1/events", s.listEvents)

	r.Group(func(r chi.Router) {
		r.Use(requireAdminToken(d.Config.AdminToken))
		r.Post("/v1/endpoints", s.createEndpoint)
		r.Get("/v1/endpoints", s.listEndpoints)
		r.Get("/v1/events", s.listEvents)
	})

	return r
}

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("readiness check failed", "error", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready\n"))
}

// requireAdminToken protects management routes. M5 replaces this with hashed
// API keys; until then a single static token is the whole authorisation model.
func requireAdminToken(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			raw, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(raw)), expected) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeProblem(w, http.StatusUnauthorized, "unauthorized", "Unauthorized",
					"A valid bearer token is required.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)

		observability.HTTPRequestsTotal.WithLabelValues(route, r.Method, statusClass(rec.status)).Inc()
		observability.HTTPRequestDuration.WithLabelValues(route).Observe(elapsed.Seconds())

		s.log.Info("http request",
			"method", r.Method,
			"route", route,
			"status", rec.status,
			"duration_ms", elapsed.Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}
