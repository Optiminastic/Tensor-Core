package httpapi

// A locked bed can still be edited, and editing it re-plates what is left.
//
// The point of the whole path: a bed of four with one bad plank used to mean
// deleting the bed and losing the three that were fine, because approval had
// debited filament with no way back and the plate was already built.

import (
	"context"
	"net/http"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// editableBatch is the single rule behind both the API's refusal and the UI's
// hidden buttons, so its four answers are worth pinning down.
func TestEditableBatchAllowsDraftAndLockedOnly(t *testing.T) {
	for status, want := range map[string]bool{
		production.BatchPendingApproval: true,
		production.BatchOpen:            true,
		production.BatchInProgress:      false,
		production.BatchCompleted:       false,
	} {
		got, why := editableBatch(gen.Batch{Status: status})
		if got != want {
			t.Errorf("editableBatch(%q) = %v, want %v", status, got, want)
		}
		if !got && why == "" {
			t.Errorf("editableBatch(%q) refuses without saying why", status)
		}
	}
}

// A locked bed accepts the edit request - the gate lets it through - and when
// the rebuild cannot run, NOTHING is changed.
//
// The test server has no object storage, which is the useful accident here: it
// exercises the promise that an edit either happens completely or not at all. A
// half-applied edit is the dangerous shape - a bed whose plank was removed, whose
// plate was cleared, and whose operator was told it failed.
func TestIntegrationALockedBatchEditIsAllOrNothing(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	ctx := context.Background()

	machineID := seedMachine(t, store, "EDIT-P2S-01")
	batchID := seedDraftBatch(t, store, "BATCH-EDIT-LOCKED", production.BatchOpen, &machineID, "P2S")
	// Two planks, so removing one leaves a bed that still needs a plate. A bed
	// emptied by the removal needs no plate at all and is allowed through
	// without storage - see plateableBatch.
	seedConfiguredJob(t, store, "BATCH-EDIT-LOCKED-J2", jobConfig{
		batchID: &batchID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "P2S",
	})
	queueItem := int32(91)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = $2 WHERE id = $1`, batchID, queueItem); err != nil {
		t.Fatalf("mark the bed sent: %v", err)
	}
	jobs, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("read the seeded bed: %v", err)
	}

	rr := doJSON(router, http.MethodDelete,
		"/batches/"+batchID.String()+"/jobs/"+jobs[0].ID.String(), manage, nil)
	// Not 409: a locked bed is editable now, and the only thing standing in the
	// way here is the missing storage the rebuild needs.
	if rr.Code == http.StatusConflict {
		t.Fatalf("remove from a locked batch = 409 %s; a locked bed must be editable, "+
			"or one bad plank costs the three beside it", rr.Body.String())
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("remove = %d body=%s, want 503 (no object storage in tests)", rr.Code, rr.Body.String())
	}

	after, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		t.Fatalf("re-read the bed: %v", err)
	}
	if len(after) != len(jobs) {
		t.Errorf("bed holds %d job(s) after a refused edit, want the original %d", len(after), len(jobs))
	}
	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.QueueItemID == nil || *batch.QueueItemID != queueItem {
		t.Error("a refused edit cleared the bed's queue item; the plate is still in " +
			"BambuBuddy's queue and the bed no longer knows it")
	}
}

// ClearBatchPlateForEdit drops what described the bed's old contents and leaves
// the bed itself alone. Editing is not unlocking: the operator changed the four
// planks deliberately and does not want the planner rearranging them afterwards.
func TestIntegrationClearBatchPlateForEditKeepsTheBedLocked(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	machineID := seedMachine(t, store, "EDIT-P2S-03")
	batchID := seedDraftBatch(t, store, "BATCH-EDIT-CLEAR", production.BatchOpen, &machineID, "P2S")
	fileID := seedFileAsset(t, store, 200, 50, 40)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = 92, pipeline_run_id = 5, plate_sliced_at = now(),
		        merged_file_id = $2, preview_file_id = $2, print_error = 'stale'
		  WHERE id = $1`, batchID, fileID); err != nil {
		t.Fatalf("stamp the bed as sent: %v", err)
	}

	cleared, err := store.Q.ClearBatchPlateForEdit(ctx, batchID)
	if err != nil {
		t.Fatalf("clear the plate: %v", err)
	}
	if cleared.Status != production.BatchOpen {
		t.Errorf("bed status = %q, want it still locked", cleared.Status)
	}
	for name, set := range map[string]bool{
		"merged_file_id":  cleared.MergedFileID != nil,
		"preview_file_id": cleared.PreviewFileID != nil,
		"queue_item_id":   cleared.QueueItemID != nil,
		"pipeline_run_id": cleared.PipelineRunID != nil,
		"plate_sliced_at": cleared.PlateSlicedAt.Valid,
		"print_error":     cleared.PrintError != nil,
	} {
		if set {
			t.Errorf("%s survived the clear; it describes the bed's previous contents", name)
		}
	}
}

// A printing bed is a plate laying plastic on a machine. There is no version of
// "remove a plank" that reaches it, so the API says so rather than editing
// Tensor into disagreeing with the printer.
func TestIntegrationAPrintingBatchRefusesJobEdits(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	ctx := context.Background()

	machineID := seedMachine(t, store, "EDIT-P2S-02")
	batchID := seedDraftBatch(t, store, "BATCH-EDIT-PRINTING", production.BatchInProgress, &machineID, "P2S")
	jobs, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("read the seeded bed: %v", err)
	}

	rr := doJSON(router, http.MethodDelete,
		"/batches/"+batchID.String()+"/jobs/"+jobs[0].ID.String(), manage, nil)
	if rr.Code != http.StatusConflict {
		t.Errorf("remove from a printing batch = %d, want 409", rr.Code)
	}
}
