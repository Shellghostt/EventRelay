package delivery

import (
	"context"
	"github.com/google/uuid"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeliverySignsAndClassifies(t *testing.T) {
	for _, status := range []int{200, 204, 301, 400, 401, 408, 410, 425, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			secret := []byte("delivery-secret")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if err := hmacsign.Verify(secret, r.Header.Get(hmacsign.HeaderName), body, time.Now(), time.Minute); err != nil {
					t.Error(err)
				}
				if r.Header.Get("X-EventRelay-Attempt") != "2" || r.Header.Get("X-EventRelay-Event-Id") == "" {
					t.Error("missing delivery metadata")
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := New(time.Second, true)
			defer c.Close()
			r := c.Deliver(context.Background(), store.Lease{Event: store.Event{ID: uuid.New(), Payload: []byte(`{"a":1}`), AttemptCount: 2}, Endpoint: store.Endpoint{URL: srv.URL, DeliverySecret: secret}})
			if r.Status != status || r.Outcome != Classify(status) || r.Duration <= 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestBlockedDestinationAndTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer srv.Close()
	l := store.Lease{Endpoint: store.Endpoint{URL: srv.URL}}
	c := New(40*time.Millisecond, false)
	defer c.Close()
	if r := c.Deliver(context.Background(), l); r.Outcome != "network" || calls.Load() != 0 {
		t.Fatalf("private destination reached: %+v", r)
	}
	c2 := New(40*time.Millisecond, true)
	defer c2.Close()
	if r := c2.Deliver(context.Background(), l); r.Outcome != "timeout" {
		t.Fatalf("%+v", r)
	}
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "0.0.0.0", "ff02::1", "fc00::1", "64:ff9b::a00:1", "64:ff9b:1::a00:1", "100::1", "2001::1", "2001:20::1", "2002:a00:1::1", "3fff::1", "192.88.99.1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(ip)) {
			t.Errorf("public IP %s blocked", ip)
		}
	}
}

func TestBrokenResponseRetainsTerminalStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	c := New(time.Second, true)
	defer c.Close()
	r := c.Deliver(context.Background(), store.Lease{Endpoint: store.Endpoint{URL: srv.URL}})
	if r.Status != http.StatusBadRequest || r.Outcome != "non_retryable" {
		t.Fatalf("terminal HTTP response became retryable: %+v", r)
	}
}
