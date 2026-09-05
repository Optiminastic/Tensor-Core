package httpapi

// Finishing a bed plank by plank.
//
// POST /batches/:id/jobs/complete
//
// Marking a whole bed Done in one action assumed every plank on it succeeded.
// That is not how a plate comes off a printer: three are good and one warped, or
// one customer's name is wrong, and the operator wants to sign off the three
// while the fourth is still being sorted out. Done-in-one forced them to either
// finish the bad plank as though it were good, or leave three finished planks
// waiting on it.
//
// So finishing is a selection. The ticked jobs are completed - which is what
// puts them in front of Assembly, QC and Packaging - and the bed itself only
// becomes Done when nothing on it is still outstanding.
//
// The completed jobs stay ON the bed. A bed that has printed is a record of what
// physically ran, and the Completed list reads its orders and colours from the
// jobs that point at it.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type completeBatchJobsRequest struct {
	JobIDs []string `json:"job_ids"`
}

// completeBatchJobsResponse says what happened, so the dialog can report it
// without a second round trip.
type completeBatchJobsResponse struct {
	Batch batchResponse `json:"batch"`
	// Completed is how many planks this request finished - not how many were
	// asked for. A plank already done, or one that failed and is being
	// reprinted, is left alone.
	Completed int `json:"completed"`
	// Remaining is how many planks on the bed are still outstanding. Zero means
	// the bed has just moved to Done.
	Remaining int `json:"remaining"`
}

// completeBatchJobs marks the selected planks on a bed done.
func (s *Server) completeBatchJobs(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req completeBatchJobsRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.JobIDs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "Select at least one job to mark done.")
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if batch.Status == production.BatchCompleted {
		detail(c, http.StatusConflict, "This batch is already finished.")
		return
	}

	// Every id must be a plank on THIS bed. Silently ignoring a stray id would
	// let the dialog report four planks finished when it finished three.
	onBed, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	member := make(map[uuid.UUID]bool, len(onBed))
	for _, j := range onBed {
		member[j.ID] = true
	}
	ids := make([]uuid.UUID, 0, len(req.JobIDs))
	for _, raw := range req.JobIDs {
		jobID, err := uuid.Parse(raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "One of the job_ids is not a valid identifier.")
			return
		}
		if !member[jobID] {
			detail(c, http.StatusUnprocessableEntity, "One of the jobs is not on this batch.")
			return
		}
		ids = append(ids, jobID)
	}

	done, err := s.store.Q.CompleteSelectedJobsOnBatch(ctx, gen.CompleteSelectedJobsOnBatchParams{
		BatchID: &id, JobIds: ids,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not mark those jobs done.")
		return
	}
	remaining, err := s.store.Q.CountUnfinishedJobsOnBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read what is left on the batch.")
		return
	}

	log := obs.FromContext(ctx)
	updated := batch
	if remaining == 0 {
		// Nothing outstanding, so the bed is finished. SetBatchStatus completes
		// any straggler and schedules a replan - a machine has just freed up.
		updated, err = s.SetBatchStatus(ctx, id, production.BatchCompleted)
		if err != nil {
			writeStatusError(c, err, "Could not finish the batch.")
			return
		}
		log.Info("bed finished, every plank signed off", "batch", batch.BatchNumber)
	} else {
		log.Info("planks signed off, bed still open",
			"batch", batch.BatchNumber, "completed", len(done), "remaining", remaining)
	}

	c.JSON(http.StatusOK, completeBatchJobsResponse{
		Batch: batchDTO(updated), Completed: len(done), Remaining: int(remaining),
	})
}
