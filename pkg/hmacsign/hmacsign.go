// Package hmacsign implements the EventRelay webhook signature scheme.
//
// The scheme is:
//
//	signed_payload = "v1:" + timestamp + ":" + raw_request_body
//	signature      = lower_hex( HMAC-SHA256( secret, signed_payload ) )
//	header         = "t=" + timestamp + ",v1=" + signature
//
// A header may carry more than one v1 signature so that a secret can be
// rotated without rejecting in-flight requests.
package hmacsign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Scheme is the signature version written into the header.
const Scheme = "v1"

// HeaderName is the request header carrying the signature.
const HeaderName = "X-EventRelay-Signature"

// DefaultTolerance is the accepted clock skew for a signature timestamp.
const DefaultTolerance = 5 * time.Minute

var (
	// ErrMalformed means the header could not be parsed.
	ErrMalformed = errors.New("hmacsign: malformed signature header")
	// ErrExpired means the timestamp is outside the accepted tolerance.
	ErrExpired = errors.New("hmacsign: signature timestamp outside tolerance")
	// ErrNoMatch means no provided signature matched the expected one.
	ErrNoMatch = errors.New("hmacsign: signature does not match")
)

func signedPayload(timestamp int64, body []byte) []byte {
	prefix := Scheme + ":" + strconv.FormatInt(timestamp, 10) + ":"
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, body...)
	return out
}

// Sign returns the hex-encoded signature for body at the given Unix timestamp.
func Sign(secret []byte, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(signedPayload(timestamp, body))
	return hex.EncodeToString(mac.Sum(nil))
}

// Header builds a complete signature header value.
func Header(secret []byte, timestamp int64, body []byte) string {
	return fmt.Sprintf("t=%d,%s=%s", timestamp, Scheme, Sign(secret, timestamp, body))
}

// Parse extracts the timestamp and all v1 signatures from a header value.
// It never returns partial results: an error means the header is unusable.
func Parse(header string) (timestamp int64, signatures []string, err error) {
	if strings.TrimSpace(header) == "" {
		return 0, nil, ErrMalformed
	}

	var haveTimestamp bool
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return 0, nil, ErrMalformed
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "t":
			if haveTimestamp {
				return 0, nil, ErrMalformed
			}
			ts, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil {
				return 0, nil, ErrMalformed
			}
			timestamp = ts
			haveTimestamp = true
		case Scheme:
			if value == "" {
				return 0, nil, ErrMalformed
			}
			if _, decErr := hex.DecodeString(value); decErr != nil {
				return 0, nil, ErrMalformed
			}
			signatures = append(signatures, strings.ToLower(value))
		default:
			// Unknown fields are ignored so the scheme can be extended.
		}
	}

	if !haveTimestamp || len(signatures) == 0 {
		return 0, nil, ErrMalformed
	}
	return timestamp, signatures, nil
}

// Verify checks a header against the body. tolerance may be zero, in which
// case DefaultTolerance is used.
func Verify(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	timestamp, signatures, err := Parse(header)
	if err != nil {
		return err
	}

	skew := now.Unix() - timestamp
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > tolerance {
		return ErrExpired
	}

	expected := Sign(secret, timestamp, body)
	for _, candidate := range signatures {
		if hmac.Equal([]byte(candidate), []byte(expected)) {
			return nil
		}
	}
	return ErrNoMatch
}
