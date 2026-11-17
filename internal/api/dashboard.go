package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shellghostt/eventrelay/internal/config"
	"github.com/shellghostt/eventrelay/internal/dashboard"
)

const dashboardCookie = "eventrelay_dashboard"
const dashboardLifetime = 30 * time.Minute
const dashboardSessionLimit = 256

type browserSession struct {
	ID      string
	Nonce   string
	Expires time.Time
	Secure  bool
}

type dashboardSessions struct {
	mu    sync.Mutex
	items map[string]browserSession
	now   func() time.Time
}

type sessionContextKey struct{}

func newDashboardSessions() *dashboardSessions {
	return &dashboardSessions{items: make(map[string]browserSession), now: time.Now}
}

func randomSessionValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func dashboardHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *server) mountDashboard(r chi.Router) {
	s.sessions = newDashboardSessions()
	assets, _ := fs.Sub(dashboard.Assets, "dist")
	static := http.StripPrefix("/dashboard/", http.FileServer(http.FS(assets)))
	r.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusPermanentRedirect)
	})
	r.Route("/dashboard", func(r chi.Router) {
		r.Use(dashboardHeaders)
		r.Get("/", static.ServeHTTP)
		r.Get("/*", static.ServeHTTP)
		r.Route("/api", func(r chi.Router) {
			r.Post("/login", s.dashboardLogin)
			r.Group(func(r chi.Router) {
				r.Use(s.requireDashboardSession)
				r.Get("/session", s.dashboardSession)
				r.Post("/logout", s.dashboardLogout)
				r.Get("/endpoints", s.listEndpoints)
				r.Get("/endpoints/{id}", s.endpointDetail)
				r.Get("/endpoints/{id}/stats", s.endpointStats)
				r.Patch("/endpoints/{id}", s.patchEndpoint)
				r.Get("/events", s.listEvents)
				r.Get("/events/{id}", s.eventDetail)
				r.Get("/events/{id}/attempts", s.attempts)
				r.Get("/events/{id}/cycles", s.cycles)
				r.Post("/events/{id}/replay", s.replay)
			})
		})
	})
}

// Forwarded headers do not select the browser origin. A TLS proxy must pin it.
func (s *server) dashboardOrigin(r *http.Request) (string, bool) {
	if s.cfg.DashboardOrigin != "" {
		origin, err := config.ValidateDashboardOrigin(s.cfg.DashboardOrigin)
		return origin, err == nil
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	origin, err := config.ValidateDashboardOrigin(scheme + "://" + r.Host)
	if err != nil {
		return "", false
	}
	u, _ := url.Parse(origin)
	if scheme == "http" {
		host := u.Hostname()
		ip, err := netip.ParseAddr(host)
		if host != "localhost" && (err != nil || !ip.IsLoopback()) {
			return "", false
		}
	}
	return origin, true
}

func (s *server) checkDashboardOrigin(w http.ResponseWriter, r *http.Request) bool {
	expected, ok := s.dashboardOrigin(r)
	if !ok || r.Header.Get("Origin") != expected {
		writeProblem(w, 403, "invalid-origin", "Browser origin rejected", "Use the configured same-origin dashboard.")
		return false
	}
	return true
}

func setDashboardCookie(w http.ResponseWriter, session browserSession) {
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Value: session.ID, Path: "/dashboard/", Expires: session.Expires,
		MaxAge: int(dashboardLifetime.Seconds()), HttpOnly: true, Secure: session.Secure, SameSite: http.SameSiteStrictMode})
}

func clearDashboardCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: dashboardCookie, Path: "/dashboard/", Value: "", Expires: time.Unix(1, 0), MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (s *server) dashboardLogin(w http.ResponseWriter, r *http.Request) {
	if !s.checkDashboardOrigin(w, r) {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeMutation(w, r, &req) {
		return
	}
	if s.cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.cfg.AdminToken)) != 1 {
		writeProblem(w, 401, "unauthorized", "Unauthorized", "The management token was not accepted.")
		return
	}
	id, err := randomSessionValue()
	if err != nil {
		writeProblem(w, 500, "internal", "Internal error", "")
		return
	}
	nonce, err := randomSessionValue()
	if err != nil {
		writeProblem(w, 500, "internal", "Internal error", "")
		return
	}
	origin, _ := s.dashboardOrigin(r)
	session := browserSession{ID: id, Nonce: nonce, Expires: s.sessions.now().Add(dashboardLifetime), Secure: strings.HasPrefix(origin, "https://")}
	s.sessions.mu.Lock()
	for key, item := range s.sessions.items {
		if !s.sessions.now().Before(item.Expires) {
			delete(s.sessions.items, key)
		}
	}
	if old, err := r.Cookie(dashboardCookie); err == nil {
		delete(s.sessions.items, old.Value)
	}
	if len(s.sessions.items) >= dashboardSessionLimit {
		s.sessions.mu.Unlock()
		writeProblem(w, 503, "dashboard-session-limit", "Login unavailable", "The dashboard session limit has been reached. Log out an existing session or wait for expiry.")
		return
	}
	s.sessions.items[id] = session
	s.sessions.mu.Unlock()
	setDashboardCookie(w, session)
	inspectionJSON(w, 200, map[string]any{"csrf_nonce": session.Nonce, "expires_at": session.Expires})
}

func (s *server) requireDashboardSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(dashboardCookie)
		if err != nil {
			writeProblem(w, 401, "unauthorized", "Session required", "Sign in to the dashboard.")
			return
		}
		s.sessions.mu.Lock()
		session, found := s.sessions.items[cookie.Value]
		if found && !s.sessions.now().Before(session.Expires) {
			delete(s.sessions.items, cookie.Value)
			found = false
		}
		s.sessions.mu.Unlock()
		if !found {
			writeProblem(w, 401, "unauthorized", "Session expired", "Sign in to the dashboard again.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.checkDashboardOrigin(w, r) {
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-EventRelay-CSRF")), []byte(session.Nonce)) != 1 {
				writeProblem(w, 403, "invalid-csrf", "Action rejected", "A valid session CSRF nonce is required.")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func (s *server) dashboardSession(w http.ResponseWriter, r *http.Request) {
	item := r.Context().Value(sessionContextKey{}).(browserSession)
	inspectionJSON(w, 200, map[string]any{"csrf_nonce": item.Nonce, "expires_at": item.Expires})
}

func (s *server) dashboardLogout(w http.ResponseWriter, r *http.Request) {
	item := r.Context().Value(sessionContextKey{}).(browserSession)
	s.sessions.mu.Lock()
	delete(s.sessions.items, item.ID)
	s.sessions.mu.Unlock()
	clearDashboardCookie(w, item.Secure)
	w.WriteHeader(http.StatusNoContent)
}
