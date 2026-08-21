package httpapi

// A small TTL cache over the two BambuBuddy reads that sit on hot paths.
//
// It exists because live status became something the UI polls rather than
// something a page fetched once. Without it, every viewer multiplied the load:
// GET /machine-fleet/:id/live costs TWO upstream calls - ListPrinters, purely to
// translate a serial number into a printer id, then GetStatus - so three tabs
// watching three machines at a 10s poll is 108 requests a minute to a service
// running on a laptop at the end of a home internet connection.
//
// Caching policy lives here rather than in internal/integrations/bambubuddy for
// the same reason internal/auth/freshness.go keeps the permission cache out of
// the store: the client stays pure transport, and the policy sits next to the
// handlers that need it. Both fleet_machine_live.go and fleet_machine_camera.go
// resolve printer ids, and both get this for free.

import (
	"context"
	"sync"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// bambuFetchTimeout bounds one upstream call made on behalf of waiting
// requests. Slightly above the client's own 10s timeout so the client's error
// surfaces rather than this deadline masking it.
const bambuFetchTimeout = 12 * time.Second

type cachedIndex struct {
	bySerial map[string]int
	err      error
	expiry   time.Time
}

type cachedStatus struct {
	status bambubuddy.Status
	err    error
	expiry time.Time
}

// inflight is one upstream call that later arrivals wait on instead of making
// their own. Generic so the index and the status paths share one implementation.
type inflight[T any] struct {
	done  chan struct{}
	value T
	err   error
}

// bambuCache caches the serial->printer-id map and per-printer status.
//
// Safe for concurrent use. Both maps are bounded by the size of the fleet -
// single digits - so there is no sweeper: unlike the per-user permission cache
// this cannot grow with traffic.
type bambuCache struct {
	mu sync.Mutex

	indexTTL  time.Duration
	statusTTL time.Duration
	errorTTL  time.Duration
	now       func() time.Time

	index         cachedIndex
	indexInflight *inflight[map[string]int]

	statuses        map[int]cachedStatus
	statusInflights map[int]*inflight[bambubuddy.Status]
}

func newBambuCache(indexTTL, statusTTL, errorTTL time.Duration) *bambuCache {
	return &bambuCache{
		indexTTL: indexTTL, statusTTL: statusTTL, errorTTL: errorTTL,
		now:             time.Now,
		statuses:        make(map[int]cachedStatus),
		statusInflights: make(map[int]*inflight[bambubuddy.Status]),
	}
}

// ttlFor picks the lifetime for an entry: a failure is remembered only briefly.
//
// Negative caching matters more than it looks. An unreachable printer costs a
// full 10s client timeout, and without this every poll from every viewer pays
// it again - the single-flight only collapses arrivals that overlap, and
// staggered tabs do not overlap.
func (c *bambuCache) ttlFor(err error, ok time.Duration) time.Duration {
	if err != nil {
		return c.errorTTL
	}
	return ok
}

// printerID resolves a machine's serial number to BambuBuddy's printer id,
// consulting the cached index first.
//
// Returns -1 with a nil error when the fleet is reachable but has no printer
// with that serial, matching bambuPrinterIDFor's original contract.
func (c *bambuCache) printerID(
	ctx context.Context,
	machineCode string,
	load func(context.Context) ([]bambubuddy.Printer, error),
) (int, error) {
	index, err := c.printerIndex(ctx, load)
	if err != nil {
		return -1, err
	}
	if id, ok := index[machineCode]; ok {
		return id, nil
	}
	return -1, nil
}

func (c *bambuCache) printerIndex(
	ctx context.Context,
	load func(context.Context) ([]bambubuddy.Printer, error),
) (map[string]int, error) {
	if c.indexTTL <= 0 {
		return buildPrinterIndex(load(ctx))
	}

	c.mu.Lock()
	if c.index.bySerial != nil || c.index.err != nil {
		if c.now().Before(c.index.expiry) {
			defer c.mu.Unlock()
			return c.index.bySerial, c.index.err
		}
	}
	if call := c.indexInflight; call != nil {
		c.mu.Unlock()
		return waitFor(ctx, call)
	}
	call := &inflight[map[string]int]{done: make(chan struct{})}
	c.indexInflight = call
	c.mu.Unlock()

	obs.FromContext(ctx).Debug("bambubuddy fetch", "kind", "printers")
	fetchCtx, cancelFetch := detach(ctx)
	index, err := buildPrinterIndex(load(fetchCtx))
	cancelFetch()

	c.mu.Lock()
	c.index = cachedIndex{bySerial: index, err: err, expiry: c.now().Add(c.ttlFor(err, c.indexTTL))}
	c.indexInflight = nil
	c.mu.Unlock()

	call.value, call.err = index, err
	close(call.done)
	return index, err
}

// status returns one printer's live state, from cache when warm.
func (c *bambuCache) status(
	ctx context.Context,
	printerID int,
	load func(context.Context, int) (bambubuddy.Status, error),
) (bambubuddy.Status, error) {
	if c.statusTTL <= 0 {
		return load(ctx, printerID)
	}

	c.mu.Lock()
	if entry, ok := c.statuses[printerID]; ok && c.now().Before(entry.expiry) {
		c.mu.Unlock()
		return entry.status, entry.err
	}
	if call, ok := c.statusInflights[printerID]; ok {
		c.mu.Unlock()
		return waitFor(ctx, call)
	}
	call := &inflight[bambubuddy.Status]{done: make(chan struct{})}
	c.statusInflights[printerID] = call
	c.mu.Unlock()

	obs.FromContext(ctx).Debug("bambubuddy fetch", "kind", "status", "printer", printerID)
	fetchCtx, cancelFetch := detach(ctx)
	st, err := load(fetchCtx, printerID)
	cancelFetch()

	c.mu.Lock()
	c.statuses[printerID] = cachedStatus{
		status: st, err: err, expiry: c.now().Add(c.ttlFor(err, c.statusTTL)),
	}
	delete(c.statusInflights, printerID)
	c.mu.Unlock()

	call.value, call.err = st, err
	close(call.done)
	return st, err
}

// putIndex and putStatus let the fleet sync seed what it already fetched.
//
// The sync calls the client directly - it is the writer of truth and must never
// read through a cache it fills - but sharing its results costs nothing and
// means a manual Sync makes a newly added printer visible immediately rather
// than after the index TTL.
func (c *bambuCache) putIndex(printers []bambubuddy.Printer) {
	if c.indexTTL <= 0 {
		return
	}
	index, err := buildPrinterIndex(printers, nil)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.index = cachedIndex{bySerial: index, err: err, expiry: c.now().Add(c.indexTTL)}
}

func (c *bambuCache) putStatus(printerID int, st bambubuddy.Status) {
	if c.statusTTL <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[printerID] = cachedStatus{status: st, expiry: c.now().Add(c.statusTTL)}
}

// waitFor blocks until the in-flight call finishes, or the caller gives up.
//
// The caller abandoning must not abandon the others: the fetch itself runs on a
// detached context (see detach), so one viewer navigating away cannot cancel
// the call every other waiter is blocked on.
func waitFor[T any](ctx context.Context, call *inflight[T]) (T, error) {
	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// detach runs an upstream call on a context that outlives the request that
// happened to trigger it.
//
// The request context carries the logger and request id, which is worth
// keeping, but its cancellation belongs to one browser tab. With many viewers
// sharing a single fetch, honouring that cancellation would let whichever tab
// arrived first kill the call the rest are waiting on. The caller must defer
// the returned cancel.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), bambuFetchTimeout)
}

// buildPrinterIndex maps serial number -> printer id, using the same fleetCode
// fallback the sync uses so both sides agree on the join key.
func buildPrinterIndex(printers []bambubuddy.Printer, err error) (map[string]int, error) {
	if err != nil {
		return nil, err
	}
	index := make(map[string]int, len(printers))
	for _, p := range printers {
		index[fleetCode(p)] = p.ID
	}
	return index, nil
}
