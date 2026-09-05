package httpapi

// Editing a bed by hand, including one that is already locked.
//
// A locked bed used to be final: approval reserved its filament, stamped a
// machine and built its plate, so membership was frozen from there. That is
// right for a bed on a printer and wrong for every other reason a bed gets
// locked - a plank fails QC, a customer changes their mind, an operator sees a
// mistake before the plate ever moves. The answer was to delete the bed and let
// the planner build another, which loses the three planks that were fine.
//
// So a locked bed is editable, and editing it undoes what locking did, in the
// order that keeps the physical world in step:
//
//  1. take the plate back out of BambuBuddy's queue, if it went there
//  2. give back the filament the old composition reserved
//  3. change the membership
//  4. rebuild the plate from what is now on the bed, and reserve for that
//
// The one thing it will not do is edit a bed that is PRINTING. That plate is on
// a machine, laying plastic; there is no version of "remove a plank" that
// reaches it.

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// editableBatch reports whether a bed's membership may still be changed, and
// says why not when it may not.
//
// Draft and Locked, yes. Printing and Done, no - and for the same reason in
// both cases: those two states record what physically happened. A plate is on a
// machine or has come off one, and editing the list afterwards would make the
// record describe a print that never ran.
func editableBatch(b gen.Batch) (bool, string) {
	switch b.Status {
	case production.BatchPendingApproval, production.BatchOpen:
		return true, ""
	case production.BatchInProgress:
		return false, "This batch is printing, so its jobs can no longer be changed."
	default:
		return false, "This batch is finished, so its jobs can no longer be changed."
	}
}

// requireEditableBatch loads a bed and refuses the request if its membership is
// settled.
func (s *Server) requireEditableBatch(c *gin.Context, id uuid.UUID) (gen.Batch, bool) {
	batch, err := s.store.Q.GetBatchByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return gen.Batch{}, false
	}
	if ok, why := editableBatch(batch); !ok {
		detail(c, http.StatusConflict, why)
		return gen.Batch{}, false
	}
	return batch, true
}

// plateableBatch checks object storage before an edit that will need to build a
// plate.
//
// Checked before anything is written, not when the rebuild reaches for it:
// finding out afterwards would leave a bed whose membership had changed, whose
// plate had been cleared, and whose caller was told the edit failed.
//
// Not required when the edit will leave the bed empty. There is no plate to
// build for nothing, and demanding storage there would block the one edit that
// needs none - taking the last plank off a bed.
func (s *Server) plateableBatch(c *gin.Context, remaining int) bool {
	if remaining <= 0 {
		return true
	}
	return s.filesReady(c)
}

// beginBatchEdit prepares a locked bed to be changed: it withdraws the plate
// from BambuBuddy's queue and releases the filament the old composition
// reserved.
//
// Called BEFORE the membership changes, because both halves are about the bed
// as it stands: the queue holds the old plate, and the reservation was made for
// the old jobs. Doing either afterwards would withdraw the right plate for the
// wrong reason and refund the wrong grams.
//
// A Draft needs none of it - nothing was reserved and nothing was sent - so this
// is a no-op there.
func (s *Server) beginBatchEdit(ctx context.Context, batch gen.Batch) error {
	if batch.Status != production.BatchOpen {
		return nil
	}
	if err := s.withdrawFromPrinterQueue(ctx, batch); err != nil {
		return err
	}
	if !batch.FilamentReserved {
		return nil
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return statusErrf(http.StatusInternalServerError, "Could not read the batch's jobs.", err)
	}
	if err := s.adjustFilamentByColour(ctx, s.store.Q, filamentSplitForJobs(jobs), +1); err != nil {
		return statusErrf(http.StatusInternalServerError, "Could not release the batch's filament.", err)
	}
	return nil
}

