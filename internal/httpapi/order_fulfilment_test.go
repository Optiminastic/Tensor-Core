package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// A fulfilled order's work is finished: its jobs move to Done and come off the
// bed, so a plank that has already been posted stops occupying space a live
// order needs.
func TestIntegrationFulfilledOrderClosesOutOfProduction(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	ctx := context.Background()

	orderID := seedOrder(t, store, 9301, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	if len(jobs) != 1 {
		t.Fatalf("from-order jobs = %d, want 1", len(jobs))
	}
	jobID := uuid.MustParse(jobs[0].ID)
	fileID := seedFileAsset(t, store, 50, 50, 20)
	givePrintFile(t, store, jobID, fileID)

	// Put it on a Draft bed, which is what the planner would do.
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	if rr := doJSON(router, http.MethodPost, "/batches/auto-create", manage, nil); rr.Code != http.StatusOK {
		t.Fatalf("auto-create = %d body=%s", rr.Code, rr.Body.String())
	}
	before, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("load job: %v", err)
	}
	if before.BatchID == nil {
		t.Fatal("fixture job was not batched, so this test would prove nothing")
	}

	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	fulfilled := "fulfilled"
	order.FulfillmentStatus = &fulfilled

	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	if closed := srv.reconcileFulfilledOrder(ctx, order); closed != 1 {
		t.Fatalf("closed %d jobs, want 1", closed)
	}

	after, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if after.Status != production.StatusCompleted {
		t.Errorf("job status = %q, want %q so it shows under Done", after.Status, production.StatusCompleted)
	}
	if after.BatchID != nil {
		t.Error("job is still on a bed; a posted plank must not hold bed space")
	}
}

// A bed that is printing is a record of what physically happened. Completing a
// job on it is right - the parcel shipped - but quietly removing it from the
// batch would make that record a lie.
func TestIntegrationFulfilledOrderLeavesACommittedBedIntact(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	ctx := context.Background()

	orderID := seedOrder(t, store, 9302, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	jobID := uuid.MustParse(jobs[0].ID)
	fileID := seedFileAsset(t, store, 50, 50, 20)
	givePrintFile(t, store, jobID, fileID)

	machineID := seedMachine(t, store, "P2S-01")
	batchID := seedDraftBatch(t, store, "BATCH-PRINTING", production.BatchInProgress, &machineID, "P2S")
	if err := store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: ptr(batchID), JobIds: []uuid.UUID{jobID},
	}); err != nil {
		t.Fatalf("assign job to the printing batch: %v", err)
	}

	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	fulfilled := "fulfilled"
	order.FulfillmentStatus = &fulfilled

	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	srv.reconcileFulfilledOrder(ctx, order)

	after, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if after.Status != production.StatusCompleted {
		t.Errorf("job status = %q, want completed", after.Status)
	}
	if after.BatchID == nil || *after.BatchID != batchID {
		t.Error("the job was taken off a bed that is already printing; that bed's " +
			"membership is a record of what went on the machine")
	}
}

