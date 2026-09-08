package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// Uploading a model releases a product Tensor cannot build into batching.
//
// This is the whole route to a bed for a photo frame, a night lamp, a name
// plate - anything the renderer does not own. The importer flags such a job
// with whichever of three reasons fits what it could not find, and every one of
// them means the same thing: there is no model here. An operator supplies the
// 3MF and the job must become ordinary work, joining the next bed of its
// colour like any other.
//
// Before this, the upload cleared only stl_missing. A job stamped
// no_approved_design - the usual flag for exactly these products, since no
// design row matches their SKU - stayed excluded from ListBatchableJobs after
// the operator had done the one thing that fixes it, and the queue offered them
// an upload button that visibly changed nothing.
func TestIntegrationUploadingAModelClearsEveryNoModelFlag(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	for _, issue := range []string{
		production.IssueSTLMissing,
		production.IssueNoApprovedDesign,
		production.IssueSKUMissing,
	} {
		t.Run(issue, func(t *testing.T) {
			jobID := seedConfiguredJob(t, store, "JOB-UP-"+issue, jobConfig{
				material: "PLA Basics", leftNozzleMm: 0.4, machineFamily: "H2C", colour: "GOLD",
			})
			flag(t, store, jobID, issue)
			if batchable(t, store, jobID) {
				t.Fatalf("a job flagged %s was batchable before any upload", issue)
			}

			fileID := seedFileAsset(t, store, 100, 40, 20)
			if _, err := store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
				ID: jobID, PrintFileID: &fileID,
			}); err != nil {
				t.Fatalf("attach the uploaded model: %v", err)
			}

			job, err := store.Q.GetProductionJobByID(ctx, jobID)
			if err != nil {
				t.Fatalf("reload the job: %v", err)
			}
			if job.IssueReason != nil {
				t.Errorf("issue_reason = %q after the upload, want cleared - the file "+
					"is the remedy for %s", *job.IssueReason, issue)
			}
			if !batchable(t, store, jobID) {
				t.Errorf("a job flagged %s is still out of the batchable pool after "+
					"its model was uploaded", issue)
			}
		})
	}

	// The other half of the rule, and the reason this is not a blanket clear:
	// a model file does not put filament on a shelf, so the job stays flagged
	// and out of the pool. Clearing it here would send a plate to a printer
	// that cannot print it.
	t.Run("a reason the upload does not answer survives", func(t *testing.T) {
		jobID := seedConfiguredJob(t, store, "JOB-UP-STOCK", jobConfig{
			material: "PLA Basics", leftNozzleMm: 0.4, machineFamily: "H2C", colour: "GOLD",
		})
		flag(t, store, jobID, production.IssueFilamentOutOfStock)

		fileID := seedFileAsset(t, store, 100, 40, 20)
		if _, err := store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
			ID: jobID, PrintFileID: &fileID,
		}); err != nil {
			t.Fatalf("attach the uploaded model: %v", err)
		}

		job, err := store.Q.GetProductionJobByID(ctx, jobID)
		if err != nil {
			t.Fatalf("reload the job: %v", err)
		}
		if job.IssueReason == nil || *job.IssueReason != production.IssueFilamentOutOfStock {
			t.Errorf("issue_reason = %v, want %s kept - an upload does not conjure filament",
				job.IssueReason, production.IssueFilamentOutOfStock)
		}
	})
}

func flag(t *testing.T, store *db.Store, jobID uuid.UUID, issue string) {
	t.Helper()
	if _, err := store.Q.UpdateProductionJobFields(context.Background(),
		gen.UpdateProductionJobFieldsParams{
			ID: jobID, SetIssueReason: true, IssueReason: &issue,
		}); err != nil {
		t.Fatalf("flag the job %s: %v", issue, err)
	}
}

// batchable reports whether the planner would pick this job up, asked of the
// query the planner actually uses rather than of the column it reads.
func batchable(t *testing.T, store *db.Store, jobID uuid.UUID) bool {
	t.Helper()
	rows, err := store.Q.ListBatchableJobs(context.Background())
	if err != nil {
		t.Fatalf("list batchable jobs: %v", err)
	}
	for _, r := range rows {
		if r.ID == jobID {
			return true
		}
	}
	return false
}
