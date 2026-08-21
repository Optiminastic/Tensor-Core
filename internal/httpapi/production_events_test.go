package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// TestIntegrationJobEventsStayAppendOnly pins what migrations 0037/0038
// relaxed, and what they must not.
//
// The log started out refusing every UPDATE and DELETE unconditionally. That
// read as maximally safe and was actually a deadlock: production_job_events
// has a job_id FK declared ON DELETE CASCADE plus batch_id/related_job_id
// declared ON DELETE SET NULL, so the trigger blocked the database's own
// referential actions - and no job or batch that had ever recorded an event
// could be deleted at all. Every reset path was silently broken.
//
// The relaxation is narrow: a referential action may remove or null a row
// whose referent is already gone. Editing history is still refused, and that
// is the half worth a test.
func TestIntegrationJobEventsStayAppendOnly(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	batchID := seedDraftBatch(t, store, "BATCH-EVENTS", production.BatchPendingApproval, nil, "H2C")
	jobs, err := store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("load batch jobs: %v (%d)", err, len(jobs))
	}
	job := jobs[0]

	if err := recordJobEvent(ctx, store.Q, jobEvent{
		JobID: job.ID, EventType: production.EventJobCreated,
		ActorID: "usr_admin", BatchID: &batchID,
	}); err != nil {
		t.Fatalf("record event: %v", err)
	}

	// Tampering is still refused, both ways.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_job_events SET actor_id = 'someone_else' WHERE job_id = $1`, job.ID); err == nil {
		t.Error("rewriting an event's actor succeeded; the log is no longer append-only")
	}
	if _, err := store.Pool.Exec(ctx,
		`DELETE FROM production_job_events WHERE job_id = $1`, job.ID); err == nil {
		t.Error("deleting an event for a job that still exists succeeded; history can be selectively erased")
	}

	// Deleting the batch nulls batch_id on the event (ON DELETE SET NULL) -
	// this is the referential action 0038 had to permit.
	if _, err := store.Pool.Exec(ctx, `DELETE FROM batches WHERE id = $1`, batchID); err != nil {
		t.Fatalf("deleting a batch with event history must succeed: %v", err)
	}
	var batchRef *uuid.UUID
	if err := store.Pool.QueryRow(ctx,
		`SELECT batch_id FROM production_job_events WHERE job_id = $1`, job.ID).Scan(&batchRef); err != nil {
		t.Fatalf("event vanished when its batch was deleted: %v", err)
	}
	if batchRef != nil {
		t.Errorf("batch_id = %v after the batch was deleted, want NULL", batchRef)
	}

	// Deleting the job takes its history with it - the cascade 0037 permitted.
	if _, err := store.Pool.Exec(ctx, `DELETE FROM production_jobs WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("deleting a job with event history must succeed: %v", err)
	}
	var left int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM production_job_events WHERE job_id = $1`, job.ID).Scan(&left); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if left != 0 {
		t.Errorf("%d event(s) survived their job's deletion", left)
	}
}