// Partially fulfilled is NOT fulfilled. Part of an order having shipped says
// nothing about the plank still on a bed, and cancelling it would throw away
// work somebody is waiting for.
func TestIntegrationPartiallyFulfilledOrderIsLeftAlone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	ctx := context.Background()

	orderID := seedOrder(t, store, 9303, []map[string]any{
		{"product_id": "SKU1", "product_name": "Cube", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	jobID := uuid.MustParse(jobs[0].ID)

	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	partial := "partially_fulfilled"
	order.FulfillmentStatus = &partial

	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	if closed := srv.reconcileFulfilledOrder(ctx, order); closed != 0 {
		t.Fatalf("closed %d jobs on a partially fulfilled order, want 0", closed)
	}

	after, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if after.Status != production.StatusQueued {
		t.Errorf("job status = %q, want it still queued", after.Status)
	}
}

// A shipped plank must leave the PLATE, not just the bed.
//
// Recomputing the numbers is not enough: the merged 3MF still holds the plank's
// geometry, and for a bed already handed to BambuBuddy that plate is queued in
// front of a printer. Left alone, the machine prints the plank that is already
// with the customer.
func TestIntegrationFulfilledOrderClearsTheBedsStalePlate(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	ctx := context.Background()

	shipped := seedOrder(t, store, 9601, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	waiting := seedOrder(t, store, 9602, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	shippedJob := uuid.MustParse(fromOrderJobs(t, router, minter, shipped)[0].ID)
	waitingJob := uuid.MustParse(fromOrderJobs(t, router, minter, waiting)[0].ID)

	machineID := seedMachine(t, store, "FULFIL-P2S-01")
	batchID := seedDraftBatch(t, store, "BATCH-FULFIL-PLATE", production.BatchOpen, &machineID, "P2S")
	if _, err := store.Pool.Exec(ctx, `DELETE FROM production_jobs WHERE batch_id = $1 AND id NOT IN ($2, $3)`,
		batchID, shippedJob, waitingJob); err != nil {
		t.Fatalf("clear the seeded job: %v", err)
	}
	if err := store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: ptr(batchID), JobIds: []uuid.UUID{shippedJob, waitingJob},
	}); err != nil {
		t.Fatalf("put the jobs on the bed: %v", err)
	}
	// As if approved, plated and sent.
	plateID := seedFileAsset(t, store, 200, 50, 40)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET merged_file_id = $2, preview_file_id = $2, queue_item_id = 71,
		        plate_sliced_at = now() WHERE id = $1`, batchID, plateID); err != nil {
		t.Fatalf("mark the bed sent: %v", err)
	}

	order, err := store.Q.GetOrderByID(ctx, shipped)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	fulfilled := "fulfilled"
	order.FulfillmentStatus = &fulfilled

	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	srv.reconcileFulfilledOrder(ctx, order)

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.MergedFileID != nil && *batch.MergedFileID == plateID {
		t.Error("the bed still points at the plate holding the shipped plank; that plate " +
			"would print it again")
	}
	if batch.QueueItemID != nil {
		t.Error("the bed still points at its BambuBuddy queue item - the queued plate " +
			"contains a plank already with the customer")
	}
	if batch.PlateSlicedAt.Valid {
		t.Error("the bed still claims a sliced plate; that slice measured the shipped plank")
	}

	// The plank that has not shipped stays.
	left, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		t.Fatalf("read the bed: %v", err)
	}
	if len(left) != 1 || left[0].ID != waitingJob {
		t.Errorf("bed holds %d job(s), want only the one still to print", len(left))
	}
}

// A LOCKED bed whose every plank has shipped is finished, not deleted. It was
// committed - it has a plate, a machine and a number people have quoted - and
// deleting it would make a bed somebody watched simply vanish.
func TestIntegrationABedEmptiedByFulfilmentIsMarkedDone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	ctx := context.Background()

	orderID := seedOrder(t, store, 9603, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	jobID := uuid.MustParse(fromOrderJobs(t, router, minter, orderID)[0].ID)

	machineID := seedMachine(t, store, "FULFIL-P2S-02")
	batchID := seedDraftBatch(t, store, "BATCH-FULFIL-EMPTY", production.BatchOpen, &machineID, "P2S")
	if _, err := store.Pool.Exec(ctx, `DELETE FROM production_jobs WHERE batch_id = $1`, batchID); err != nil {
		t.Fatalf("clear the seeded job: %v", err)
	}
	if err := store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: ptr(batchID), JobIds: []uuid.UUID{jobID},
	}); err != nil {
		t.Fatalf("put the job on the bed: %v", err)
	}

	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("load order: %v", err)
	}
	fulfilled := "fulfilled"
	order.FulfillmentStatus = &fulfilled

	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	srv.reconcileFulfilledOrder(ctx, order)

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("the emptied bed was deleted; a locked bed must be marked done instead: %v", err)
	}
	if batch.Status != production.BatchCompleted {
		t.Errorf("bed status = %q, want %q so it lands under the Completed filter",
			batch.Status, production.BatchCompleted)
	}
}
