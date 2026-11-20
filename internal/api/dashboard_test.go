package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shellghostt/eventrelay/internal/config"
)

const browserTestToken = "dashboard-synthetic-token"

func dashboardTestServer(origin string) (*server, http.Handler) {
	s := &server{cfg: config.Config{AdminToken: browserTestToken, DashboardOrigin: origin}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := chi.NewRouter()
	s.mountDashboard(r)
	return s, r
}

func browserRequest(t *testing.T, handler http.Handler, method, path, origin, body string, cookie *http.Cookie, csrf string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-EventRelay-CSRF", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func browserLogin(t *testing.T, handler http.Handler, origin string) (*http.Cookie, string) {
	t.Helper()
	w := browserRequest(t, handler, "POST", "/dashboard/api/login", origin, `{"token":"`+browserTestToken+`"}`, nil, "", 200)
	var out struct {
		Nonce   string    `json:"csrf_nonce"`
		Expires time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Nonce) < 40 || out.Expires.IsZero() {
		t.Fatal("missing random nonce or absolute expiry")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0], out.Nonce
}

func TestDashboardSessionOriginCSRFRevocationAndRestart(t *testing.T) {
	_, r := dashboardTestServer("")
	origin := "http://127.0.0.1:8080"
	browserRequest(t, r, "POST", "/dashboard/api/login", "", `{"token":"`+browserTestToken+`"}`, nil, "", 403)
	browserRequest(t, r, "POST", "/dashboard/api/login", "http://evil.example", `{"token":"`+browserTestToken+`"}`, nil, "", 403)
	browserRequest(t, r, "POST", "/dashboard/api/login", origin, `{"token":"wrong"}`, nil, "", 401)
	browserRequest(t, r, "POST", "/dashboard/api/login", origin, `{"token":"`+browserTestToken+`"} {}`, nil, "", 422)
	cookie, nonce := browserLogin(t, r, origin)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/dashboard/" || cookie.Secure || cookie.MaxAge != 1800 || strings.Contains(cookie.Value, browserTestToken) || cookie.Value == nonce {
		t.Fatalf("unsafe cookie %+v", cookie)
	}
	w := browserRequest(t, r, "GET", "/dashboard/api/session", "", "", cookie, "", 200)
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing browser security headers")
	}
	browserRequest(t, r, "POST", "/dashboard/api/logout", "http://evil.example", "", cookie, nonce, 403)
	browserRequest(t, r, "POST", "/dashboard/api/logout", origin, "", cookie, "", 403)
	browserRequest(t, r, "POST", "/dashboard/api/logout", origin, "", cookie, "wrong", 403)
	browserRequest(t, r, "GET", "/dashboard/api/session", "", "", cookie, "", 200)
	logout := browserRequest(t, r, "POST", "/dashboard/api/logout", origin, "", cookie, nonce, 204)
	if logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear browser cookie")
	}
	browserRequest(t, r, "GET", "/dashboard/api/session", "", "", cookie, "", 401)
	cookie, _ = browserLogin(t, r, origin)
	_, restarted := dashboardTestServer("")
	browserRequest(t, restarted, "GET", "/dashboard/api/session", "", "", cookie, "", 401)
	full := NewRouter(Deps{Config: config.Config{AdminToken: browserTestToken}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	browserRequest(t, full, "GET", "/v1/events", origin, "", cookie, "", 401)
	browserRequest(t, full, "POST", "/v1/endpoints", origin, `{"url":"https://example.test"}`, cookie, "", 401)
}

func TestDashboardAbsoluteExpiryAndBoundedSessions(t *testing.T) {
	s, r := dashboardTestServer("")
	origin := "http://127.0.0.1:8080"
	now := time.Now()
	s.sessions.now = func() time.Time { return now }
	cookie, _ := browserLogin(t, r, origin)
	now = now.Add(29 * time.Minute)
	browserRequest(t, r, "GET", "/dashboard/api/session", "", "", cookie, "", 200)
	now = now.Add(time.Minute)
	browserRequest(t, r, "GET", "/dashboard/api/session", "", "", cookie, "", 401)
	for i := 0; i < dashboardSessionLimit; i++ {
		browserLogin(t, r, origin)
	}
	browserRequest(t, r, "POST", "/dashboard/api/login", origin, `{"token":"`+browserTestToken+`"}`, nil, "", 503)
	if len(s.sessions.items) != dashboardSessionLimit {
		t.Fatal("session map exceeded its bound")
	}
	now = now.Add(dashboardLifetime)
	browserLogin(t, r, origin)
	if len(s.sessions.items) != 1 {
		t.Fatal("expired sessions were not pruned")
	}
}

func TestDashboardTLSProxyAndUnconfiguredRemoteHTTP(t *testing.T) {
	_, pinned := dashboardTestServer("https://console.example.test")
	cookie, _ := browserLogin(t, pinned, "https://console.example.test")
	if !cookie.Secure {
		t.Fatal("TLS proxy session cookie is not Secure")
	}
	_, r := dashboardTestServer("")
	req := httptest.NewRequest("POST", "http://remote.example/dashboard/api/login", strings.NewReader(`{"token":"`+browserTestToken+`"}`))
	req.Header.Set("Origin", "http://remote.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("remote HTTP or untrusted forwarded protocol enabled a browser session")
	}
	page := browserRequest(t, r, "GET", "/dashboard/", "", "", nil, "", 200)
	if strings.Contains(page.Body.String(), browserTestToken) || !strings.Contains(page.Body.String(), "/dashboard/assets/") {
		t.Fatal("assets exposed credentials or were not built")
	}
}
