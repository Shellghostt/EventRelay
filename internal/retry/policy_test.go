package retry

import (
	"errors"
	"math"
	"net/http"
	"testing"
	"time"
)

var completion = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// sequence returns a randFloat64 yielding the samples in order and fails the
// test if more draws happen, proving a code path never draws jitter.
func sequence(t *testing.T, samples ...float64) func() float64 {
	t.Helper()
	i := 0
	return func() float64 {
		if i >= len(samples) {
			t.Fatalf("unexpected jitter draw #%d", i+1)
		}
		f := samples[i]
		i++
		return f
	}
}

func assertApprox(t *testing.T, got time.Time, want time.Duration) {
	t.Helper()
	delta := got.Sub(completion)
	if math.Abs(float64(delta-want)) > float64(time.Millisecond) {
		t.Fatalf("wait = %v, want %v (+/-1ms)", delta, want)
	}
}

func TestNominalWaitTable(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 10 * time.Second}, {2, 30 * time.Second}, {3, 2 * time.Minute},
		{4, 10 * time.Minute}, {5, 30 * time.Minute}, {6, 2 * time.Hour},
		{7, 6 * time.Hour}, {8, 24 * time.Hour}, {15, 24 * time.Hour}, {19, 24 * time.Hour},
	}
	for _, c := range cases {
		got, ok := NominalWait(c.attempt)
		if !ok || got != c.want {
			t.Fatalf("NominalWait(%d) = %v,%v; want %v,true", c.attempt, got, ok, c.want)
		}
	}
	for _, attempt := range []int{-1, 0, 20, 21} {
		if _, ok := NominalWait(attempt); ok {
			t.Fatalf("NominalWait(%d) scheduled an entry; want none", attempt)
		}
	}
}

func TestNominalTotal(t *testing.T) {
	var total time.Duration
	for attempt := 1; attempt <= 7; attempt++ {
		w, ok := NominalWait(attempt)
		if !ok {
			t.Fatalf("NominalWait(%d) missing", attempt)
		}
		total += w
	}
	if total != 31360*time.Second {
		t.Fatalf("seven nominal waits total %v; want 31360s (8h42m40s)", total)
	}
}

func TestDecideJitterBounds(t *testing.T) {
	cases := []struct {
		sample float64
		want   time.Duration
	}{
		{0.0, 8 * time.Second},  // 10s * 0.8
		{0.5, 10 * time.Second}, // 10s * 1.0
		{0.9999, 12 * time.Second},
	}
	for _, c := range cases {
		d, err := Decide(Input{
			Outcome: "retryable", StatusCode: 500, FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion,
		}, sequence(t, c.sample))
		if err != nil {
			t.Fatalf("sample %v: %v", c.sample, err)
		}
		assertApprox(t, d.NextAttempt, c.want)
		if d.Terminal || d.Diagnostic != "" {
			t.Fatalf("sample %v: unexpected terminal/diagnostic %+v", c.sample, d)
		}
	}
}

func TestDecideExhaustionBeforeSchedule(t *testing.T) {
	cases := []struct {
		name          string
		outcome       string
		failedAttempt int
		maxAttempts   int
	}{
		{"min budget one", "retryable", 1, 1},
		{"default budget attempt eight", "retryable", 8, 8},
		{"ceiling attempt twenty", "network", 20, 20},
		{"timeout at budget", "timeout", 3, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Decide(Input{
				Outcome: c.outcome, StatusCode: 500, FailedAttempt: c.failedAttempt,
				MaxAttempts: c.maxAttempts, CompletedAt: completion,
			}, sequence(t)) // zero samples: any draw fails the test
			if err != nil {
				t.Fatal(err)
			}
			if !d.Terminal || d.Reason != ReasonAttemptsExhausted {
				t.Fatalf("decision = %+v; want terminal %s", d, ReasonAttemptsExhausted)
			}
			if !d.NextAttempt.IsZero() || d.Diagnostic != "" {
				t.Fatalf("terminal decision scheduled or diagnosed: %+v", d)
			}
		})
	}
	// Just below each budget the policy wait applies.
	live := []struct {
		failedAttempt, maxAttempts int
		want                       time.Duration
	}{
		{7, 8, 288 * time.Minute},    // 6h * 0.8
		{19, 20, 1152 * time.Minute}, // 24h * 0.8
	}
	for _, c := range live {
		d, err := Decide(Input{
			Outcome: "retryable", StatusCode: 500, FailedAttempt: c.failedAttempt,
			MaxAttempts: c.maxAttempts, CompletedAt: completion,
		}, sequence(t, 0.0))
		if err != nil {
			t.Fatal(err)
		}
		if d.Terminal {
			t.Fatalf("attempt %d/%d must not be terminal: %+v", c.failedAttempt, c.maxAttempts, d)
		}
		assertApprox(t, d.NextAttempt, c.want)
	}
}

