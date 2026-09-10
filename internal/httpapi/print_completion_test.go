package httpapi

// The loop that was open: BambuBuddy finishes a print, and Tensor finds out.
//
// Every fixture here uses the real shapes from the live archive - the colour
// leads the plate name where plateFileStem puts it last, and most rows carry
// status "archived" rather than "completed".

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bambuFleetStub serves the two endpoints the reconciliation reads.
func bambuFleetStub(t *testing.T, queue, archives string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/queue/":
			_, _ = w.Write([]byte(queue))
		case r.URL.Path == "/api/v1/archives/":
			_, _ = w.Write([]byte(archives))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sentBed is a locked bed that has been handed to BambuBuddy, with a plate
// named the way plateFileStem names one and two jobs on it.
func sentBed(t *testing.T, store *db.Store, number, plate string) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	machineID := seedMachine(t, store, "DONE-"+number)
	batchID := seedDraftBatch(t, store, number, production.BatchOpen, &machineID, "A2L")

	fileID := seedFileAsset(t, store, 200, 50, 40)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE file_assets SET filename = $2 WHERE id = $1`, fileID, plate); err != nil {
		t.Fatalf("name the plate: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET merged_file_id = $2, pipeline_run_id = 77, approved_at = now() - interval '1 hour'
		   WHERE id = $1`, batchID, fileID); err != nil {
		t.Fatalf("mark the bed sent: %v", err)
	}

	var jobs []uuid.UUID
	for _, n := range []string{"JOB-" + number + "-A", "JOB-" + number + "-B"} {
		id := seedConfiguredJob(t, store, n, jobConfig{
			batchID: &batchID, material: "PLA", leftNozzleMm: 0.4,
			machineFamily: "A2L", colour: "GOLD", printFileID: &fileID,
		})
		jobs = append(jobs, id)
	}
	return batchID, jobs
}

func reloadBatch(t *testing.T, store *db.Store, id uuid.UUID) gen.Batch {
	t.Helper()
	b, err := store.Q.GetBatchByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	return b
}

