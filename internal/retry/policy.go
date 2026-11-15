// Package retry computes the retry schedule and terminal transition for a
// failed delivery attempt.
//
// The production policy is a code constant, not configuration: the nominal
// wait table, the jitter bounds and the Retry-After cap are fixed for M3
// (ADR 0005). Production schedule overrides require a new ADR. Tests may
// construct a Policy with a shorter schedule and no jitter; tests inject the
// random source and the completion time.
package retry

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Terminal reasons persisted in dead_letters.reason. Exactly these three
// values are written (ADR 0005); migration 0003 normalizes older rows.
const (
	// ReasonAttemptsExhausted marks a retryable-classified failure, including
	// timeout and network outcomes and a lease expired on that attempt, whose
	// attempt number reached the endpoint budget.
	ReasonAttemptsExhausted = "attempts_exhausted"
	// ReasonNonRetryableResponse marks a refusal the destination itself
	// returned: any non-retryable HTTP status, including 410 and 3xx.
	ReasonNonRetryableResponse = "non_retryable_response"
	// ReasonNonRetryablePolicy marks a refusal made before any HTTP response
	// existed: an invalid destination URL or a policy-blocked address.
	ReasonNonRetryablePolicy = "non_retryable_policy"
)

// DiagnosticInvalidRetryAfter is recorded when a Retry-After header is present
// but unusable. It is a fixed string so persisted values stay bounded; the raw
// header value is never stored or logged.
const DiagnosticInvalidRetryAfter = "retry-after ignored: invalid value"

const (
	// headerCap bounds a usable Retry-After wait. The cap applies to the
	// header only; a jittered 24-hour policy entry may exceed it.
	headerCap = 24 * time.Hour

	// repeatWait is the nominal wait shared by failed attempts 8 through 19.
	repeatWait = 24 * time.Hour

	// lastIndexedAttempt is the highest failed attempt the table indexes.
	lastIndexedAttempt = 19
)

// baseWaits holds the nominal wait before another attempt after failed
// attempts 1 through 7. Index 0 is unused.
var baseWaits = [8]time.Duration{
	1: 10 * time.Second,
	2: 30 * time.Second,
	3: 2 * time.Minute,
	4: 10 * time.Minute,
	5: 30 * time.Minute,
	6: 2 * time.Hour,
	7: 6 * time.Hour,
}

// NominalWait returns the production policy wait before another attempt after
// the given failed attempt number, and whether that attempt has a scheduled
// entry. Exhaustion is checked before consulting the table.
func NominalWait(failedAttempt int) (time.Duration, bool) {
	if failedAttempt < 1 {
		return 0, false
	}
	if failedAttempt < len(baseWaits) {
		return baseWaits[failedAttempt], true
	}
	if failedAttempt <= lastIndexedAttempt {
		return repeatWait, true
	}
	return 0, false
}

// Policy is one retry schedule. Default is the production M3 policy; the zero
// value is a deterministic schedule with no jitter, which is what tests use
// when timing must be exact.
type Policy struct {
	// JitterLow and JitterHigh bound the uniform multiplier applied to the
	// nominal wait. When the bounds do not span a range, the wait is used
	// unjittered.
	JitterLow, JitterHigh float64
	// Waits maps the failed attempt number to the nominal wait before the next
	// attempt. ok is false when the attempt has no scheduled entry.
	Waits func(failedAttempt int) (time.Duration, bool)
}

// Default is the production M3 policy (ADR 0005).
var Default = Policy{
	JitterLow:  0.8,
	JitterHigh: 1.2,
	Waits:      NominalWait,
}

// Wait returns the jittered wait before another attempt after the given failed
// attempt number, and whether that attempt has a scheduled entry.
func (p Policy) Wait(failedAttempt int, randFloat64 func() float64) (time.Duration, bool) {
	nominal, ok := p.Waits(failedAttempt)
	if !ok {
		return 0, false
	}
	return time.Duration(float64(nominal) * p.factor(randFloat64)), true
}

// factor maps a uniform [0,1) sample onto the policy jitter bounds. Policies
// whose bounds do not span a range never draw the random source.
func (p Policy) factor(randFloat64 func() float64) float64 {
	if p.JitterHigh <= p.JitterLow {
		return 1
	}
	f := randFloat64()
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return p.JitterLow + f*(p.JitterHigh-p.JitterLow)
}

// Input describes one failed attempt to schedule.
type Input struct {
	// Outcome is the attempt classification: "retryable", "non_retryable",
	// "timeout" or "network". "success" never reaches this package.
	Outcome string
	// StatusCode is the HTTP status of the response, or 0 when no response
	// exists (timeout, network error, refused delivery).
	StatusCode int
	// RetryAfter is the raw Retry-After header value, empty when absent. It is
	// parsed and discarded; it is never persisted or logged.
	RetryAfter string
	// FailedAttempt is the number of the attempt that failed.
	FailedAttempt int
	// MaxAttempts is the endpoint's attempt budget, 1 to 20.
	MaxAttempts int
	// CompletedAt is the completion time of the failed attempt.
	CompletedAt time.Time
}

