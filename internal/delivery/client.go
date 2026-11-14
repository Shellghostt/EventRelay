// Package delivery signs and sends one bounded HTTP attempt.
package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/pkg/hmacsign"
)

type Client struct{ http *http.Client }

func New(timeout time.Duration, allowPrivate bool) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Environment proxies must not bypass destination checks.
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 16
	transport.MaxResponseHeaderBytes = 64 << 10
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("destination has no addresses")
		}
		for _, ip := range ips {
			if !allowPrivate && !PublicIP(ip) {
				return nil, errors.New("destination address is blocked")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &Client{http: &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !ipv6GlobalUnicast.Contains(ip) {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func (c *Client) Close() { c.http.CloseIdleConnections() }
func Classify(status int) string {
	if status >= 200 && status < 300 {
		return "success"
	}
	if status == 408 || status == 425 || status == 429 || status >= 500 {
		return "retryable"
	}
	return "non_retryable"
}

func (c *Client) Deliver(ctx context.Context, l store.Lease) (result store.AttemptResult) {
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Endpoint.URL, bytes.NewReader(l.Event.Payload))
	if err != nil {
		return store.AttemptResult{Outcome: "non_retryable", Error: "invalid destination URL"}
	}
	if req.URL.User != nil || req.URL.Hostname() == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return store.AttemptResult{Outcome: "non_retryable", Error: "invalid destination URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "EventRelay/0.1")
	req.Header.Set("X-EventRelay-Event-Id", l.Event.ID.String())
	req.Header.Set("X-EventRelay-Attempt", strconv.Itoa(int(l.Event.AttemptCount)))
	req.Header.Set("X-Event-Type", l.Event.EventType)
	req.Header.Set(hmacsign.HeaderName, hmacsign.Header(l.Endpoint.DeliverySecret, time.Now().Unix(), l.Event.Payload))
	resp, err := c.http.Do(req)
	if err != nil {
		var n net.Error
		if errors.As(err, &n) && n.Timeout() {
			return store.AttemptResult{Outcome: "timeout", Error: "delivery deadline exceeded"}
		}
		return store.AttemptResult{Outcome: "network", Error: "delivery connection failed"}
	}
	defer func() { _ = resp.Body.Close() }()
	result.Outcome = Classify(resp.StatusCode)
	result.Status = resp.StatusCode
	// Read at most 1 KiB. Response content is deliberately omitted from storage:
	// an arbitrary body cannot be reliably redacted by a credential regex.
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if err != nil && result.Outcome != "non_retryable" {
		return store.AttemptResult{Outcome: "network", Status: resp.StatusCode, Error: "response body read failed"}
	}
	if result.Outcome != "success" {
		result.Error = "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return result
}