// A finished archive completes its bed and puts the planks in front of Assembly.
//
// The end of the chain the shop actually cares about: completing the bed is the
// ONE thing that sets each job's status to 'completed' while leaving
// assembly_status 'pending', which is exactly the precondition the Assembly
// station's query requires. Before this nothing but a human could do it.
func TestIntegrationAFinishedArchiveCompletesItsBedAndReleasesItsJobs(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	batchID, jobIDs := sentBed(t, store, "BATCH-DONE-1", "114676-114719-GOLD.3mf")

	// The queue no longer holds it - a finished print is archived - and the
	// archive names the same plate the other way round, colour first, which is
	// how the live archive really reports Tensor's plates.
	stub := bambuFleetStub(t, `[]`, `[
		{"id": 42, "print_name": "GOLD-114676-114719", "status": "archived",
		 "printer_id": 4, "actual_time_seconds": 7380, "print_time_seconds": 7000,
		 "total_filament_actual_grams": 214.5, "completed_at": "2030-01-01T10:00:00Z"}
	]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.ReconcileFinishedPrints(ctx)
	if out.Completed != 1 {
		t.Fatalf("completed = %d, want 1 (considered=%d unmatched=%d)",
			out.Completed, out.Considered, out.Unmatched)
	}

	b := reloadBatch(t, store, batchID)
	if b.Status != production.BatchCompleted {
		t.Errorf("batch status = %q, want %q", b.Status, production.BatchCompleted)
	}
	if b.PrintOutcome == nil || *b.PrintOutcome != production.BatchCompleted {
		t.Errorf("print_outcome = %v, want completed", b.PrintOutcome)
	}
	if b.ActualPrintTimeMinutes == nil || *b.ActualPrintTimeMinutes != 123 {
		t.Errorf("actual_print_time_minutes = %v, want 123 (7380s)", b.ActualPrintTimeMinutes)
	}

	// The planks are now Assembly's problem, which is the whole point.
	for _, id := range jobIDs {
		j, err := store.Q.GetProductionJobByID(ctx, id)
		if err != nil {
			t.Fatalf("reload job: %v", err)
		}
		if j.Status != production.StatusCompleted {
			t.Errorf("job %s status = %q, want %q", j.JobNumber, j.Status, production.StatusCompleted)
		}
		if j.AssemblyStatus != production.AssemblyPending {
			t.Errorf("job %s assembly = %q, want %q - it must be waiting for assembly, not past it",
				j.JobNumber, j.AssemblyStatus, production.AssemblyPending)
		}
	}
}

// Running the pass twice completes nothing the second time.
//
// It runs on every fleet sync, so this is the normal case rather than a corner
// one. The guard is a single UPDATE on print_outcome IS NULL, so a second caller
// gets no row back and correctly does nothing.
func TestIntegrationReconcilingTwiceIsHarmless(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)

	sentBed(t, store, "BATCH-DONE-2", "114800-114801-BLUE.3mf")
	stub := bambuFleetStub(t, `[]`, `[
		{"id": 43, "print_name": "BLUE-114800-114801", "status": "completed",
		 "actual_time_seconds": 3600, "completed_at": "2030-01-01T10:00:00Z"}
	]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	if out := srv.ReconcileFinishedPrints(context.Background()); out.Completed != 1 {
		t.Fatalf("first pass completed = %d, want 1", out.Completed)
	}
	if out := srv.ReconcileFinishedPrints(context.Background()); out.Completed != 0 {
		t.Errorf("second pass completed = %d, want 0 - the bed is already done", out.Completed)
	}
}

// A failed print is not finished goods.
//
// The rule that matters most here. The bed keeps its status and gains
// BambuBuddy's own words; the planks stay queued and never reach Assembly; and
// the bed drops out of the automatic dispatcher so it is not immediately sent
// again into whatever went wrong.
func TestIntegrationAFailedPrintReleasesNothing(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	batchID, jobIDs := sentBed(t, store, "BATCH-FAIL-1", "114776-BLACK.stl")

	stub := bambuFleetStub(t, `[]`, `[
		{"id": 3, "print_name": "BLACK-114776", "status": "failed", "printer_id": 4,
		 "failure_reason": "spaghetti detected", "completed_at": "2030-01-01T10:00:00Z"}
	]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.ReconcileFinishedPrints(ctx)
	if out.Failed != 1 || out.Completed != 0 {
		t.Fatalf("failed=%d completed=%d, want 1 and 0", out.Failed, out.Completed)
	}

	b := reloadBatch(t, store, batchID)
	if b.Status == production.BatchCompleted {
		t.Error("a FAILED print completed its bed; nothing about it is finished")
	}
	if b.PrintError == nil || *b.PrintError != "spaghetti detected" {
		t.Errorf("print_error = %v, want BambuBuddy's own wording", b.PrintError)
	}

	for _, id := range jobIDs {
		j, _ := store.Q.GetProductionJobByID(ctx, id)
		if j.Status == production.StatusCompleted {
			t.Errorf("job %s was completed by a failed print", j.JobNumber)
		}
	}

	// And the dispatcher will not pick it up again on its own.
	rows, err := store.Q.ListBatchesToDispatch(ctx)
	if err != nil {
		t.Fatalf("list to dispatch: %v", err)
	}
	for _, r := range rows {
		if r.ID == batchID {
			t.Error("a bed whose print failed is still offered to the automatic " +
				"dispatcher; it would be sent straight back into the same failure")
		}
	}
}

// A plate that names no orders claims no bed.
//
// plateFileStem falls back to the batch number for a bed with no Shopify order
// behind it, and BambuBuddy's own users name plates whatever they like. Either
// could otherwise complete somebody else's bed.
func TestIntegrationAnUnrelatedArchiveCompletesNothing(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)

	batchID, _ := sentBed(t, store, "BATCH-NOMATCH-1", "114900-114901-RED.3mf")
	stub := bambuFleetStub(t, `[]`, `[
		{"id": 2, "print_name": "Fidget Click Keychain - Print in Place", "status": "completed",
		 "completed_at": "2030-01-01T10:00:00Z"},
		{"id": 1, "print_name": "PHOTO_FRIDGE_MAGNET", "status": "archived",
		 "completed_at": "2030-01-01T10:00:00Z"}
	]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.ReconcileFinishedPrints(context.Background())
	if out.Completed != 0 {
		t.Errorf("completed = %d, want 0", out.Completed)
	}
	if out.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 - an unmatched bed must be reported, not ignored", out.Unmatched)
	}
	if b := reloadBatch(t, store, batchID); b.Status == production.BatchCompleted {
		t.Error("an unrelated print completed a bed")
	}
}

// A bed still printing is left alone.
func TestIntegrationABedStillInTheQueueIsNotResolved(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	ctx := context.Background()

	batchID, _ := sentBed(t, store, "BATCH-RUNNING-1", "114950-114951-WHITE.3mf")

	stub := bambuFleetStub(t, `[
		{"id": 88, "status": "printing", "library_file_name": "114950-114951-WHITE.3mf"}
	]`, `[]`)
	srv := testServerWithBambu(t, store, auth.NewGuards(minter.verifier, ""), stub.URL)

	out := srv.ReconcileFinishedPrints(ctx)
	if out.Completed != 0 || out.Failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0 and 0 while it is still printing",
			out.Completed, out.Failed)
	}
	// The queue item is filled in on the way past, which is the bug that kept
	// these beds out of plate measurement for ever.
	if out.Backfilled != 1 {
		t.Errorf("backfilled = %d, want 1", out.Backfilled)
	}
	if b := reloadBatch(t, store, batchID); b.QueueItemID == nil || *b.QueueItemID != 88 {
		t.Errorf("queue_item_id = %v, want 88", b.QueueItemID)
	}
}
