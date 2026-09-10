package httpapi

// Putting one bed on a printer, on purpose, because somebody pressed a button.
//
// The automatic dispatcher walks locked beds toward a printer by itself
// (batch_dispatch.go). This is the other half: the operator who has looked at a
// bed and wants it printing NOW, without waiting for the next pass, and who may
// be looking at a Draft rather than a locked bed.
//
// A Draft is locked first, never sent as a Draft. The next planning run can
// dissolve a Draft and rebuild it from different jobs, so sending one would
// commit filament and a printer to a layout that no longer exists by the time
// it prints - which is exactly why SendBatchToPrinter refuses anything that is
// not locked. Locking first makes the plate that prints the plate the operator
// was looking at.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// QueueBatchForPrinting locks a Draft if it needs locking, then sends the bed
// to BambuBuddy.
//
// Deliberately NOT atomic, because it cannot be: approving is a database
// transaction and sending is a multi-megabyte upload followed by a slice on the
// printer host. It is instead RECOVERABLE - the state between the two halves is
// an ordinary locked bed carrying a print_error, which the dispatcher retries on
// its next pass and which the operator can queue again by pressing the button a
// second time. SendBatchToPrinter's own already-sent guard makes that safe.
func (s *Server) QueueBatchForPrinting(
	ctx context.Context, batchID uuid.UUID, actor string,
) (printBatchResponse, error) {
	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		if isNoRows(err) {
			return printBatchResponse{}, statusErr(http.StatusNotFound, "That batch does not exist.")
		}
		return printBatchResponse{}, statusErr(http.StatusInternalServerError, "Could not load the batch.")
	}

	locked := false
	switch batch.Status {
	case production.BatchPendingApproval:
		// ApproveBatchFor does every part of locking: it re-validates each job
		// at the moment of commitment (a job held or flagged since planning is
		// refused here, before anything is uploaded), assigns a machine, builds
		// and stores the merged plate, reserves the filament and flips the
		// status - the last three in one transaction.
		//
		// readyToLock is deliberately not consulted. That gate decides whether
		// the AUTOMATIC dispatcher should freeze a half-empty bed; an operator
		// pressing this button has already decided. The response says how full
		// the bed was so the decision is visible rather than silent.
		approved, err := s.ApproveBatchFor(ctx, batchID, nil, actor)
		if err != nil {
			return printBatchResponse{}, err
		}
		batch = approved
		locked = true

	case production.BatchOpen:
		// Already locked; nothing to do but send it - except that a bed whose
		// last print FAILED keeps its 'open' status and its outcome, and
		// ListBatchesToDispatch excludes anything holding one. Clearing it here
		// is the deliberate human "run that again", and the only way back: the
		// automatic dispatcher must never retry a failed plate into whatever
		// went wrong the first time.
		if batch.PrintOutcome != nil {
			if err := s.store.Q.ClearBatchPrintOutcome(ctx, batch.ID); err != nil {
				return printBatchResponse{}, statusErrf(http.StatusInternalServerError,
					"Could not clear the previous print result.", err)
			}
			batch.PrintOutcome = nil
			batch.QueueItemID = nil
			batch.PipelineRunID = nil
		}

	case production.BatchInProgress:
		return printBatchResponse{}, statusErr(http.StatusConflict,
			"This batch is already printing.")

	default:
		return printBatchResponse{}, statusErr(http.StatusConflict, fmt.Sprintf(
			"A %s batch cannot be queued.", batch.Status))
	}

	resp, err := s.SendBatchToPrinter(ctx, batch)
	resp.Locked = locked
	if locked && err == nil {
		resp.Note = joinNotes([]string{underfilledNote(ctx, s, batch), resp.Note})
	}
	if err != nil {
		// The bed is locked and the send failed. Report both halves: the
		// operator needs to know the filament is now committed, because that is
		// the part they cannot undo by pressing the button again.
		if locked {
			return printBatchResponse{Locked: true}, err
		}
		return printBatchResponse{}, err
	}
	return resp, nil
}

// underfilledNote says how full a bed was when an operator chose to lock it, or
// nothing when it was full.
//
// Silence for a full bed on purpose: a note on every queue would train people
// to stop reading them, and "4 of 4" tells nobody anything.
func underfilledNote(ctx context.Context, s *Server, batch gen.Batch) string {
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return ""
	}
	used, cap := unitsOf(jobs), s.bedUnitCap()
	if used >= cap {
		return ""
	}
	return fmt.Sprintf("locked with %d of %d places used", used, cap)
}
