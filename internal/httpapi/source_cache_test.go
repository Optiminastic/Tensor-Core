package httpapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The cache spares the clicks within a table, never a page's own load.

func counter(values ...int) (func(context.Context) (int, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) (int, error) {
		n := calls.Add(1)
		return values[min(int(n)-1, len(values)-1)], nil
	}, &calls
}

func TestSourceCacheServesClicksAndRefreshesOnLoad(t *testing.T) {
	c := newSourceCache[int](time.Minute)
	fetch, calls := counter(1, 2, 3)
	ctx := context.Background()

	// A first load reads the source.
	if v, _ := c.get(ctx, "k", false, fetch); v != 1 || calls.Load() != 1 {
		t.Fatalf("first load = %d after %d reads", v, calls.Load())
	}
	// A click within the table is answered from the copy.
	if v, _ := c.get(ctx, "k", true, fetch); v != 1 || calls.Load() != 1 {
		t.Errorf("click = %d after %d reads, want the cached 1 and no new read", v, calls.Load())
	}
	// A reload (counts wanted) always reads, and refreshes the copy.
	if v, _ := c.get(ctx, "k", false, fetch); v != 2 || calls.Load() != 2 {
		t.Errorf("reload = %d after %d reads, want a fresh 2", v, calls.Load())
	}
	if v, _ := c.get(ctx, "k", true, fetch); v != 2 {
		t.Errorf("click after the reload = %d, want the refreshed 2", v)
	}
	// Another key is its own entry.
	if v, _ := c.get(ctx, "other", true, fetch); v != 3 {
		t.Errorf("a new key = %d, want a read", v)
	}
}

func TestSourceCacheExpires(t *testing.T) {
	c := newSourceCache[int](10 * time.Millisecond)
	fetch, calls := counter(1, 2)
	ctx := context.Background()
	_, _ = c.get(ctx, "k", true, fetch)
	time.Sleep(20 * time.Millisecond)
	if v, _ := c.get(ctx, "k", true, fetch); v != 2 || calls.Load() != 2 {
		t.Errorf("after the TTL = %d after %d reads, want a fresh read", v, calls.Load())
	}
}

func TestSourceCacheNeverKeepsAFailure(t *testing.T) {
	c := newSourceCache[int](time.Minute)
	ctx := context.Background()
	failing := func(context.Context) (int, error) { return 0, errors.New("tunnel down") }
	if _, err := c.get(ctx, "k", true, failing); err == nil {
		t.Fatal("the failure was not returned")
	}
	fetch, calls := counter(7)
	if v, err := c.get(ctx, "k", true, fetch); err != nil || v != 7 || calls.Load() != 1 {
		t.Errorf("after an outage = %d (%v), want a fresh read, not a remembered failure", v, err)
	}
}

func TestSourceCacheSharesOneReadBetweenConcurrentCallers(t *testing.T) {
	c := newSourceCache[int](time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 5, nil
	}
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = c.get(context.Background(), "k", false, slow)
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("8 concurrent callers made %d reads, want 1", calls.Load())
	}
	for i, v := range results {
		if v != 5 {
			t.Errorf("caller %d got %d", i, v)
		}
	}
}