func TestDecideNonRetryable(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		want       string
	}{
		{"plain 4xx", 400, ReasonNonRetryableResponse},
		{"410 gone", 410, ReasonNonRetryableResponse},
		{"3xx not followed", 302, ReasonNonRetryableResponse},
		{"refused before any response", 0, ReasonNonRetryablePolicy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Decide(Input{
				Outcome: "non_retryable", StatusCode: c.statusCode,
				FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion,
			}, sequence(t)) // no draw expected
			if err != nil {
				t.Fatal(err)
			}
			if !d.Terminal || d.Reason != c.want || !d.NextAttempt.IsZero() {
				t.Fatalf("decision = %+v; want terminal %s", d, c.want)
			}
		})
	}
}

func TestDecidePolicyRejectionBeforeExhaustion(t *testing.T) {
	cases := []struct {
		name          string
		failedAttempt int
		maxAttempts   int
	}{
		{"first attempt", 1, 8},
		{"final default attempt", 8, 8},
		{"final ceiling attempt", 20, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Decide(Input{
				Outcome: "network", PolicyRejected: true, FailedAttempt: c.failedAttempt,
				MaxAttempts: c.maxAttempts, CompletedAt: completion,
			}, sequence(t))
			if err != nil {
				t.Fatal(err)
			}
			if !d.Terminal || d.Reason != ReasonNonRetryablePolicy || !d.NextAttempt.IsZero() || d.Diagnostic != "" {
				t.Fatalf("policy rejection = %+v; want immediate %s without jitter", d, ReasonNonRetryablePolicy)
			}
		})
	}
}

func TestDecideRetryAfterForBrokenResponse(t *testing.T) {
	cases := []struct {
		name          string
		outcome       string
		status        int
		header        string
		failedAttempt int
		want          time.Duration
		diagnostic    string
	}{
		{"truncated 429", "network", 429, "3600", 1, time.Hour, ""},
		{"truncated 503", "network", 503, "3600", 1, time.Hour, ""},
		{"response before timeout", "timeout", 503, "3600", 1, time.Hour, ""},
		{"HTTP date", "network", 503, completion.Add(time.Hour).Format(http.TimeFormat), 1, time.Hour, ""},
		{"header cap", "network", 503, "172800", 1, 24 * time.Hour, ""},
		{"longer policy wait", "network", 503, "3600", 7, 6 * time.Hour, ""},
		{"invalid header", "network", 429, "soon", 1, 10 * time.Second, DiagnosticInvalidRetryAfter},
		{"broken 2xx", "network", 200, "3600", 1, 10 * time.Second, ""},
		{"no response", "network", 0, "3600", 1, 10 * time.Second, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Decide(Input{
				Outcome: c.outcome, StatusCode: c.status, RetryAfter: c.header,
				FailedAttempt: c.failedAttempt, MaxAttempts: 8, CompletedAt: completion,
			}, sequence(t, 0.5))
			if err != nil {
				t.Fatal(err)
			}
			assertApprox(t, d.NextAttempt, c.want)
			if d.Terminal || d.Diagnostic != c.diagnostic {
				t.Fatalf("broken response decision = %+v; want diagnostic %q", d, c.diagnostic)
			}
		})
	}
}

