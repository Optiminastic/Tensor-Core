package httpapi

// Reprinting a finished bed: the chosen planks, into a new locked bed, ready to
// send.
//
// A completed batch's planks are on a shelf. When some of them are wrong - the
// wrong names engraved, a bad first layer nobody caught until QC - each job can
// already be reprinted one at a time from the completed-batch board, and each
// reprint is a fresh job at urgent priority waiting to be batched with whatever
// else turns up. That is the right shape for one bad plank and the wrong shape
// for five: the operator wants those five on a bed now, not scattered across
// three future beds by the planner.
//
// So this reprints a SELECTION and puts the clones straight onto one locked bed.
// A selection rather than the whole batch, because a bed is rarely wholly wrong:
// on the bed that prompted this, three of four planks were correct and
// reprinting them would have been four hours of filament nobody needed.
//
// Everything here is the existing machinery in order: FailProductionJob for each
// chosen job (a reprint always records why), then a Draft bed holding the
// clones, then ApproveBatchFor to lock it - which is what reserves the filament,
// picks the machine and builds the plate. Nothing about reservation or machine
// choice is reimplemented.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// reprintBatchRequest is which planks to reprint and why.
//
// The reason is required and shared across the selection: they came off one bed
// in one state, and asking for it per job would be a form nobody fills in
// honestly.
type reprintBatchRequest struct {
	JobIDs              []string `json:"job_ids" binding:"required,min=1"`
	Reason              string   `json:"reason" binding:"required"`
	Notes               *string  `json:"notes"`
	FilamentWastedGrams *float64 `json:"filament_wasted_grams"`
	// int32 to match FailJobInput, which takes the column's own width rather
	// than Go's default int.
	TimeWastedMinutes *int32 `json:"time_wasted_minutes"`
}

// reprintBatchResponse is the new bed and what went onto it.
type reprintBatchResponse struct {
	// Batch is the locked bed holding the reprints, ready to send.
	Batch batchResponse `json:"batch"`
	// Reprinted pairs each failed job with the clone that replaces it, so the
	// operator can see which plank became which.
	Reprinted []reprintPair `json:"reprinted"`
	Note      string        `json:"note"`
}

type reprintPair struct {
	FailedJobNumber  string `json:"failed_job_number"`
	ReprintJobNumber string `json:"reprint_job_number"`
}

// reprintBatchJobs reprints selected planks from a finished bed onto a new one.
func (s *Server) reprintBatchJobs(c *gin.Context) {
	if !s.filesReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req reprintBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	if !production.ValidFailureReason(req.Reason) {
		detail(c, http.StatusUnprocessableEntity, "That failure reason is not valid.")
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	// Only a finished bed. A bed still printing has not produced the planks
	// anybody would be reprinting, and failing its jobs mid-print would take
	// them off a plate that is on a machine right now.
	if batch.Status != production.BatchCompleted {
		detail(c, http.StatusConflict,
			"Only a completed batch can be reprinted; this one is "+batch.Status+".")
		return
	}

	onBed, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	// Checked against the bed rather than taken on trust: a job id is
	// client-controlled, and failing a job from another bed would be a quiet way
	// to scrap somebody else's work.
	onThisBed := make(map[uuid.UUID]bool, len(onBed))
	for _, j := range onBed {
		onThisBed[j.ID] = true
	}

	chosen := make([]uuid.UUID, 0, len(req.JobIDs))
	for _, raw := range req.JobIDs {
		jobID, err := uuid.Parse(raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "One of the job ids is not a valid identifier.")
			return
		}
		if !onThisBed[jobID] {
			detail(c, http.StatusUnprocessableEntity, "One of the jobs is not on this batch.")
			return
		}
		chosen = append(chosen, jobID)
	}

	in := FailJobInput{
		Reason: req.Reason, Notes: req.Notes,
		FilamentWastedGrams: req.FilamentWastedGrams, TimeWastedMinutes: req.TimeWastedMinutes,
	}
	actor := currentUserID(c)

	// make, not var: a nil slice marshals as null, and the caller parses this as
	// an array. See batch_rebuild.go for the same trap sprung.
	pairs := make([]reprintPair, 0, len(chosen))
	clones := make([]uuid.UUID, 0, len(chosen))
	for _, jobID := range chosen {
		failed, reprint, err := s.FailProductionJob(ctx, jobID, in, actor)
		if err != nil {
			// Reported with what has already happened, not rolled back: the
			// jobs failed before this one really did fail, and their reprints
			// really do exist. Hiding that would leave an operator hunting for
			// jobs the system denies making.
			writeStatusError(c, err, "Could not reprint every job on this batch.")
			return
		}
		pairs = append(pairs, reprintPair{
			FailedJobNumber: failed.JobNumber, ReprintJobNumber: reprint.JobNumber,
		})
		clones = append(clones, reprint.ID)
	}

	newBatch, err := s.bedForReprints(c, clones)
	if err != nil {
		return
	}

	obs.FromContext(ctx).Info("bed reprinted onto a new one",
		"from", batch.BatchNumber, "to", newBatch.BatchNumber, "planks", len(pairs))
	c.JSON(http.StatusCreated, reprintBatchResponse{
		Batch: batchDTO(newBatch), Reprinted: pairs,
		Note: newBatch.BatchNumber + " is locked and ready to send.",
	})
}

// bedForReprints puts the clones on one bed and locks it.
//
// Created as a Draft and then approved, rather than inserted as open: approval
// is what reserves the filament, chooses the machine and merges the plate, and
// a bed that skipped it would be locked in name only - with no plate to send.
//
// Writes its own error response and returns it, so the caller can simply return.
func (s *Server) bedForReprints(c *gin.Context, clones []uuid.UUID) (gen.Batch, error) {
	ctx := c.Request.Context()

	number, err := s.store.Q.NextBatchNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a batch number.")
		return gen.Batch{}, err
	}
	draft, err := s.store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batch for the reprints.")
		return gen.Batch{}, err
	}
	// One call for the whole selection, the same way addJobsToBatch assigns a
	// hand-picked set: the clones go on together or not at all.
	if err := s.store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: &draft.ID, JobIds: clones,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not put the reprints on the batch.")
		return gen.Batch{}, err
	}

	locked, err := s.ApproveBatchFor(ctx, draft.ID, nil, currentUserID(c))
	if err != nil {
		// The bed exists and holds the reprints; only locking failed - most
		// often because no machine can take it yet. Said plainly, because the
		// remedy is to approve it from the Batches page once one can.
		writeStatusError(c, err,
			number+" was created with the reprints on it, but could not be locked yet.")
		return gen.Batch{}, err
	}
	return locked, nil
}
