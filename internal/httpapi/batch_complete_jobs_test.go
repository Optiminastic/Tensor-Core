package httpapi

// Finishing a bed plank by plank.
//
// The rule in one line: the bed stays open until nothing on it is outstanding.
// Signing off three of four planks must not finish the bed, or the fourth
// disappears from the floor's view while it is still a real piece of work.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// seedBedOfFour returns a locked bed holding four single-unit jobs.
func seedBedOfFour(t *testing.T, store *db.Store, number string) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	machineID := seedMachine(t, store, "DONE-"+number)
	batchID := seedDraftBatch(t, store, number, production.BatchOpen, &machineID, "P2S")
	jobs, err := store.Q.ListJobsForBatch(context.Background(), &batchID)
	if err != nil {
		t.Fatalf("read the seeded bed: %v", err)
	}
	ids := []uuid.UUID{jobs[0].ID}
	for i := 2; i <= 4; i++ {
		ids = append(ids, seedConfiguredJob(t, store, number+"-J"+string(rune('0'+i)), jobConfig{
			batchID: &batchID, material: "PLA", leftNozzleMm: 0.4, machineFamily: "P2S",
		}))
	}
	return batchID, ids
}

func completeJobs(t *testing.T, router http.Handler, token string, batchID uuid.UUID, jobIDs ...uuid.UUID) completeBatchJobsResponse {
	t.Helper()
	raw := make([]string, 0, len(jobIDs))
	for _, id := range jobIDs {
		raw = append(raw, id.String())
	}
	rr := doJSON(router, http.MethodPost, "/batches/"+batchID.String()+"/jobs/complete", token,
		map[string]any{"job_ids": raw})
	if rr.Code != http.StatusOK {
		t.Fatalf("complete jobs = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var out completeBatchJobsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// Three of four: those three are done, the bed is not.
func TestIntegrationCompletingSomeJobsLeavesTheBedOpen(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	ctx := context.Background()

	batchID, jobs := seedBedOfFour(t, store, "BATCH-DONE-PART")

	out := completeJobs(t, router, manage, batchID, jobs[0], jobs[1], jobs[2])
	if out.Completed != 3 {
		t.Errorf("completed = %d, want 3", out.Completed)
	}
	if out.Remaining != 1 {
		t.Errorf("remaining = %d, want 1", out.Remaining)
	}
	if out.Batch.Status != production.BatchOpen {
		t.Errorf("bed status = %q, want it still open - one plank is outstanding, and "+
			"finishing the bed would hide it", out.Batch.Status)
	}

	// The three are done and STILL on the bed: a bed that printed is a record of
	// what ran, and the Completed list reads its orders from these rows.
	for i, id := range jobs[:3] {
		job, err := store.Q.GetProductionJobByID(ctx, id)
		if err != nil {
			t.Fatalf("reload job %d: %v", i, err)
		}
		if job.Status != production.StatusCompleted {
			t.Errorf("job %d status = %q, want completed", i, job.Status)
		}
		if job.BatchID == nil || *job.BatchID != batchID {
			t.Errorf("job %d was detached from its bed", i)
		}
	}
	last, err := store.Q.GetProductionJobByID(ctx, jobs[3])
	if err != nil {
		t.Fatalf("reload the untouched job: %v", err)
	}
	if last.Status != production.StatusQueued {
		t.Errorf("unticked job status = %q, want it still queued", last.Status)
	}
}

// The last plank finishes the bed.
func TestIntegrationCompletingTheLastJobFinishesTheBed(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	batchID, jobs := seedBedOfFour(t, store, "BATCH-DONE-ALL")

	completeJobs(t, router, manage, batchID, jobs[0], jobs[1])
	out := completeJobs(t, router, manage, batchID, jobs[2], jobs[3])

	if out.Remaining != 0 {
		t.Errorf("remaining = %d, want 0", out.Remaining)
	}
	if out.Batch.Status != production.BatchCompleted {
		t.Errorf("bed status = %q, want %q once every plank is signed off",
			out.Batch.Status, production.BatchCompleted)
	}
}

// A job that is not on this bed is refused rather than ignored. Ignoring it
// would let the dialog report four planks finished when it finished three.
func TestIntegrationCompletingAJobFromAnotherBedIsRefused(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})

	batchID, _ := seedBedOfFour(t, store, "BATCH-DONE-A")
	_, otherJobs := seedBedOfFour(t, store, "BATCH-DONE-B")

	rr := doJSON(router, http.MethodPost, "/batches/"+batchID.String()+"/jobs/complete", manage,
		map[string]any{"job_ids": []string{otherJobs[0].String()}})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("complete a job from another bed = %d, want 422", rr.Code)
	}
}

// A failed plank is left alone: its reprint is already queued as a job of its
// own, and force-completing it would erase the failure. It also must not hold
// the bed open for ever - the bed it failed on is finished with it.
func TestIntegrationAFailedPlankDoesNotHoldTheBedOpen(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	ctx := context.Background()

	batchID, jobs := seedBedOfFour(t, store, "BATCH-DONE-FAIL")
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET status = 'failed' WHERE id = $1`, jobs[3]); err != nil {
		t.Fatalf("fail a plank: %v", err)
	}

	out := completeJobs(t, router, manage, batchID, jobs[0], jobs[1], jobs[2])
	if out.Remaining != 0 {
		t.Errorf("remaining = %d, want 0 - a failed plank is dealt with elsewhere", out.Remaining)
	}
	if out.Batch.Status != production.BatchCompleted {
		t.Errorf("bed status = %q, want completed", out.Batch.Status)
	}
	failed, err := store.Q.GetProductionJobByID(ctx, jobs[3])
	if err != nil {
		t.Fatalf("reload the failed job: %v", err)
	}
	if failed.Status != production.StatusFailed {
		t.Errorf("failed job status = %q; completing the bed overwrote its failure", failed.Status)
	}
}
