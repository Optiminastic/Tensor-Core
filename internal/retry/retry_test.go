package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// transient is a test error that opts into retrying, optionally with a
// server-requested delay.
type transient struct {
	after time.Duration
}

func (transient) Error() string               { return "transient" }
func (transient) Retryable() bool             { return true }
func (t transient) RetryAfter() time.Duration { return t.after }

var errPermanent = errors.New("permanent")

func fastPolicy() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func TestDoSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 call, got %d", calls)
	}
}

func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		calls++
		if calls < 3 {
			return transient{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("want 3 calls, got %d", calls)
	}
}

func TestDoStopsOnNonRetryable(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		calls++
		return errPermanent
	})
	if !errors.Is(err, errPermanent) {
		t.Fatalf("want errPermanent, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("non-retryable error must not retry: got %d calls", calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		calls++
		return transient{}
	})
	if err == nil {
		t.Fatal("want the last error, got nil")
	}
	if calls != 3 {
		t.Fatalf("want 3 attempts, got %d", calls)
	}
}

func TestDoHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Do(ctx, Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}, func(context.Context) error {
		calls++
		cancel() // cancel during the first attempt so the backoff wait aborts
		return transient{}
	})
	if err == nil {
		t.Fatal("want the last error, got nil")
	}
	if calls != 1 {
		t.Fatalf("cancellation must stop the loop after one call: got %d", calls)
	}
}

func TestRetryableIgnoresContextErrors(t *testing.T) {
	if Retryable(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded must not be retryable")
	}
	if Retryable(nil) {
		t.Fatal("nil must not be retryable")
	}
	if !Retryable(transient{}) {
		t.Fatal("transient must be retryable")
	}
}

func TestRetryAfterOverridesBackoff(t *testing.T) {
	// A 5ms Retry-After with a large base delay: the loop should wait roughly the
	// Retry-After, not the base. We assert it completes well under the base delay.
	start := time.Now()
	calls := 0
	_ = Do(context.Background(), Policy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second}, func(context.Context) error {
		calls++
		if calls == 1 {
			return transient{after: 5 * time.Millisecond}
		}
		return nil
	})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Retry-After should have shortened the wait, took %v", elapsed)
	}
}
