package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// A short cache over the outside services the paged tables read whole.
//
// Shopify's abandoned checkouts and BambuBuddy's queue and archive cannot be
// asked for one page, so the backend reads them whole and cuts the page
// itself (external_pages.go). Without this, every click inside one of those
// tables - next page, a tab, each pause in typing a search - re-read Shopify
// or went back down the BambuBuddy tunnel, for an answer it had seconds ago.
//
// WHEN IT IS USED, and when it is not, is the part that matters:
//
//   - A request paging WITHIN a table (counts=false, which the browser sends
//     once it holds the badges) is served from here if the copy is younger
//     than the TTL.
//   - A page's own first load and every re-render after an action
//     (counts=true), and every unpaged caller, always read the source - and
//     refresh this copy. Somebody who has just queued a plate and opens the
//     page sees it; the cache only spares the clicks that follow.
//
// Only successes are kept: an outage must not be remembered as an empty list.
// Callers arriving while a read is in flight wait for it rather than starting
// their own. Keys are bounded by brands and windows - a handful - and the map
// is cleared outright past sourceCacheMaxKeys rather than swept.
const (
	sourceCacheTTL      = 45 * time.Second
	sourceCacheMaxKeys  = 64
	sourceFetchDeadline = 30 * time.Second
)

type sourceEntry[T any] struct {
	value  T
	expiry time.Time
}

// sourceCache is one outside read, cached by key. Safe for concurrent use.
type sourceCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]sourceEntry[T]
	pending map[string]*inflight[T]
}

func newSourceCache[T any](ttl time.Duration) *sourceCache[T] {
	return &sourceCache[T]{
		ttl: ttl, entries: map[string]sourceEntry[T]{}, pending: map[string]*inflight[T]{},
	}
}

// get answers from the cache when cached is true and the copy is fresh;
// otherwise it reads the source - once per key at a time - and keeps a success.
func (c *sourceCache[T]) get(
	ctx context.Context, key string, cached bool, fetch func(context.Context) (T, error),
) (T, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && cached && time.Now().Before(e.expiry) {
		c.mu.Unlock()
		return e.value, nil
	}
	if call, ok := c.pending[key]; ok {
		c.mu.Unlock()
		return waitFor(ctx, call)
	}
	call := &inflight[T]{done: make(chan struct{})}
	c.pending[key] = call
	c.mu.Unlock()

	// Detached from this request: others may be waiting on the same read, and
	// one caller giving up must not fail theirs.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceFetchDeadline)
	call.value, call.err = fetch(fetchCtx)
	cancel()

	c.mu.Lock()
	delete(c.pending, key)
	if call.err == nil {
		if len(c.entries) >= sourceCacheMaxKeys {
			c.entries = map[string]sourceEntry[T]{}
		}
		c.entries[key] = sourceEntry[T]{value: call.value, expiry: time.Now().Add(c.ttl)}
	}
	c.mu.Unlock()
	close(call.done)
	return call.value, call.err
}

// sourceCaches are the three outside reads the paged tables share.
type sourceCaches struct {
	abandoned *sourceCache[[]shopify.AbandonedCheckout]
	queue     *sourceCache[[]bambubuddy.QueueItem]
	archive   *sourceCache[[]bambubuddy.Archive]
}

func newSourceCaches(ttl time.Duration) sourceCaches {
	return sourceCaches{
		abandoned: newSourceCache[[]shopify.AbandonedCheckout](ttl),
		queue:     newSourceCache[[]bambubuddy.QueueItem](ttl),
		archive:   newSourceCache[[]bambubuddy.Archive](ttl),
	}
}

// pagingWithin reports whether this request is a click inside a table that
// already holds its badges - the one kind of read the cache serves.
func pagingWithin(paged bool, wantCounts bool) bool { return paged && !wantCounts }
