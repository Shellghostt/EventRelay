package api

import (
	"net/http"
	"net/netip"
	"strings"
)

// Only X-Forwarded-For is supported. Walk from the actual peer right to left
// and stop at the first untrusted hop; leftmost client input cannot override it.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	ip := peer.Addr().Unmap()
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(ip) {
		return ip.String()
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(parts) > 32 {
		return ip.String()
	}
	for i := len(parts) - 1; i >= 0; i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return peer.Addr().Unmap().String()
		}
		ip = next.Unmap()
		if !isTrusted(ip) {
			return ip.String()
		}
	}
	return ip.String()
}