// withdrawFromPrinterQueue takes a bed's plate back out of BambuBuddy's queue.
//
// Refuses while the item is printing: that plate is on a machine and cannot be
// recalled, so the honest answer is to say so rather than to edit Tensor into
// disagreeing with the printer.
func (s *Server) withdrawFromPrinterQueue(ctx context.Context, batch gen.Batch) error {
	if batch.QueueItemID == nil || s.bambu == nil || !s.bambu.Configured() {
		return nil
	}
	log := obs.FromContext(ctx)
	itemID := int(*batch.QueueItemID)

	item, err := s.bambu.GetQueueItem(ctx, itemID)
	if err != nil {
		// Not fatal. BambuBuddy being unreachable must not trap a bed as
		// uneditable, and the item is removed below on a best-effort basis -
		// the operator can clear a leftover from BambuBuddy's own queue.
		log.Warn("could not read a bed's queue item before editing it",
			"batch", batch.BatchNumber, "queue_item", itemID, "error", err)
	} else if item.Status == bambubuddy.QueuePrinting {
		return statusErr(http.StatusConflict,
			"This batch's plate is printing on a machine right now, so its jobs cannot be changed.")
	}

	if err := s.bambu.RemoveQueueItem(ctx, itemID); err != nil {
		return statusErrf(http.StatusBadGateway,
			"Could not take this batch's plate out of the printer queue, so its jobs were left alone.", err)
	}
	log.Info("withdrew a bed's plate from the printer queue to edit it",
		"batch", batch.BatchNumber, "queue_item", itemID)
	return nil
}

// finishBatchEdit rebuilds everything the new membership implies.
//
// The plate is rebuilt here rather than at the next approval because a locked
// bed has no next approval - it is already approved. Without this the bed would
// keep the plate it was locked with, and the plank somebody just removed would
// print anyway.
func (s *Server) finishBatchEdit(ctx context.Context, c *gin.Context, batch gen.Batch) (gen.Batch, bool) {
	if batch.Status != production.BatchOpen {
		// A Draft is measured and re-plated by the shared path, and re-approved
		// later like any other bed.
		return s.recomputeBatchPlate(ctx, c, batch)
	}

	// Everything that described the old four planks goes first, so a failure
	// below leaves a bed with no plate rather than a bed with the wrong one.
	if _, err := s.store.Q.ClearBatchPlateForEdit(ctx, batch.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not clear the batch's old plate.")
		return gen.Batch{}, false
	}
	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return gen.Batch{}, false
	}

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return gen.Batch{}, false
	}
	if batch.FilamentReserved && len(jobs) > 0 {
		if err := s.adjustFilamentByColour(ctx, s.store.Q, filamentSplitForJobs(jobs), -1); err != nil {
			detail(c, http.StatusInternalServerError, "Could not reserve filament for the edited batch.")
			return gen.Batch{}, false
		}
	}

	// The corrected plate has to go back to a printer, and the bed is already
	// locked - so the dispatcher's approval step will not pick it up. This is
	// what sends it.
	s.triggerDispatch(ctx)
	return updated, true
}

// EditBatchByRemovingJob takes one plank off a bed and re-plates what is left:
// withdraw from the printer queue, remove, rebuild.
//
// The endpoint's whole body, and callable without one - a maintenance command
// runs exactly what DELETE /batches/:id/jobs/:jobId runs, rather than a second
// implementation that proves nothing about the endpoint. That matters here more
// than usual: the integration suite has no object storage, so the plate rebuild
// can only be exercised against a real bucket.
//
// Writes its own error responses onto c, as the handler did.
func (s *Server) EditBatchByRemovingJob(
	ctx context.Context, c *gin.Context, batchID, jobID uuid.UUID,
) (gen.Batch, bool) {
	batch, ok := s.requireEditableBatch(c, batchID)
	if !ok {
		return gen.Batch{}, false
	}
	held, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return gen.Batch{}, false
	}
	if !s.plateableBatch(c, len(held)-1) {
		return gen.Batch{}, false
	}
	// A locked bed gives its plate back and its filament back before it changes
	// - see beginBatchEdit. A Draft has neither, so this is a no-op there.
	if err := s.beginBatchEdit(ctx, batch); err != nil {
		writeStatusError(c, err, "Could not prepare the batch for editing.")
		return gen.Batch{}, false
	}
	if _, err := s.store.Q.RemoveJobFromBatch(ctx, gen.RemoveJobFromBatchParams{
		ID: jobID, BatchID: &batchID,
	}); err != nil {
		dbError(c, err, "That job is not on this batch.", "Could not remove the job.")
		return gen.Batch{}, false
	}
	return s.finishBatchEdit(ctx, c, batch)
}