func TestDecideRetryAfterPrecedence(t *testing.T) {
	future := func(d time.Duration) string {
		return completion.Add(d).UTC().Format(http.TimeFormat)
	}
	cases := []struct {
		name          string
		retryAfter    string
		failedAttempt int
		sample        float64
		want          time.Duration
		diagnostic    bool
	}{
		// "Retry-After: 3600 after failure 1 waits one hour."
		{"seconds win after attempt 1", "3600", 1, 0.0, time.Hour, false},
		// "after failure 7 the longer policy wait wins" (6h * 0.8 = 4.8h).
		{"policy wait wins after attempt 7", "3600", 7, 0.0, 288 * time.Minute, false},
		{"policy wait wins while jittered higher", "3600", 7, 0.9999,
			time.Duration(float64(6*time.Hour) * (0.8 + 0.9999*0.4)), false},
		{"zero header does not bypass backoff", "0", 1, 0.0, 8 * time.Second, false},
		{"negative header ignored", "-5", 1, 0.0, 8 * time.Second, true},
		{"malformed header ignored", "soon", 1, 0.0, 8 * time.Second, true},
		{"overflow header ignored", "99999999999999999999", 1, 0.0, 8 * time.Second, true},
		{"past date ignored", future(-time.Hour), 1, 0.0, 8 * time.Second, true},
		{"future date wins", future(30 * time.Minute), 1, 0.0, 30 * time.Minute, false},
		{"future date capped at 24h", future(72 * time.Hour), 1, 0.0, 24 * time.Hour, false},
		{"huge seconds capped not ignored", "9223372036854775807", 1, 0.0, 24 * time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Decide(Input{
				Outcome: "retryable", StatusCode: 429, RetryAfter: c.retryAfter,
				FailedAttempt: c.failedAttempt, MaxAttempts: 20, CompletedAt: completion,
			}, sequence(t, c.sample))
			if err != nil {
				t.Fatal(err)
			}
			assertApprox(t, d.NextAttempt, c.want)
			if got := d.Diagnostic != ""; got != c.diagnostic {
				t.Fatalf("diagnostic = %q; want presence %v", d.Diagnostic, c.diagnostic)
			}
			if d.Terminal {
				t.Fatalf("unexpected terminal decision: %+v", d)
			}
		})
	}
	// The exact 3600-second case must not be jittered below the header.
	t.Run("jitter never shortens the header wait", func(t *testing.T) {
		d, err := Decide(Input{
			Outcome: "retryable", StatusCode: 503, RetryAfter: "3600",
			FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion,
		}, sequence(t, 0.9999))
		if err != nil {
			t.Fatal(err)
		}
		if d.NextAttempt.Sub(completion) < time.Hour {
			t.Fatalf("header wait shortened to %v", d.NextAttempt.Sub(completion))
		}
	})
	// Attempt 7 uses the repeated-entry boundary correctly.
	t.Run("attempt eight under ceiling schedules 24h", func(t *testing.T) {
		d, err := Decide(Input{
			Outcome: "retryable", StatusCode: 500, FailedAttempt: 8, MaxAttempts: 20,
			CompletedAt: completion,
		}, sequence(t, 0.5))
		if err != nil {
			t.Fatal(err)
		}
		assertApprox(t, d.NextAttempt, 24*time.Hour)
	})
	// Timeout and network outcomes have no response, so no header is honored.
	t.Run("timeout ignores any header", func(t *testing.T) {
		d, err := Decide(Input{
			Outcome: "timeout", StatusCode: 0, RetryAfter: "3600",
			FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion,
		}, sequence(t, 0.0))
		if err != nil {
			t.Fatal(err)
		}
		assertApprox(t, d.NextAttempt, 8*time.Second)
		if d.Diagnostic != "" {
			t.Fatalf("unexpected diagnostic %q", d.Diagnostic)
		}
	})
}

