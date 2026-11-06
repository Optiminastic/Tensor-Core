package workerset

// Which process consumes what.
//
// These are cheap assertions about three maps, and they exist because the
// failure they guard against is silent. A queue nobody consumes does not
// error: jobs pile up, the screens look normal, and the first sign is an
// operator asking why nothing has batched since yesterday.

import (
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestTheThreeHalvesDoNotOverlap(t *testing.T) {
	// If a queue appeared in two, the pipeline would keep working it after
	// RUN_PERSONALISATION_WORKERS=false or RUN_BATCH_WORKERS=false and the
	// separation would be a lie.
	sets := map[string]map[string]bool{}
	add := func(owner string, names []string) {
		for _, n := range names {
			if sets[n] == nil {
				sets[n] = map[string]bool{}
			}
			sets[n][owner] = true
		}
	}
	add("production", keys(productionQueues(config.Settings{}, 1)))
	add("personalisation", keys(personalisationQueues()))
	add("batch", keys(batchQueues(time.Minute)))
	for name, owners := range sets {
		if len(owners) > 1 {
			t.Errorf("%s is consumed by %v; the split does not hold", name, owners)
		}
	}
}

func TestTheCustomersQueuesAreTheOnesOnAClock(t *testing.T) {
	customer := personalisationQueues()

	// The link send AND the window sweep ride this queue - the sweep is what
	// releases a held order, so it must not sit behind the floor's work.
	if _, ok := customer[production.PersonalisationLinkQueueName]; !ok {
		t.Error("the link queue is not in the personalisation half")
	}
	// The render the customer is watching while their timer runs.
	if _, ok := customer[production.PersonalisationPreviewQueueName]; !ok {
		t.Error("the preview queue is not in the personalisation half")
	}
	if len(customer) != 2 {
		t.Errorf("personalisation owns %d queues; keep this set small and deliberate", len(customer))
	}
}

func TestTheBedsWholeLifecycleIsOneWorker(t *testing.T) {
	bed := batchQueues(time.Minute)
	// Plan (Draft, lock), dispatch (to a printer), pin (to the printer
	// chosen) and the fleet sync that notices a print finished. Any one of
	// them left elsewhere is half a state machine in each process.
	for _, name := range []string{
		production.BatchPlanQueueName,
		production.DispatchBatchQueueName,
		production.QueuePinQueueName,
		production.FleetSyncQueueName,
	} {
		if _, ok := bed[name]; !ok {
			t.Errorf("%s is not in the batch worker", name)
		}
	}
	// FLEET_SYNC_INTERVAL_SECONDS=0 registers no fleet worker, so its queue
	// must not be consumed either.
	if _, ok := batchQueues(0)[production.FleetSyncQueueName]; ok {
		t.Error("fleet sync consumed with no fleet worker registered")
	}
}

func TestTheFloorKeepsItsOwnWork(t *testing.T) {
	floor := productionQueues(config.Settings{}, 1)

	// Model generation stays with the floor on purpose: it renders what gets
	// PRINTED, and moving it would carry the floor's render load into the
	// process that must answer a customer inside thirty minutes.
	for _, name := range []string{
		production.JobCreationQueueName,
		production.ModelGenQueueName,
		production.OrderSyncQueueName,
	} {
		if _, ok := floor[name]; !ok {
			t.Errorf("%s left the production half", name)
		}
	}
}

func TestConcurrencyIsNeverZero(t *testing.T) {
	// A queue configured with no workers is consumed by nobody, which is the
	// same silent failure as leaving it out of the map altogether.
	all := map[string]map[string]int{
		"production":      workersOf(productionQueues(config.Settings{}, 1)),
		"personalisation": workersOf(personalisationQueues()),
		"batch":           workersOf(batchQueues(time.Minute)),
	}
	for owner, qs := range all {
		for name, n := range qs {
			if n < 1 {
				t.Errorf("%s queue %s has %d workers", owner, name, n)
			}
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func workersOf(m map[string]river.QueueConfig) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v.MaxWorkers
	}
	return out
}
