// Package retry provides a small, dependency-free retry loop with exponential
// backoff and jitter for the backend's outbound calls (Shopify, the slicer
// dispatcher). It is context-aware: a cancelled or expired context stops the
// loop immediately, and a server-supplied Retry-After delay overrides the
// computed backoff so we respect a provider's rate limit rather than fight it.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// Policy controls the retry loop. The zero value is not useful; use Default or
// build one explicitly.
type Policy struct {
	// MaxAttempts is the total number of tries, including the first. A value
	// below 1 is treated as 1 (no retry).
	MaxAttempts int
	// BaseDelay is the delay before the second attempt; it doubles each round.
	BaseDelay time.Duration
	// MaxDelay caps the per-round backoff after jitter.
	MaxDelay time.Duration
}

// Default is a conservative policy for short, latency-sensitive outbound calls:
// up to three tries with 200ms growing to at most 2s between them.
func Default() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond, MaxDelay: 2 * time.Second}
}

// retryableError lets a caller signal that an error is worth retrying and, for
// rate limiting, how long to wait. Outbound clients wrap their transient errors
// in a value implementing this interface (see shopify/slicing sentinels).
type retryableError interface {
	Retryable() bool
	// RetryAfter reports a server-requested delay, or zero when none was given.
	RetryAfter() time.Duration
}

// Retryable reports whether err should be retried. An error is retryable when it
// (or something it wraps) implements retryableError and returns true. A context
// error is never retryable here — the loop handles cancellation directly.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var re retryableError
	if errors.As(err, &re) {
		return re.Retryable()
	}
	return false
}

// retryAfter extracts a server-requested delay from err, or zero.
func retryAfter(err error) time.Duration {
	var re retryableError
	if errors.As(err, &re) {
		return re.RetryAfter()
	}
	return 0
}

// Do runs fn until it succeeds, returns a non-retryable error, exhausts the
// policy's attempts, or ctx is done. It returns the last error fn produced (or
// the context error if that ended the loop first).
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		// Last attempt, or an error we must not retry: stop now.
		if attempt == attempts || !Retryable(lastErr) {
			return lastErr
		}

		wait := backoff(p, attempt)
		if after := retryAfter(lastErr); after > 0 {
			wait = after
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastErr
		case <-timer.C:
		}
	}
	return lastErr
}

// backoff computes the delay before the (attempt+1)-th try: BaseDelay doubled
// per round, capped at MaxDelay, with full jitter to avoid thundering herds.
func backoff(p Policy, attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	d := p.BaseDelay << (attempt - 1) // attempt is 1-based
	if d <= 0 || d > p.MaxDelay {     // guard against shift overflow too
		d = p.MaxDelay
	}
	if d <= 0 {
		return 0
	}
	// Full jitter in [0, d]. rand's default source is fine: we only need spread,
	// not cryptographic randomness.
	return time.Duration(rand.Int63n(int64(d) + 1))
}
