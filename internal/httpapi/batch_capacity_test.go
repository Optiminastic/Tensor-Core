package httpapi

// The unit cap has to count what is being ADDED, not only what is already on
// the bed.
//
// addJobsToBatch asked bedIsFull before the add and nothing after it, so a bed
// one short of the cap accepted any number of jobs - or one job of quantity ten
// - and went to a printer holding more planks than a plate has places. The area
// check beside it cannot catch this: five 200x50 planks cover 52% of the bed, so
// the 80% utilisation gate never fires under colour batching.
//
// The fixture is built from production.MaxBedUnits rather than from a written-in
// number. It used to seed three jobs and call that "one place free", which was
// true only while the cap was four; when the cap became five the bed had two
// free places, the add the test expected to be refused was legitimately
// accepted, and the failure read as a capacity bug that did not exist.
//
// These use the "validation passed, then storage refused it" idiom the
// batch-edit tests use: this server has no object storage, so an add that gets
// as far as rebuilding the plate reports 503. A 422 means it was refused on the
// rule under test; a 503 means the rule let it through.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestIntegrationAddJobsCountsTheUnitsBeingAdded(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	ctx := context.Background()

	fileID := seedFileAsset(t, store, 50, 50, 20)
	cfg := func(batch *uuid.UUID, qty int32) jobConfig {
		return jobConfig{
			batchID: batch, material: "PLA", colour: "BLUE", leftNozzleMm: 0.4,
			machineFamily: "H2C", printFileID: &fileID, quantity: qty,
		}
	}

	// A bed with exactly one place left, whatever the cap happens to be.
	b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-CAP-1",
		Status: production.BatchPendingApproval, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	for i := range production.MaxBedUnits - 1 {
		seedConfiguredJob(t, store, fmt.Sprintf("BATCH-CAP-1-%d", i), cfg(&b.ID, 1))
	}

	add := func(t *testing.T, ids ...uuid.UUID) int {
		t.Helper()
		raw := make([]string, 0, len(ids))
		for _, id := range ids {
			raw = append(raw, id.String())
		}
		rr := doJSON(router, http.MethodPost, "/batches/"+b.ID.String()+"/jobs", manage,
			map[string]any{"job_ids": raw})
		return rr.Code
	}

	// Two single-unit jobs: one place free, two asked for.
	twoA := seedConfiguredJob(t, store, "BATCH-CAP-1-D", cfg(nil, 1))
	twoB := seedConfiguredJob(t, store, "BATCH-CAP-1-E", cfg(nil, 1))
	if code := add(t, twoA, twoB); code != http.StatusUnprocessableEntity {
		t.Errorf("adding two jobs to a bed with one free place = %d, want 422", code)
	}

	// One job of quantity two. The old check counted jobs, not products, so
	// this passed even though it fills two places.
	qtyTwo := seedConfiguredJob(t, store, "BATCH-CAP-1-F", cfg(nil, 2))
	if code := add(t, qtyTwo); code != http.StatusUnprocessableEntity {
		t.Errorf("adding one job of quantity 2 to a bed with one free place = %d, want 422", code)
	}

	// One single-unit job exactly fills the bed: allowed through the rule, then
	// refused for want of object storage.
	fits := seedConfiguredJob(t, store, "BATCH-CAP-1-G", cfg(nil, 1))
	if code := add(t, fits); code != http.StatusServiceUnavailable {
		t.Errorf("adding one job to a bed with one free place = %d, want 503 "+
			"(the cap allowed it; this environment has no storage)", code)
	}
}
