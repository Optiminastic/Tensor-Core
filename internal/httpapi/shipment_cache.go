package httpapi

// A TTL cache over one brand's whole delivery picture.
//
// IT EXISTS BECAUSE THE CARRIER RATE-LIMITED US. Thirty days on the live store
// is 703 parcels, which is three Shopify pages plus fifteen Delhivery requests
// - and the page was paying all of it on every load AND on every filter chip,
// because the chips are server-rendered links. Delhivery answered HTTP 429.
//
// Probed against the live API afterwards: twelve sequential requests pass, and
// so do bursts of four. So the limit is on sustained volume, which means the
// fix is not a smaller batch or a slower loop - it is not asking the same
// question nine times in a minute. The client's 429 backoff is the second line
// of defence; this is the first.
//
// A SHORT TTL IS HONEST HERE. A parcel's scan trail changes every few hours at
// most: it is a van moving between cities, not a number ticking. Two minutes of
// staleness cannot change what somebody does about a delivery that has been
// stuck since Tuesday, and the response carries generated_at so the page can
// say how old the answer is rather than implying it is live.
//
// Policy lives here rather than in internal/integrations/delhivery for the same
// reason bambu_cache.go keeps BambuBuddy's policy out of its client: the client
// stays pure transport.

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Optiminastic/tensor-core/internal/obs"
)

// shipmentCacheTTL is how long one brand's delivery picture is reused.
const shipmentCacheTTL = 2 * time.Minute

// shipmentErrorTTL is how long a FAILURE is remembered.
//
// Short, but not zero. When Delhivery is rate limiting, the worst possible
// response to somebody reloading the page is another burst of requests at the
// service that just asked us to stop - that is how a minute of throttling
// becomes ten. Fifteen seconds is long enough to break that loop and short
// enough that a transient failure does not strand the page.
const shipmentErrorTTL = 15 * time.Second

// shipmentFetchTimeout bounds one assembly made on behalf of waiting requests.
// Generous because it covers two upstreams and up to fifteen carrier calls.
const shipmentFetchTimeout = 60 * time.Second

// shipmentSnapshot is everything the handler needs to answer, for one brand and
// one window, with no filtering applied. The filter is pure and runs per
// request, which is what makes a chip click free.
type shipmentSnapshot struct {
	Shop          string
	Rows          []shipmentRowResponse
	Counts        map[string]int
	Actionable    int
	Shipped       int
	Tracked       int
	OtherCarriers map[string]int
	At            time.Time
}

type cachedShipments struct {
	snapshot *shipmentSnapshot
	err      error
	expiry   time.Time
}

// shipmentCache is safe for concurrent use. Bounded by (brands x windows),
// which is single digits, so there is no sweeper.
type shipmentCache struct {
	mu  sync.Mutex
	ttl time.Duration
	// errorTTL is deliberately separate; see the constant above.
	errorTTL time.Duration
	now      func() time.Time

	entries   map[string]cachedShipments
	inflights map[string]*inflight[*shipmentSnapshot]
}

func newShipmentCache(ttl, errorTTL time.Duration) *shipmentCache {
	return &shipmentCache{
		ttl: ttl, errorTTL: errorTTL, now: time.Now,
		entries:   make(map[string]cachedShipments),
		inflights: make(map[string]*inflight[*shipmentSnapshot]),
	}
}

func shipmentCacheKey(brandSlug string, days int) string {
	return brandSlug + "|" + strconv.Itoa(days)
}

func (c *shipmentCache) ttlFor(err error) time.Duration {
	if err != nil {
		return c.errorTTL
	}
	return c.ttl
}

// get returns a brand's delivery picture, assembling it only when the cached
// one has expired - and only once, however many requests arrive at that moment.
//
// `refresh` skips the cached value but still joins an assembly already running:
// somebody pressing refresh wants a fresh answer, not a second burst at a
// carrier that is already being read.
func (c *shipmentCache) get(
	ctx context.Context,
	key string,
	refresh bool,
	assemble func(context.Context) (*shipmentSnapshot, error),
) (*shipmentSnapshot, error) {
	if c.ttl <= 0 {
		return assemble(ctx)
	}

	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && !refresh && c.now().Before(entry.expiry) {
		c.mu.Unlock()
		return entry.snapshot, entry.err
	}
	if call, ok := c.inflights[key]; ok {
		c.mu.Unlock()
		return waitFor(ctx, call)
	}
	call := &inflight[*shipmentSnapshot]{done: make(chan struct{})}
	c.inflights[key] = call
	c.mu.Unlock()

	obs.FromContext(ctx).Debug("shipments fetch", "key", key, "refresh", refresh)
	// Detached for the same reason bambu_cache.go detaches, but on its own
	// timeout: detach() carries BambuBuddy's 12s, and this assembly legitimately
	// takes longer. A caller who navigates away must not cancel work that other
	// requests are already waiting on, and the result is worth keeping either way.
	fetchCtx, cancelFetch := context.WithTimeout(
		context.WithoutCancel(ctx), shipmentFetchTimeout)
	snapshot, err := assemble(fetchCtx)
	cancelFetch()

	c.mu.Lock()
	c.entries[key] = cachedShipments{
		snapshot: snapshot, err: err, expiry: c.now().Add(c.ttlFor(err)),
	}
	delete(c.inflights, key)
	c.mu.Unlock()

	call.value, call.err = snapshot, err
	close(call.done)
	return snapshot, err
}
