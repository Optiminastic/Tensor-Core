package bambuddy

// An opt-in smoke test against a real BamBuddy instance. Skipped unless
// BAMBUDDY_LIVE_URL is set, so it never runs in CI.
//
// Read-only by design: it proves the URL prefix, auth header and error mapping
// line up with a real server without creating anything on the shop floor. The
// write path (upload + queue) is covered by the httptest tests, because
// exercising it live would put a real job on a real printer.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestLiveSmokeReadOnly(t *testing.T) {
	base := os.Getenv("BAMBUDDY_LIVE_URL")
	if base == "" {
		t.Skip("set BAMBUDDY_LIVE_URL to smoke-test against a real BamBuddy")
	}
	key := os.Getenv("BAMBUDDY_LIVE_API_KEY")

	c := New(base, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// An id that will not exist. What is being checked is the shape of the
	// failure: a clean, typed ErrNotFound means the request reached BamBuddy's
	// router at the right path with an accepted credential. A transport error or
	// ErrUnauthorized would mean the base URL, prefix or key is wrong.
	_, err := c.GetPrinterStatus(ctx, key, 999999)
	switch {
	case err == nil:
		t.Log("printer 999999 unexpectedly exists; the instance is reachable either way")
	case errors.Is(err, ErrNotFound):
		t.Log("reachable: BamBuddy answered a clean 404 for an unknown printer")
	case errors.Is(err, ErrUnauthorized):
		t.Fatalf("BamBuddy rejected the key - check its scopes: %v", err)
	default:
		t.Fatalf("could not reach BamBuddy at %s: %v", base, err)
	}
}
