package production

// How a pull of every connected store is enqueued.
//
// These two facts are what make it safe for every worker process to tick the
// order sync for ITSELF rather than relying on River's periodic jobs - which
// run on the elected leader alone, and so ran four times in twenty-two hours
// on the live shop while the lease moved between four clients.
//
// If the dedupe window ever stops being the tick interval, K processes
// ticking together stop collapsing into one pull and start stampeding the
// shop's Shopify store instead. That is the failure this pins.

import (
	"testing"
	"time"
)

func TestOrderSyncIsDedupedOverTheTickInterval(t *testing.T) {
	every := 10 * time.Minute
	opts := OrderSyncInsertOpts(every)

	// Its own queue: a pull is up to eight paged GraphQL calls, and sharing a
	// queue with the job-creation work would let one block the other.
	if opts.Queue != OrderSyncQueueName {
		t.Errorf("queue = %q, want %q", opts.Queue, OrderSyncQueueName)
	}
	// THE WHOLE SAFETY OF TICKING EVERYWHERE. Without it, four processes on a
	// ten-minute tick are four pulls of the same store every ten minutes.
	if opts.UniqueOpts.ByPeriod != every {
		t.Errorf("dedupe window = %v, want the tick interval %v", opts.UniqueOpts.ByPeriod, every)
	}
}

func TestOrderSyncPullsEveryConnectedStore(t *testing.T) {
	// The empty BrandSlug is the worker's "all brands". A tick that named one
	// brand would quietly stop importing for every other connected store, and
	// nothing on any screen would say which.
	if (SyncOrdersArgs{}).BrandSlug != "" {
		t.Error("the periodic pull must carry no brand, which is how it means every brand")
	}
	if (SyncOrdersArgs{}).Kind() != "sync_shopify_orders" {
		t.Errorf("kind = %q, which is what River matches a worker on",
			(SyncOrdersArgs{}).Kind())
	}
}
