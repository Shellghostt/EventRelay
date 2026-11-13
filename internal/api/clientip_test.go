package api

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	for _, tc := range []struct{ peer, xff, want string }{
		{"192.0.2.1:42", "1.2.3.4", "192.0.2.1"},
		{"10.0.0.1:42", "1.2.3.4, 192.0.2.2, 10.0.0.2", "192.0.2.2"},
		{"10.0.0.1:42", "1.2.3.4, garbage", "10.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		if got := clientIP(r, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
}
