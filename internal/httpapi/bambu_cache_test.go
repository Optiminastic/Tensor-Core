package httpapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

// The whole point of this cache is that upstream load stops scaling with the
// number of viewers, so that is what these assert: call counts, not values.

func TestStatusCacheServesFromCacheWithinTTL(t *testing.T) {
	var calls atomic.Int32
	load := func(context.Context, int) (bambubuddy.Status, error) {
		calls.Add(1)
		return bambubuddy.Status{State: "RUNNING"}, nil
	}

	c := newBambuCache(time.Minute, 5*time.Second, time.Second)
	now := time.Now()
	c.now = func() time.Time { return now }

	for range 10 {
		st, err := c.status(context.Background(), 1, load)
		if err != nil || st.State != "RUNNING" {
			t.Fatalf("status = %+v, %v", st, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 - ten viewers must cost one call", got)
	}

	// Past the TTL the next reader refreshes, and only that one.
	now = now.Add(6 * time.Second)
	if _, err := c.status(context.Background(), 1, load); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls after expiry = %d, want 2", got)
	}
}

// Twenty simultaneous arrivals on a cold cache must produce exactly one
// upstream call. This is the case a plain TTL cache (like versionCache) does
// NOT handle, and the one that matters when a dashboard loads.
func TestStatusCacheCollapsesConcurrentMisses(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context, int) (bambubuddy.Status, error) {
		calls.Add(1)
		<-release // hold the call open so every goroutine piles up behind it
		return bambubuddy.Status{State: "RUNNING"}, nil
	}

	c := newBambuCache(time.Minute, 5*time.Second, time.Second)

	var wg sync.WaitGroup
	results := make([]bambubuddy.Status, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _ := c.status(context.Background(), 1, load)
			results[i] = st
		}()
	}
	// Give them time to arrive and block, then let the single fetch finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 for 20 concurrent readers", got)
	}
	for i, st := range results {
		if st.State != "RUNNING" {
			t.Fatalf("waiter %d got %+v, want the shared result", i, st)
		}
	}
}

// One viewer navigating away must not cancel the fetch the others are waiting
// on. This regresses silently to "every waiter fails" if the detached context
// is ever simplified back to the caller's.
func TestStatusCacheSurvivesLeaderCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context, _ int) (bambubuddy.Status, error) {
		close(started)
		<-release
		// If the fetch ran on the leader's context this would already be done.
		if ctx.Err() != nil {
			return bambubuddy.Status{}, ctx.Err()
		}
		return bambubuddy.Status{State: "RUNNING"}, nil
	}

	c := newBambuCache(time.Minute, 5*time.Second, time.Second)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	go func() { _, _ = c.status(leaderCtx, 1, load) }()
	<-started

	// A second viewer arrives and waits on the same call.
	done := make(chan bambubuddy.Status, 1)
	go func() {
		st, _ := c.status(context.Background(), 1, load)
		done <- st
	}()
	time.Sleep(20 * time.Millisecond)

	cancelLeader() // the first tab goes away mid-flight
	close(release)

	select {
	case st := <-done:
		if st.State != "RUNNING" {
			t.Fatalf("waiter got %+v after the leader cancelled; the fetch must outlive it", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never completed after the leader cancelled")
	}
}

// A dead printer costs a full client timeout, so the failure is remembered
// briefly. Without this every poll from every viewer pays that cost again.
func TestStatusCacheRemembersFailuresBriefly(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("printer unreachable")
	load := func(context.Context, int) (bambubuddy.Status, error) {
		calls.Add(1)
		return bambubuddy.Status{}, wantErr
	}

	c := newBambuCache(time.Minute, 30*time.Second, 5*time.Second)
	now := time.Now()
	c.now = func() time.Time { return now }

	for range 5 {
		if _, err := c.status(context.Background(), 1, load); !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want the upstream error", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 - the failure must be cached", got)
	}

	// The error TTL is shorter than the success TTL, so recovery is quick.
	now = now.Add(6 * time.Second)
	if _, err := c.status(context.Background(), 1, load); !errors.Is(err, wantErr) {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls after error TTL = %d, want 2", got)
	}
}

// TTL 0 disables caching, which is what makes the before/after measurement a
// config flip rather than a code change.
func TestStatusCacheDisabled(t *testing.T) {
	var calls atomic.Int32
	load := func(context.Context, int) (bambubuddy.Status, error) {
		calls.Add(1)
		return bambubuddy.Status{State: "IDLE"}, nil
	}
	c := newBambuCache(0, 0, 0)
	for range 4 {
		if _, err := c.status(context.Background(), 1, load); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("upstream calls = %d, want 4 with the cache disabled", got)
	}
}

// The index maps serial -> printer id using the same fleetCode fallback the
// sync uses, and an unknown serial is "not found", not an error.
func TestPrinterIDIndex(t *testing.T) {
	var calls atomic.Int32
	load := func(context.Context) ([]bambubuddy.Printer, error) {
		calls.Add(1)
		return []bambubuddy.Printer{
			{ID: 7, SerialNumber: "SERIAL-A"},
			{ID: 9, SerialNumber: ""}, // falls back to bambubuddy-9
		}, nil
	}

	c := newBambuCache(time.Minute, time.Second, time.Second)

	id, err := c.printerID(context.Background(), "SERIAL-A", load)
	if err != nil || id != 7 {
		t.Fatalf("printerID = %d, %v; want 7", id, err)
	}
	id, err = c.printerID(context.Background(), "bambubuddy-9", load)
	if err != nil || id != 9 {
		t.Fatalf("serial-less printer id = %d, %v; want 9", id, err)
	}
	id, err = c.printerID(context.Background(), "NOT-IN-FLEET", load)
	if err != nil || id != -1 {
		t.Fatalf("unknown serial = %d, %v; want -1 and no error", id, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ListPrinters calls = %d, want 1 for three lookups", got)
	}
}
