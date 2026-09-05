package httpapi

// Recording that a list of orders printed.
//
// The two outcomes that matter are the two halves of the floor's ask: a bed
// whose whole contents printed is finished, and a bed that printed only partly
// must go back to being fillable - otherwise it sits at two of four places for
// ever, because a locked bed has left the planning pool.

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The store writes "T3DPS-114762" and the floor writes "114762". The prefix
// itself contains a digit, which is why this takes the trailing run rather than
// every digit in the string.
func TestOrderNumberKeyTakesTheTrailingDigits(t *testing.T) {
	for in, want := range map[string]string{
		"T3DPS-114762": "114762",
		"114762":       "114762",
		"#TEST0001":    "0001",
		"no-digits":    "",
		"":             "",
	} {
		if got := orderNumberKey(in); got != want {
			t.Errorf("orderNumberKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A bed whose every plank printed is Done, and its jobs are completed - which is
// what puts them in front of Assembly, QC and Packaging.
func TestIntegrationMarkOrdersPrintedFinishesAFullyPrintedBed(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	orderID := seedOrder(t, store, 9401, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	jobs := fromOrderJobs(t, router, minter, orderID)
	jobID := uuid.MustParse(jobs[0].ID)
	machineID := seedMachine(t, store, "PRINTED-P2S-01")
	batchID := seedDraftBatch(t, store, "BATCH-PRINTED-FULL", production.BatchOpen, &machineID, "P2S")
	assignJobsToBatch(t, store, batchID, jobID)
	// seedDraftBatch seeds a job of its own; both are on the order's list only
	// if they share it, so clear the other one off the bed first.
	if _, err := store.Pool.Exec(ctx,
		`DELETE FROM production_jobs WHERE batch_id = $1 AND id <> $2`, batchID, jobID); err != nil {
		t.Fatalf("clear the seeded job: %v", err)
	}

	out, err := srv.MarkOrdersPrinted(ctx, []string{printedNumber(9401)})
	if err != nil {
		t.Fatalf("mark printed: %v", err)
	}
	if len(out.Missing) != 0 {
		t.Fatalf("missing = %v, want none", out.Missing)
	}
	if out.JobsCompleted != 1 {
		t.Errorf("jobs completed = %d, want 1", out.JobsCompleted)
	}
	if len(out.BatchesCompleted) != 1 {
		t.Errorf("beds completed = %v, want the one bed", out.BatchesCompleted)
	}

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.Status != production.BatchCompleted {
		t.Errorf("bed status = %q, want %q so it lands under the Completed filter",
			batch.Status, production.BatchCompleted)
	}
	job, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != production.StatusCompleted {
		t.Errorf("job status = %q, want completed", job.Status)
	}
	// The finished bed must still know what was on it. The Completed list reads
	// its orders and colours from the jobs that point at it, so a bed whose jobs
	// were detached shows a row of dashes - no orders, no colours, nothing to
	// tell one finished plate from another.
	if job.BatchID == nil || *job.BatchID != batchID {
		t.Error("the completed job was detached from its bed; the Completed list " +
			"would show no orders and no colours for it")
	}
	held, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		t.Fatalf("read the finished bed: %v", err)
	}
	if len(held) != 1 {
		t.Errorf("finished bed holds %d job(s), want the 1 that printed on it", len(held))
	}
}

// A bed that printed only partly loses the planks that printed and goes back to
// being a Draft - the only state the planner refills. Everything approval
// produced is cleared with it: a stale queue id reads as "already sent", so the
// refilled bed would never reach a printer.
func TestIntegrationMarkOrdersPrintedReopensAPartlyPrintedBed(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	printed := seedOrder(t, store, 9402, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	waiting := seedOrder(t, store, 9403, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	printedJob := uuid.MustParse(fromOrderJobs(t, router, minter, printed)[0].ID)
	waitingJob := uuid.MustParse(fromOrderJobs(t, router, minter, waiting)[0].ID)

	machineID := seedMachine(t, store, "PRINTED-P2S-02")
	batchID := seedDraftBatch(t, store, "BATCH-PRINTED-PART", production.BatchOpen, &machineID, "P2S")
	assignJobsToBatch(t, store, batchID, printedJob, waitingJob)
	if _, err := store.Pool.Exec(ctx,
		`DELETE FROM production_jobs WHERE batch_id = $1 AND id NOT IN ($2, $3)`,
		batchID, printedJob, waitingJob); err != nil {
		t.Fatalf("clear the seeded job: %v", err)
	}
	// As if it had been approved and sent.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET queue_item_id = 77, filament_reserved = true, plate_sliced_at = now()
		  WHERE id = $1`, batchID); err != nil {
		t.Fatalf("mark the bed sent: %v", err)
	}

	out, err := srv.MarkOrdersPrinted(ctx, []string{printedNumber(9402)})
	if err != nil {
		t.Fatalf("mark printed: %v", err)
	}
	if len(out.BatchesReopened) != 1 {
		t.Fatalf("beds reopened = %v, want the one bed", out.BatchesReopened)
	}

	batch, err := store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		t.Fatalf("reload batch: %v", err)
	}
	if batch.Status != production.BatchPendingApproval {
		t.Errorf("bed status = %q, want %q so the planner can refill it",
			batch.Status, production.BatchPendingApproval)
	}
	if batch.QueueItemID != nil {
		t.Error("the bed still carries a BambuBuddy queue id; the dispatcher reads that as " +
			"already sent, so the refilled bed would never reach a printer")
	}
	if batch.PlateSlicedAt.Valid {
		t.Error("the bed still claims a sliced plate; that plate described its old contents")
	}

	// The printed plank is off the bed and done; the one still waiting stays.
	left, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		t.Fatalf("read the bed: %v", err)
	}
	if len(left) != 1 || left[0].ID != waitingJob {
		t.Errorf("bed holds %d job(s), want only the one that has not printed", len(left))
	}
}

// assignJobsToBatch puts jobs on a bed, the way the planner's commit does.
func assignJobsToBatch(t *testing.T, store *db.Store, batchID uuid.UUID, jobIDs ...uuid.UUID) {
	t.Helper()
	if err := store.Q.AssignJobsToBatch(context.Background(), gen.AssignJobsToBatchParams{
		BatchID: ptr(batchID), JobIds: jobIDs,
	}); err != nil {
		t.Fatalf("assign jobs to the bed: %v", err)
	}
}

// printedNumber is the order number seedOrder actually writes for a seed id -
// what the floor would read off the paperwork.
func printedNumber(shopifyID int) string {
	return fmt.Sprintf("%d", firstProductionOrderNumber+shopifyID)
}
