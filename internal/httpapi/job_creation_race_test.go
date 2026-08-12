package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

// TestIntegrationCreateJobsForOrderIsRaceSafe covers "the same action twice"
// at the order level.
//
// CreateJobsForOrder used to count existing jobs in one transaction and insert
// in another. Under READ COMMITTED - which is what InTx uses - neither
// transaction can see the other's uncommitted inserts, so two concurrent runs
// both counted zero and both inserted a full set of jobs for the order. That is
// not a theoretical race: importShopifyOrder enqueues a create_jobs_from_order
// job on *every* re-sync of the same order, PRODUCTION_CONCURRENCY defaults to
// 5, and the manual backfill endpoint can race either of them.
//
// A unique constraint cannot express this - an order may legitimately contain
// the same SKU twice with different personalisation - so the guard is a
// transaction-scoped advisory lock plus a re-count taken while holding it.
//
// The assertion that matters is the final job count. Exactly one caller must
// win; every other must come back with errJobsAlreadyCreated, which the HTTP
// layer maps to 409 and the River worker acks as success.
func TestIntegrationCreateJobsForOrderIsRaceSafe(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	s := NewServer(config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		CORSOrigins: []string{"http://localhost:3001"},
	}, store, guards, nil)

	orderID := seedOrder(t, store, 14001, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA"},
		{"product_id": "SKU2", "product_name": "Plate", "quantity": 2, "material": "PLA"},
	})

	const racers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		conflict int
		others   []error
	)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, to actually overlap
			_, err := s.CreateJobsForOrder(context.Background(), orderID)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, errJobsAlreadyCreated):
				conflict++
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range others {
		t.Errorf("unexpected error from a racing caller: %v", err)
	}
	if wins != 1 {
		t.Errorf("callers that created jobs = %d, want exactly 1", wins)
	}
	if conflict != racers-1 {
		t.Errorf("callers that saw errJobsAlreadyCreated = %d, want %d", conflict, racers-1)
	}

	count, err := store.Q.CountJobsForOrder(context.Background(), &orderID)
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 2 {
		t.Errorf("production jobs for the order = %d, want 2 (one per line item) - "+
			"more than that means the duplicate-creation race is back", count)
	}
}
