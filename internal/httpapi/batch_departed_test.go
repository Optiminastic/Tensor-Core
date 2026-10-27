package httpapi

// Draft beds holding a job that has left the queue.
//
// A bed can only be approved if every job on it is still queued; the guard in
// unprintableJob refuses otherwise with "A job in this batch is no longer
// queued." Nothing used to remove such a job, and under colour batching the
// planner keeps a Draft it reproduces rather than dissolving it - so the bed
// was retried and refused on every dispatch pass for ever. Measured on
// production 2026-10-27: BATCH-1001248 failed that way every two minutes.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedBareBatch inserts a batch with NO jobs on it. The shared seedDraftBatch
// helper adds one automatically, which would mask exactly what these check.
func seedBareBatch(t *testing.T, store *db.Store, number, status string) uuid.UUID {
	t.Helper()
	b, err := store.Q.InsertBatch(context.Background(), gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: status, MaterialShortage: false,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	return b.ID
}

func jobBatchOf(t *testing.T, store *db.Store, jobID uuid.UUID) *uuid.UUID {
	t.Helper()
	var batch *uuid.UUID
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT batch_id FROM production_jobs WHERE id = $1`, jobID).Scan(&batch); err != nil {
		t.Fatalf("read job: %v", err)
	}
	return batch
}

func batchExists(t *testing.T, store *db.Store, id uuid.UUID) bool {
	t.Helper()
	var n int
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM batches WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	return n > 0
}

func TestADepartedJobIsTakenOffItsDraftBed(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	draft := seedBareBatch(t, store, "BATCH-DEPARTED-1", production.BatchPendingApproval)
	// The wedge: one job has started printing, so the bed can never be
	// approved while it is still on it.
	departed := seedJobOnBatch(t, store, draft, "DEPARTED-J1", production.StatusInProduction)
	// Its bedmate is fine and must stay, so the bed can still print.
	staying := seedJobOnBatch(t, store, draft, "DEPARTED-J2", production.StatusQueued)

	rows, err := store.Q.DetachDepartedJobsFromDrafts(ctx)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("detached %d jobs, want 1", len(rows))
	}
	if rows[0].JobNumber != "DEPARTED-J1" {
		t.Errorf("detached %q, want the job that left the queue", rows[0].JobNumber)
	}
	if jobBatchOf(t, store, departed) != nil {
		t.Error("the departed job is still on the bed, so the bed is still wedged")
	}
	if jobBatchOf(t, store, staying) == nil {
		t.Error("took a queued job off the bed; only departed ones should go")
	}
}

func TestAHeldOrFlaggedJobKeepsItsBed(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// These come back. A hold is lifted, an issue is resolved, a customer
	// answers - and the bed should still be waiting for them rather than
	// having been quietly taken apart.
	draft := seedBareBatch(t, store, "BATCH-DEPARTED-2", production.BatchPendingApproval)
	held := seedJobOnBatch(t, store, draft, "STILL-QUEUED-J1", production.StatusQueued)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET held = true WHERE id = $1`, held); err != nil {
		t.Fatalf("hold job: %v", err)
	}

	rows, err := store.Q.DetachDepartedJobsFromDrafts(ctx)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("detached %d jobs; a held job is still queued and keeps its bed", len(rows))
	}
	if jobBatchOf(t, store, held) == nil {
		t.Error("a held job was taken off its bed")
	}
}

func TestABedLeftEmptyIsRemoved(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// An empty Draft is worse than no bed: every job on it is queued
	// vacuously, so the approval guard passes and dispatch would commit a
	// plate with nothing to print.
	draft := seedBareBatch(t, store, "BATCH-DEPARTED-3", production.BatchPendingApproval)
	seedJobOnBatch(t, store, draft, "EMPTIES-J1", production.StatusCompleted)

	if _, err := store.Q.DetachDepartedJobsFromDrafts(ctx); err != nil {
		t.Fatalf("detach: %v", err)
	}
	gone, err := store.Q.DeleteEmptyDraftBatches(ctx)
	if err != nil {
		t.Fatalf("delete empty: %v", err)
	}
	if len(gone) != 1 {
		t.Fatalf("removed %d beds, want 1", len(gone))
	}
	if batchExists(t, store, draft) {
		t.Error("the emptied bed is still there, and dispatch would try to print it")
	}
}

func TestAnApprovedBedIsNeverTouched(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// Past Draft the plate is sliced and the filament reserved. Changing what
	// is on the bed there would strand a plate a machine is about to print,
	// which is far worse than a wedged Draft.
	locked := seedBareBatch(t, store, "BATCH-DEPARTED-4", production.BatchPendingApproval)
	if _, err := store.Pool.Exec(ctx,
		`UPDATE batches SET status = $2 WHERE id = $1`, locked, production.BatchOpen); err != nil {
		t.Fatalf("lock batch: %v", err)
	}
	onIt := seedJobOnBatch(t, store, locked, "LOCKED-J1", production.StatusInProduction)

	rows, err := store.Q.DetachDepartedJobsFromDrafts(ctx)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("detached %d jobs from a bed past Draft", len(rows))
	}
	if jobBatchOf(t, store, onIt) == nil {
		t.Error("a job was taken off a bed that is already committed to a printer")
	}
	if !batchExists(t, store, locked) {
		t.Error("an approved bed was removed")
	}
}
