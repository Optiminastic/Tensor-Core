package httpapi

// Deleting a bed.
//
// A batch is not just a row. It holds jobs somebody is waiting for, it may have
// filament reserved against it, and it may have a plate sitting in BambuBuddy's
// queue waiting for a printer. Deleting one without unwinding those leaves the
// jobs stranded - assigned to a batch that no longer exists, invisible to the
// planner, waiting for a plate nobody will ever print - and the filament
// reserved for work that will never happen.
//
// So the order matters, and it is the same order an edit already uses:
// withdraw the plate, release the filament, put the jobs back in the pool, and
// only then drop the bed.
//
// Two statuses are refused outright, for different reasons. A bed that is
// PRINTING is on a machine right now; deleting it would leave Tensor with no
// record of a plate that is physically being made. A bed that has PRINTED is
// the record of what came off that machine - the thing you go back to when a
// customer asks what they were sent - and erasing history to tidy a list is a
// bad trade. Neither refusal is a technical limit; both are judgements, and the
// message says which.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// deleteBatchResponse says what the deletion freed, so the caller can tell an
// operator where the work went rather than only that a row disappeared.
type deleteBatchResponse struct {
	BatchNumber string `json:"batch_number"`
	// JobsReleased is how many jobs went back to the pool to be re-planned.
	JobsReleased int    `json:"jobs_released"`
	Note         string `json:"note"`
}

// deleteBatch removes one bed and returns its jobs to the pool.
func (s *Server) deleteBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}

	switch batch.Status {
	case production.BatchInProgress:
		detail(c, http.StatusConflict,
			"This batch is printing on a machine right now, so it cannot be deleted.")
		return
	case production.BatchCompleted:
		detail(c, http.StatusConflict,
			"This batch has already printed. Its plate and jobs are the record of what was made, so it cannot be deleted.")
		return
	}

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}

	// Withdraws the plate from BambuBuddy's queue and gives back any filament
	// this bed reserved. A no-op on a Draft, which has neither.
	if err := s.beginBatchEdit(ctx, batch); err != nil {
		writeStatusError(c, err, "Could not release the batch before deleting it.")
		return
	}

	// Jobs first, then the bed. The other order would leave a window in which
	// the jobs point at a batch row that is already gone.
	if err := s.store.Q.ReleaseJobsFromBatch(ctx, &id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not return the batch's jobs to the queue.")
		return
	}
	rows, err := s.store.Q.DeleteBatch(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the batch.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "That batch does not exist.")
		return
	}

	// The jobs are batchable again, and a bed's worth of them arriving at once
	// is exactly the backlog the planner should look at.
	s.triggerBatchPlan(ctx)

	obs.FromContext(ctx).Info("batch deleted",
		"batch", batch.BatchNumber, "status", batch.Status, "jobs_released", len(jobs))
	c.JSON(http.StatusOK, deleteBatchResponse{
		BatchNumber:  batch.BatchNumber,
		JobsReleased: len(jobs),
		Note:         batch.BatchNumber + " was deleted; its jobs are back in the queue to be re-planned.",
	})
}