func TestParseRetryAfter(t *testing.T) {
	date := func(d time.Duration) string { return completion.Add(d).UTC().Format(http.TimeFormat) }
	cases := []struct {
		value       string
		want        time.Duration
		present     bool
		invalid     bool
		completedAt time.Time
	}{
		{"", 0, false, false, completion},
		{"   ", 0, false, false, completion},
		{"0", 0, true, false, completion},
		{"3600", time.Hour, true, false, completion},
		{"86399", 86399 * time.Second, true, false, completion},
		{"86400", 24 * time.Hour, true, false, completion},
		{"86401", 24 * time.Hour, true, false, completion},
		{"-1", 0, true, true, completion},
		{"soon", 0, true, true, completion},
		{"99999999999999999999", 0, true, true, completion},
		{date(0), 0, true, true, completion},                      // not in the future
		{date(time.Second), time.Second, true, false, completion}, // barely future
		{date(-time.Second), 0, true, true, completion},           // past
		{date(48 * time.Hour), 24 * time.Hour, true, false, completion},
	}
	for _, c := range cases {
		wait, present, invalid := ParseRetryAfter(c.value, c.completedAt)
		if wait != c.want || present != c.present || invalid != c.invalid {
			t.Fatalf("ParseRetryAfter(%q) = %v,%v,%v; want %v,%v,%v",
				c.value, wait, present, invalid, c.want, c.present, c.invalid)
		}
	}
}

func TestDecideInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"failed attempt below one", Input{Outcome: "retryable", FailedAttempt: 0, MaxAttempts: 8, CompletedAt: completion}},
		{"max attempts below one", Input{Outcome: "retryable", FailedAttempt: 1, MaxAttempts: 0, CompletedAt: completion}},
		{"max attempts above ceiling", Input{Outcome: "retryable", FailedAttempt: 1, MaxAttempts: 21, CompletedAt: completion}},
		{"unset completion", Input{Outcome: "retryable", FailedAttempt: 1, MaxAttempts: 8}},
		{"success is not schedulable", Input{Outcome: "success", StatusCode: 200, FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion}},
		{"unknown outcome", Input{Outcome: "weird", FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decide(c.in, sequence(t, 0.0))
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v; want ErrInvalidInput", err)
			}
		})
	}
	t.Run("nil random source", func(t *testing.T) {
		if _, err := Decide(Input{Outcome: "retryable", FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion}, nil); err == nil {
			t.Fatal("nil random source accepted")
		}
	})
	t.Run("policy without schedule", func(t *testing.T) {
		_, err := Policy{}.Decide(Input{Outcome: "retryable", FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion}, sequence(t, 0.0))
		if err == nil {
			t.Fatal("empty policy accepted")
		}
	})
}

func TestPolicyZeroValueIsDeterministic(t *testing.T) {
	// The zero policy never draws jitter, which is what harnesses and
	// store-level tests use when timing must be exact.
	flat := Policy{Waits: func(int) (time.Duration, bool) { return 50 * time.Millisecond, true }}
	d, err := flat.Decide(Input{
		Outcome: "retryable", StatusCode: 500, FailedAttempt: 1, MaxAttempts: 8, CompletedAt: completion,
	}, sequence(t)) // zero samples: any draw fails the test
	if err != nil {
		t.Fatal(err)
	}
	if got := d.NextAttempt.Sub(completion); got != 50*time.Millisecond {
		t.Fatalf("wait = %v; want exactly 50ms", got)
	}
	w, ok := flat.Wait(1, sequence(t))
	if !ok || w != 50*time.Millisecond {
		t.Fatalf("Wait = %v,%v; want 50ms,true", w, ok)
	}
}

func TestDefaultPolicyWaitBounds(t *testing.T) {
	low, ok := Default.Wait(1, func() float64 { return 0 })
	if !ok || low != 8*time.Second {
		t.Fatalf("lower bound = %v,%v; want 8s,true", low, ok)
	}
	high, ok := Default.Wait(1, func() float64 { return 1 })
	if !ok || high != 12*time.Second {
		t.Fatalf("upper bound = %v,%v; want 12s,true", high, ok)
	}
}
