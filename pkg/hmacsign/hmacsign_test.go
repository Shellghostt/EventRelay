package hmacsign_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shellghostt/eventrelay/pkg/hmacsign"
)

// The vector below is published in docs/PLAN.md section 9.1. It is reproduced
// here so that an implementation in another language can be checked against
// the same value.
//
//	secret = "whsec_test"
//	body   = {"order_id":"8891","total":4200}
//	ts     = 1763000000
//	header = t=1763000000,v1=087c7bdb9ce6d9764e566fb7c4cf7e7d6a1f907e8f27205ebecc8ee406226722
const (
	testSecret = "whsec_test"
	testBody   = `{"order_id":"8891","total":4200}`
	testTS     = int64(1763000000)
	testSig    = "087c7bdb9ce6d9764e566fb7c4cf7e7d6a1f907e8f27205ebecc8ee406226722"
)

func testNow() time.Time { return time.Unix(testTS, 0) }

func TestSignMatchesPublishedVector(t *testing.T) {
	got := hmacsign.Sign([]byte(testSecret), testTS, []byte(testBody))
	if got != testSig {
		t.Fatalf("signature mismatch\n got: %s\nwant: %s", got, testSig)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	header := hmacsign.Header([]byte(testSecret), testTS, []byte(testBody))

	if !strings.HasPrefix(header, "t=1763000000,v1=") {
		t.Fatalf("unexpected header shape: %s", header)
	}
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), testNow(), 5*time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	header := hmacsign.Header([]byte(testSecret), testTS, []byte(testBody))
	err := hmacsign.Verify([]byte(testSecret), header, []byte(`{"order_id":"8891","total":1}`), testNow(), 5*time.Minute)
	if !errors.Is(err, hmacsign.ErrNoMatch) {
		t.Fatalf("got %v, want ErrNoMatch", err)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	header := hmacsign.Header([]byte(testSecret), testTS, []byte(testBody))
	err := hmacsign.Verify([]byte("whsec_other"), header, []byte(testBody), testNow(), 5*time.Minute)
	if !errors.Is(err, hmacsign.ErrNoMatch) {
		t.Fatalf("got %v, want ErrNoMatch", err)
	}
}

func TestVerifyRejectsExpiredTimestamp(t *testing.T) {
	header := hmacsign.Header([]byte(testSecret), testTS, []byte(testBody))

	tooLate := testNow().Add(5*time.Minute + time.Second)
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), tooLate, 5*time.Minute); !errors.Is(err, hmacsign.ErrExpired) {
		t.Fatalf("future skew: got %v, want ErrExpired", err)
	}

	tooEarly := testNow().Add(-5*time.Minute - time.Second)
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), tooEarly, 5*time.Minute); !errors.Is(err, hmacsign.ErrExpired) {
		t.Fatalf("past skew: got %v, want ErrExpired", err)
	}
}

func TestVerifyAcceptsRotationWithTwoSignatures(t *testing.T) {
	oldSecret := []byte("whsec_old")
	newSecret := []byte("whsec_new")

	header := "t=1763000000,v1=" + hmacsign.Sign(oldSecret, testTS, []byte(testBody)) +
		",v1=" + hmacsign.Sign(newSecret, testTS, []byte(testBody))

	for name, secret := range map[string][]byte{"old": oldSecret, "new": newSecret} {
		if err := hmacsign.Verify(secret, header, []byte(testBody), testNow(), 5*time.Minute); err != nil {
			t.Fatalf("%s secret: %v", name, err)
		}
	}
}

func TestParseRejectsMalformedHeaders(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"no timestamp":      "v1=" + testSig,
		"no signature":      "t=1763000000",
		"timestamp not int": "t=abc,v1=" + testSig,
		"missing equals":    "t=1763000000,v1" + testSig,
		"duplicate ts":      "t=1763000000,t=1763000001,v1=" + testSig,
		"non-hex sig":       "t=1763000000,v1=zzzz",
		"empty sig value":   "t=1763000000,v1=",
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := hmacsign.Parse(header); !errors.Is(err, hmacsign.ErrMalformed) {
				t.Fatalf("got %v, want ErrMalformed", err)
			}
		})
	}
}

func TestVerifyIgnoresUnknownFields(t *testing.T) {
	header := "t=1763000000,v1=" + testSig + ",future=v2,note=ignored"
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), testNow(), 5*time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyUppercaseSignatureIsAccepted(t *testing.T) {
	header := "t=1763000000,v1=" + strings.ToUpper(testSig)
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), testNow(), 5*time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyUsesDefaultToleranceWhenZero(t *testing.T) {
	header := hmacsign.Header([]byte(testSecret), testTS, []byte(testBody))

	within := testNow().Add(4 * time.Minute)
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), within, 0); err != nil {
		t.Fatalf("within default tolerance: %v", err)
	}

	outside := testNow().Add(6 * time.Minute)
	if err := hmacsign.Verify([]byte(testSecret), header, []byte(testBody), outside, 0); !errors.Is(err, hmacsign.ErrExpired) {
		t.Fatalf("outside default tolerance: %v", err)
	}
}
