package httpapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func snapshotWith(tracked int) *shipmentSnapshot {
	return &shipmentSnapshot{Tracked: tracked, At: time.Now()}
}

// The whole point: a second read inside the TTL must not touch the carrier.
// This is what the filter chips do - each one is a server-rendered link, and
// before the cache each click re-tracked every parcel in the window.
func TestShipmentCacheServesWithinTTL(t *testing.T) {
	var calls int32
	cache := newShipmentCache(time.Minute, time.Second)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		return snapshotWith(703), nil
	}

	for i := 0; i < 5; i++ {
		got, err := cache.get(context.Background(), "brand|30", false, assemble)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if got.Tracked != 703 {
			t.Fatalf("get %d returned %d", i, got.Tracked)
		}
	}
	if calls != 1 {
		t.Errorf("assembled %d times, want 1 - the carrier is being re-read per request", calls)
	}
}

// A different window is a different question, and must not be served the
// answer to the first one.
func TestShipmentCacheKeyedOnWindow(t *testing.T) {
	var calls int32
	cache := newShipmentCache(time.Minute, time.Second)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		return snapshotWith(int(atomic.AddInt32(&calls, 1))), nil
	}

	first, _ := cache.get(context.Background(), shipmentCacheKey("brand", 30), false, assemble)
	second, _ := cache.get(context.Background(), shipmentCacheKey("brand", 7), false, assemble)
	third, _ := cache.get(context.Background(), shipmentCacheKey("other", 30), false, assemble)

	if first.Tracked == second.Tracked || second.Tracked == third.Tracked {
		t.Errorf("windows/brands shared an entry: %d %d %d",
			first.Tracked, second.Tracked, third.Tracked)
	}
	if calls != 3 {
		t.Errorf("assembled %d times, want 3", calls)
	}
}

func TestShipmentCacheExpires(t *testing.T) {
	var calls int32
	cache := newShipmentCache(time.Minute, time.Second)
	clock := time.Now()
	cache.now = func() time.Time { return clock }
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		return snapshotWith(1), nil
	}

	_, _ = cache.get(context.Background(), "k", false, assemble)
	clock = clock.Add(59 * time.Second)
	_, _ = cache.get(context.Background(), "k", false, assemble)
	if calls != 1 {
		t.Fatalf("assembled %d times before the TTL elapsed, want 1", calls)
	}
	clock = clock.Add(2 * time.Second)
	_, _ = cache.get(context.Background(), "k", false, assemble)
	if calls != 2 {
		t.Errorf("assembled %d times after the TTL elapsed, want 2", calls)
	}
}

// Negative caching, and it matters more than it looks. When Delhivery is
// throttling, the worst response to somebody reloading is another burst at the
// service that just asked us to stop.
func TestShipmentCacheRemembersFailuresBriefly(t *testing.T) {
	var calls int32
	cache := newShipmentCache(time.Minute, 15*time.Second)
	clock := time.Now()
	cache.now = func() time.Time { return clock }
	boom := errors.New("Delhivery is rate limiting Tensor")
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		return nil, boom
	}

	if _, err := cache.get(context.Background(), "k", false, assemble); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if _, err := cache.get(context.Background(), "k", false, assemble); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if calls != 1 {
		t.Errorf("retried %d times immediately, want 1 - a reload must not re-burst", calls)
	}

	// But it must not be remembered as long as a success, or a transient
	// failure strands the page.
	clock = clock.Add(16 * time.Second)
	_, _ = cache.get(context.Background(), "k", false, assemble)
	if calls != 2 {
		t.Errorf("assembled %d times after the error TTL, want 2", calls)
	}
}

// refresh=1 skips the cached value, but must still JOIN an assembly already
// running - somebody pressing refresh wants a fresh answer, not a second burst
// at a carrier that is already being read.
func TestShipmentCacheRefreshJoinsInflight(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	cache := newShipmentCache(time.Minute, time.Second)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return snapshotWith(42), nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = cache.get(context.Background(), "k", false, assemble)
	}()
	// Wait for the first call to be inside assemble, which it only reaches
	// after registering itself as in-flight.
	for atomic.LoadInt32(&calls) == 0 {
		time.Sleep(time.Millisecond)
	}

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.get(context.Background(), "k", true, assemble)
			if err != nil {
				t.Errorf("refresh: %v", err)
				return
			}
			if got.Tracked != 42 {
				t.Errorf("refresh returned %d", got.Tracked)
			}
		}()
	}

	// The refreshers have to actually REACH the cache before the gate opens.
	// Without this the first call can finish first, and they then correctly
	// bypass a settled entry and assemble again - which is the next test's
	// behaviour, not this one's.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Errorf("assembled %d times, want 1 - refresh must join the call in flight", calls)
	}
}

// A refresh AFTER the first call has settled does re-read: that is what the
// button is for.
func TestShipmentCacheRefreshBypassesSettledEntry(t *testing.T) {
	var calls int32
	cache := newShipmentCache(time.Minute, time.Second)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		return snapshotWith(int(atomic.AddInt32(&calls, 1))), nil
	}

	_, _ = cache.get(context.Background(), "k", false, assemble)
	got, _ := cache.get(context.Background(), "k", true, assemble)
	if calls != 2 || got.Tracked != 2 {
		t.Errorf("assembled %d times, returned %d - want a fresh read", calls, got.Tracked)
	}
}

// Many requests arriving at a cold cache must produce ONE assembly, not one
// each. This is the case that actually happens: a page load fires the list and
// the browser revalidates behind it.
func TestShipmentCacheCollapsesConcurrentMisses(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	cache := newShipmentCache(time.Minute, time.Second)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return snapshotWith(7), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.get(context.Background(), "k", false, assemble)
			if err != nil || got.Tracked != 7 {
				t.Errorf("got %v, %v", got, err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls != 1 {
		t.Errorf("assembled %d times for 20 concurrent readers, want 1", calls)
	}
}

// A zero TTL disables the cache entirely, which is how a test or a debugging
// session gets the uncached path.
func TestShipmentCacheDisabled(t *testing.T) {
	var calls int32
	cache := newShipmentCache(0, 0)
	assemble := func(context.Context) (*shipmentSnapshot, error) {
		atomic.AddInt32(&calls, 1)
		return snapshotWith(1), nil
	}
	_, _ = cache.get(context.Background(), "k", false, assemble)
	_, _ = cache.get(context.Background(), "k", false, assemble)
	if calls != 2 {
		t.Errorf("assembled %d times with the cache off, want 2", calls)
	}
}