// Decision is the single scheduling outcome of one failed attempt.
type Decision struct {
	// NextAttempt is when the next attempt may run. Zero when Terminal.
	NextAttempt time.Time
	// Terminal reports that the event must move to the dead-letter queue.
	Terminal bool
	// Reason is the dead_letters.reason value when Terminal.
	Reason string
	// Diagnostic is a bounded note recorded when a present Retry-After header
	// had to be ignored. It is empty otherwise.
	Diagnostic string
}

// ErrInvalidInput reports input Decide cannot schedule against.
var ErrInvalidInput = errors.New("retry: invalid scheduling input")

// Decide computes the transition after one failed attempt under the production
// policy.
func Decide(in Input, randFloat64 func() float64) (Decision, error) {
	return Default.Decide(in, randFloat64)
}

// Decide computes the transition after one failed attempt and draws the jitter
// at most once, only when a policy wait is needed. Exhaustion is checked
// before any wait is computed, so the attempt that reaches MaxAttempts never
// schedules an entry. randFloat64 must return a uniform value in [0,1).
func (p Policy) Decide(in Input, randFloat64 func() float64) (Decision, error) {
	if p.Waits == nil {
		return Decision{}, errors.New("retry: policy has no schedule")
	}
	if randFloat64 == nil {
		return Decision{}, errors.New("retry: random source is required")
	}
	if in.FailedAttempt < 1 {
		return Decision{}, fmt.Errorf("%w: failed attempt %d", ErrInvalidInput, in.FailedAttempt)
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 20 {
		return Decision{}, fmt.Errorf("%w: max attempts %d", ErrInvalidInput, in.MaxAttempts)
	}
	if in.CompletedAt.IsZero() {
		return Decision{}, fmt.Errorf("%w: completion time is unset", ErrInvalidInput)
	}
	switch in.Outcome {
	case "retryable", "non_retryable", "timeout", "network":
	default:
		return Decision{}, fmt.Errorf("%w: unknown outcome %q", ErrInvalidInput, in.Outcome)
	}

	// A refusal the destination returned, or one made before any request could
	// be sent, is terminal regardless of the attempt budget.
	if in.Outcome == "non_retryable" {
		reason := ReasonNonRetryableResponse
		if in.StatusCode == 0 {
			reason = ReasonNonRetryablePolicy
		}
		return Decision{Terminal: true, Reason: reason}, nil
	}

	// Exhaustion before scheduling: the final allowed attempt is terminal and
	// never consumes a table entry.
	if in.FailedAttempt >= in.MaxAttempts {
		return Decision{Terminal: true, Reason: ReasonAttemptsExhausted}, nil
	}

	wait, ok := p.Wait(in.FailedAttempt, randFloat64)
	if !ok {
		// Unreachable while MaxAttempts <= 20; kept as a guard so a bad budget
		// can never widen the schedule.
		return Decision{Terminal: true, Reason: ReasonAttemptsExhausted}, nil
	}
	next := in.CompletedAt.UTC().Add(wait)

	// A Retry-After header exists only when a response came back. Timeout and
	// network outcomes have no header to honor.
	if in.Outcome != "retryable" || in.StatusCode == 0 {
		return Decision{NextAttempt: next}, nil
	}

	headerWait, present, invalid := ParseRetryAfter(in.RetryAfter, in.CompletedAt)
	if invalid {
		return Decision{NextAttempt: next, Diagnostic: DiagnosticInvalidRetryAfter}, nil
	}
	// A valid header contributes a minimum wait: the larger of the capped
	// header wait and the jittered policy wait. A zero header therefore does
	// not bypass backoff, and jitter never shortens the header request.
	if present && headerWait > wait {
		next = in.CompletedAt.UTC().Add(headerWait)
	}
	return Decision{NextAttempt: next}, nil
}

// ParseRetryAfter parses one Retry-After header value against the completion
// time of the failed attempt. Seconds are relative to completion; HTTP dates
// are absolute and measured from completion in UTC. It returns the wait capped
// at 24 hours, whether a value was present, and whether that value had to be
// ignored as invalid. An absent header returns (0, false, false).
func ParseRetryAfter(value string, completedAt time.Time) (wait time.Duration, present bool, invalid bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0, false, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, true, true
		}
		// Cap before converting so an in-range integer cannot overflow the
		// duration conversion.
		if secs >= int64(headerCap/time.Second) {
			return headerCap, true, false
		}
		return time.Duration(secs) * time.Second, true, false
	}
	if t, err := http.ParseTime(v); err == nil {
		w := t.Sub(completedAt)
		if w <= 0 {
			return 0, true, true
		}
		if w > headerCap {
			w = headerCap
		}
		return w, true, false
	}
	return 0, true, true
}
