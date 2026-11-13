package api_test

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shellghostt/eventrelay/internal/api"
	"github.com/shellghostt/eventrelay/internal/config"
)

// The request log records the peer address, never a client-supplied one.
//
// X-Forwarded-For, True-Client-IP and X-Real-IP are chosen by whoever makes the request, so a log
// that trusted them would let the caller write its own address into the operator's logs. ADR 0003
// records the decision; this test is the regression that keeps it true.
func TestTheRequestLogRecordsThePeerAddressAndNotAForwardedHeader(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	router := api.NewRouter(api.Deps{
		Config: config.Config{AdminToken: "test-token"},
		Log:    logger,
	})

	request := httptest.NewRequest("GET", "/healthz", nil)
	request.RemoteAddr = "10.0.0.1:54321"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("True-Client-IP", "203.0.113.10")
	request.Header.Set("X-Real-IP", "203.0.113.11")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	output := logs.String()
	if !strings.Contains(output, "10.0.0.1:54321") {
		t.Fatalf("the log does not contain the peer address: %s", output)
	}
	for _, spoofed := range []string{"203.0.113.9", "203.0.113.10", "203.0.113.11"} {
		if strings.Contains(output, spoofed) {
			t.Fatalf("the log contains the client-supplied address %s: %s", spoofed, output)
		}
	}
}
