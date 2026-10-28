package workerset

// Which process consumes what.
//
// These are cheap assertions about two maps, and they exist because the
// failure they guard against is silent. A queue nobody consumes does not
// error: jobs pile up, the screens look normal, and the first sign is an
// operator asking why nothing has batched since yesterday.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestTheTwoHalvesDoNotOverlap(t *testing.T) {
	// If a queue appeared in both, the pipeline would keep working it after
	// RUN_PERSONALISATION_WORKERS=false and the separation would be a lie.
	floor := productionQueues(config.Settings{}, 1)
	customer := personalisationQueues()

	for name := range customer {
		if _, both := floor[name]; both {
			t.Errorf("%s is consumed by both halves; the split does not hold", name)
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

func TestTheFloorKeepsItsOwnWork(t *testing.T) {
	floor := productionQueues(config.Settings{}, 1)

	// Model generation stays with the floor on purpose: it renders what gets
	// PRINTED, and moving it would carry the floor's render load into the
	// process that must answer a customer inside thirty minutes.
	for _, name := range []string{
		production.JobCreationQueueName,
		production.BatchPlanQueueName,
		production.ModelGenQueueName,
		production.DispatchBatchQueueName,
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
	for name, qc := range productionQueues(config.Settings{}, 1) {
		if qc.MaxWorkers < 1 {
			t.Errorf("production queue %s has %d workers", name, qc.MaxWorkers)
		}
	}
	for name, qc := range personalisationQueues() {
		if qc.MaxWorkers < 1 {
			t.Errorf("personalisation queue %s has %d workers", name, qc.MaxWorkers)
		}
	}
}
