// Package worker coordinates Redis dispatch, PostgreSQL leases and HTTP attempts.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"math/rand/v2"

	"github.com/google/uuid"
	"github.com/shellghostt/eventrelay/internal/delivery"
	"github.com/shellghostt/eventrelay/internal/retry"
	"github.com/shellghostt/eventrelay/internal/store"
	"github.com/shellghostt/eventrelay/internal/stream"
)

type Worker struct {
	Store         *store.Store
	Stream        *stream.Stream
	Client        *delivery.Client
	Log           *slog.Logger
	Concurrency   int
	LeaseDuration time.Duration
	SweepInterval time.Duration
	// Policy schedules retries and recovery waits. retry.Default applies when
	// its schedule is unset; a shorter injected schedule is for tests only
	// (ADR 0005).
	Policy retry.Policy
	// RandFloat64 returns uniform [0,1) samples and must be safe for
	// concurrent use. The math/rand/v2 global source applies when nil.
	RandFloat64 func() float64
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Concurrency < 1 || w.LeaseDuration <= 0 || w.SweepInterval <= 0 {
		return errors.New("invalid worker configuration")
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if w.Policy.Waits == nil {
		w.Policy = retry.Default
	}
	if w.RandFloat64 == nil {
		w.RandFloat64 = rand.Float64
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); w.sweepLoop(ctx) }()
	for i := 0; i < w.Concurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.consume(ctx, uuid.NewString()) }()
	}
	wg.Wait()
	return nil
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (w *Worker) consume(ctx context.Context, id string) {
	for ctx.Err() == nil {
		if err := w.Stream.Ensure(ctx); err != nil {
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		// A shared consumer avoids leaking a Redis consumer record for each crashed
		// process. PostgreSQL provides exclusivity, not the Redis consumer identity.
		messages, err := w.Stream.Read(ctx, "workers")
		if err != nil {
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		for _, m := range messages {
			tenant, err1 := uuid.Parse(value(m.Values, "tenant_id"))
			event, err2 := uuid.Parse(value(m.Values, "event_id"))
			if err1 != nil || err2 != nil {
				_ = w.Stream.Ack(ctx, m.ID)
				continue
			}
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			lease, err := w.Store.Claim(claimCtx, tenant, event, id, w.LeaseDuration)
			cancel()
			if err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					w.Log.Warn("claim failed", "event_id", event)
				}
				_ = w.Stream.Ack(ctx, m.ID)
				continue
			}
			// Once leased, crash recovery is durable; no Redis pending entry is needed.
			_ = w.Stream.Ack(ctx, m.ID)
			result := w.Client.Deliver(ctx, lease)
			// The completion time drives schedule math; the resulting absolute
			// next attempt time is persisted once inside the fenced write.
			var decision retry.Decision
			if result.Outcome != "success" {
				d, err := w.Policy.Decide(retry.Input{
					Outcome:       result.Outcome,
					StatusCode:    result.Status,
					RetryAfter:    result.RetryAfter,
					FailedAttempt: int(lease.Event.AttemptCount),
					MaxAttempts:   int(lease.Endpoint.MaxAttempts),
					CompletedAt:   time.Now(),
				}, w.RandFloat64)
				if err != nil {
					w.Log.Error("retry scheduling failed; lease recovery will retry", "event_id", event, "error", err)
					continue
				}
				decision = d
			}
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = w.Store.Finish(finishCtx, lease, result, decision)
			finishCancel()
			if err != nil {
				w.Log.Warn("attempt completion not committed; lease recovery will retry", "event_id", event)
				continue
			}
			w.Log.Info("delivery attempt", "event_id", event, "attempt", lease.Event.AttemptCount, "outcome", result.Outcome, "status_code", result.Status, "duration_ms", result.Duration.Milliseconds())
		}
	}
}
func value(values map[string]any, key string) string { s, _ := values[key].(string); return s }

func (w *Worker) sweepLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("sweep failed; will retry")
		}
		if !wait(ctx, w.SweepInterval) {
			return
		}
	}
}

// recoveryNext schedules the attempt after an interrupted one under the worker
// policy. No Retry-After exists: the attempt never produced a response.
func (w *Worker) recoveryNext(failedAttempt int) time.Time {
	wait, ok := w.Policy.Wait(failedAttempt, w.RandFloat64)
	if !ok {
		w.Log.Warn("no schedule entry for interrupted attempt; retrying immediately", "attempt", failedAttempt)
		return time.Now()
	}
	return time.Now().Add(wait)
}

func (w *Worker) Sweep(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tenant := uuid.MustParse(store.DefaultTenantID)
	if _, err := w.Store.RecoverExpired(ctx, tenant, w.recoveryNext); err != nil {
		return err
	}
	if err := w.Stream.Ensure(ctx); err != nil {
		return err
	}
	// Remove abandoned Redis pending entries. Their event rows are republished below.
	if err := w.Stream.CleanPending(ctx, w.LeaseDuration); err != nil {
		return err
	}
	for {
		due, err := w.Store.Due(ctx, tenant, 256, w.SweepInterval)
		if err != nil {
			return err
		}
		for _, d := range due {
			if err := w.Stream.Publish(ctx, d); err != nil {
				return err
			}
		}
		if len(due) < 256 {
			return nil
		}
	}
}
