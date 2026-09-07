package httpapi

// WHEN a bed gets its machine.
//
// Planning used to score the whole fleet as it created a bed - loaded material,
// loaded colours, queue depth, health, idle recency - and stamp the winner onto
// the Draft. That made how beds are FORMED depend on which printers happened to
// be free, using state already stale by the time anyone approved it.
//
// The rule is now the shop's own: four planks of one colour on a bed, and
// nothing about machines. The choice moves to approval, which is the moment the
// plate is actually merged, the filament reserved and the slice queued.
//
// The first two tests are the halves of that sentence, and neither is
// meaningful without the other: without the first, planning could quietly start
// reading the fleet again; without the second, beds would never reach a printer
// at all. The third pins what approval says when the fleet has nothing to give.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// A planned bed carries no machine, even when the fleet is idle and would
// happily have taken it.
func TestIntegrationPlanningLeavesTheMachineUnassigned(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	// An idle, online printer of the family generated planks are built for. The
	// old code would have found and stamped it.
	seedFleetMachineWithProfile(t, store, "PLAN-A2L-1", "A2L", "online")

	orderID := seedOrder(t, store, 9101, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	fileID := seedFileAsset(t, store, 50, 50, 20)
	for _, j := range fromOrderJobs(t, router, minter, orderID) {
		givePrintFile(t, store, uuid.MustParse(j.ID), fileID)
	}

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp autoCreateResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Created) != 1 {
		t.Fatalf("auto-create created %d batches, want 1", len(resp.Created))
	}

	batch, err := store.Q.GetBatchByID(context.Background(), uuid.MustParse(resp.Created[0].ID))
	if err != nil {
		t.Fatalf("load the created batch: %v", err)
	}
	if batch.MachineID != nil {
		t.Errorf("a freshly planned bed was stamped with machine %v; planning must not read the "+
			"fleet at all - the machine is chosen at approval", *batch.MachineID)
	}
}

// ...and approval finds one, so a bed with no machine still reaches a printer.
//
// This is the half that stops the change above from silently stalling the
// floor: lockFullBatches approves with a nil machine, so if approval could not
// choose one, no bed would ever lock.
func TestIntegrationApprovalAssignsTheMachine(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	// One online printer for approval to find.
	seedFleetMachineWithProfile(t, store, "APPROVE-A2L-1", "A2L", "online")

	// A Draft with no machine, exactly as planning now leaves it.
	batchID := seedDraftBatch(t, store, "BATCH-NOMACHINE", production.BatchPendingApproval, nil, "A2L")

	before, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("load the draft: %v", err)
	}
	if before.MachineID != nil {
		t.Fatalf("the seeded draft already has a machine; this test needs one without")
	}

	// nil machine, as lockFullBatches and the dispatcher both pass.
	//
	// This test server has no object storage, so approval cannot run to
	// completion - it stops when it tries to merge the plate. That stopping
	// point is the assertion: reaching the plate means the machine was chosen,
	// because selection happens before it and would otherwise have returned
	// "No machine can take this batch yet." The same "got far enough to fail on
	// storage" idiom the batch-edit tests use.
	_, err = srv.ApproveBatchFor(ctx, batchID, nil, systemActor)
	if err == nil {
		t.Fatal("approval unexpectedly completed without object storage")
	}
	if strings.Contains(err.Error(), "No machine can take this batch") {
		t.Fatalf("approval could not choose a machine for a machineless draft: %v - "+
			"lockFullBatches approves with nil, so no bed would ever lock", err)
	}
	if !strings.Contains(err.Error(), "Object storage is not configured") {
		t.Fatalf("approval failed for an unexpected reason: %v", err)
	}
}

// With no machine on the batch AND none the fleet can offer, approval says so
// plainly rather than reporting the old "provide a machine_id" - nothing is
// asking the caller for one any more.
func TestIntegrationApprovalWithNoMachineAvailableSaysSo(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	// No fleet machines seeded at all.
	batchID := seedDraftBatch(t, store, "BATCH-NOFLEET", production.BatchPendingApproval, nil, "A2L")

	_, err := srv.ApproveBatchFor(ctx, batchID, nil, systemActor)
	if err == nil {
		t.Fatal("approval succeeded with no machine in the fleet")
	}
	if !strings.Contains(err.Error(), "No machine can take this batch") {
		t.Errorf("approval error = %v, want it to say no machine can take the batch", err)
	}
}
