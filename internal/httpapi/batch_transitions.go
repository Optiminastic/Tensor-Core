package httpapi

// The three batch/job transitions that more than one caller drives, lifted out
// of their Gin handlers so there is exactly one implementation of each:
//
//	ApproveBatchFor    Draft -> Locked: merge the plate, reserve filament, stamp
//	                   the machine and the approver.
//	SetBatchStatus     any -> a new batch status, completing every job on the bed
//	                   when that status is "completed".
//	FailProductionJob  a job failed on the bed: record the failure, waste and
//	                   reprint.
//
// The rule this file exists to enforce: a non-HTTP caller (the print simulator,
// a future Bambu bridge) may drive a transition only by calling the same method
// the handler calls. Anything else is a second lifecycle implementation that
// will drift - the mistake internal/production/lifecycle.go's doc comment warns
// about, and the reason CreateJobsForOrder and AutoCreateBatches already have
// this shape.
//
// Each method returns *statusError for the outcomes its handler used to write a
// response for, so the handler reproduces byte-identical status codes and
// {"detail"} strings through one errors.As, and a non-HTTP caller gets a plain
// error it can inspect.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// statusError is an extracted transition's HTTP outcome, carried out of the
// method so the handler does not have to re-derive it from a sentinel. err
// holds the underlying cause for logging; it is never shown to the client.
type statusError struct {
	status int
	msg    string
	err    error
}

func (e *statusError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.err)
	}
	return e.msg
}

func (e *statusError) Unwrap() error { return e.err }

// Status is the HTTP status this outcome maps to, for non-HTTP callers that
// want to tell "lost a race" (409) from "genuinely broken" (500).
func (e *statusError) Status() int { return e.status }

func statusErr(status int, msg string) *statusError { return &statusError{status: status, msg: msg} }

func statusErrf(status int, msg string, cause error) *statusError {
	return &statusError{status: status, msg: msg, err: cause}
}

// writeStatusError renders a *statusError to the response. Any other error
// becomes the caller's generic 500 message, so an unexpected failure never
// leaks an internal string to the client.
func writeStatusError(c *gin.Context, err error, genericMsg string) {
	var se *statusError
	if errors.As(err, &se) {
		detail(c, se.status, se.msg)
		return
	}
	detail(c, http.StatusInternalServerError, genericMsg)
}

// --- approve --------------------------------------------------------------

// ApproveBatchFor moves a Draft batch to Locked: it resolves the merged plate
// (reusing the Draft's cached preview when there is one), reserves the
// filament, and stamps the machine and approver. machineID overrides the
// machine the batch planner already assigned; pass nil to keep it.
//
// The filament reservation and the Draft->Locked transition share one
// transaction so a lost race against ApproveBatch's own `WHERE status =
// 'pending_approval'` guard can never leave stock debited without the batch
// moving, or the reverse.
func (s *Server) ApproveBatchFor(
	ctx context.Context, batchID uuid.UUID, machineID *uuid.UUID, approvedBy string,
) (gen.Batch, error) {
	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		if isNoRows(err) {
			return gen.Batch{}, statusErrf(http.StatusNotFound, "That batch does not exist.", err)
		}
		return gen.Batch{}, statusErrf(http.StatusInternalServerError, "Could not load the batch.", err)
	}
	if batch.Status != production.BatchPendingApproval {
		return gen.Batch{}, statusErr(http.StatusConflict, "This batch has already been approved.")
	}

	if machineID == nil {
		machineID = batch.MachineID
	}
	if machineID == nil {
		return gen.Batch{}, statusErr(http.StatusUnprocessableEntity,
			"This batch has no machine assigned yet; provide a machine_id.")
	}
	if _, err := s.store.Q.GetMachineOps(ctx, *machineID); err != nil {
		if isNoRows(err) {
			return gen.Batch{}, statusErrf(http.StatusNotFound, "That machine does not exist.", err)
		}
		return gen.Batch{}, statusErrf(http.StatusInternalServerError, "Could not load the machine.", err)
	}

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		return gen.Batch{}, statusErrf(http.StatusInternalServerError, "Could not read the batch's jobs.", err)
	}
	if len(jobs) == 0 {
		return gen.Batch{}, statusErr(http.StatusConflict, "The batch has no jobs to approve.")
	}
	// Re-checked at the moment of commitment, not trusted from when the batch
	// was planned. A Draft can sit for a long time and the world moves under
	// it: personalisation can be un-validated, a job can be flagged, held, or
	// pulled onto another bed entirely. Locking on stale preconditions is how a
	// plate reaches a machine containing something that should never have been
	// printed.
	for _, j := range jobs {
		switch {
		case j.PersonalisationStatus == production.PersonalisationPending:
			return gen.Batch{}, statusErr(http.StatusConflict, "A job in this batch has unvalidated personalisation.")
		case j.IssueReason != nil:
			return gen.Batch{}, statusErr(http.StatusConflict, "A job in this batch has an unresolved issue.")
		case j.Held:
			return gen.Batch{}, statusErr(http.StatusConflict, "A job in this batch is on hold.")
		case j.Status != production.StatusQueued:
			return gen.Batch{}, statusErr(http.StatusConflict, "A job in this batch is no longer queued.")
		}
	}

	mergedID, unitsPerBed, utilisation, err := s.mergedPlateFor(ctx, batch, jobs, approvedBy)
	if err != nil {
		return gen.Batch{}, err
	}

	// Keep a real measurement of this bed if the plate-slice worker already
	// produced one. batchTimeFromJobs is the MAX-of-jobs approximation, and
	// recomputing it unconditionally here would silently overwrite the
	// measurement at approval - i.e. every batch would reach the machine
	// scheduler carrying the estimate again, and the plate slice would have
	// been for nothing.
	total, eff := batchTimeFromJobs(jobs)
	if batch.PlateSlicedAt.Valid {
		total, eff = batch.TotalPrintTimeMinutes, db.NumFloatPtr(batch.EffectiveTimePerUnitMinutes)
	}
	filament := sumFilament(jobs)
	split := filamentSplitForJobs(jobs)
	shortage := !s.filamentAvailableByColour(ctx, s.store.Q, split)

	var b gen.Batch
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := s.adjustFilamentByColour(ctx, q, split, -1); err != nil {
			return err
		}
		var err error
		b, err = q.ApproveBatch(ctx, gen.ApproveBatchParams{
			ID: batchID, MachineID: machineID, ApprovedBy: &approvedBy, MergedFileID: &mergedID,
			MaterialShortage: shortage, UnitsPerBed: unitsPerBed, TotalPrintTimeMinutes: total,
			EffectiveTimePerUnitMinutes: eff, TotalFilamentGrams: &filament,
			BedUtilizationPercent: utilisation,
		})
		return err
	})
	if isNoRows(err) {
		// Lost the race against the guard: someone else approved it between the
		// status check above and this UPDATE.
		return gen.Batch{}, statusErr(http.StatusConflict, "This batch has already been approved.")
	}
	if err != nil {
		return gen.Batch{}, statusErrf(http.StatusInternalServerError, "Could not approve the batch.", err)
	}
	// This is the one point where slicing is worth its cost. The bed is now
	// committed: filament is reserved, a machine is stamped, and re-planning
	// will not touch it again - so the plate about to be measured is the plate
	// that will actually print.
	//
	// Slicing Drafts instead (which this used to do) measured beds the next
	// planner pass would dissolve, at minutes of Bambu Studio CPU each. The
	// result lands via SetBatchPlateSliceResult, replacing the estimate the
	// batch was scheduled on; the machine scheduler reads
	// total_print_time_minutes, so its projection self-corrects with no extra
	// wiring. Best-effort - a failed slice leaves the batch on its estimate.
	if !b.PlateSlicedAt.Valid {
		units := 0
		if unitsPerBed != nil {
			units = int(*unitsPerBed)
		}
		s.enqueuePlateSlice(ctx, b, jobs, mergedID, units)
	}
	return b, nil
}

// --- status ---------------------------------------------------------------

// SetBatchStatus applies a batch status change and, when the new status is
// "completed", completes every job on the bed and schedules a replan.
//
// Completing the jobs here is what makes them reachable from the
// Assembly/Finishing/QC/Packaging queues at all: an operator moves the bed to
// Done, not each job individually. CompleteProductionJobsForBatch deliberately
// skips a job that failed on its own - its reprint was already queued by
// FailProductionJob, and force-completing it would erase the failure and let a
// bad print through as if it had passed.
func (s *Server) SetBatchStatus(ctx context.Context, batchID uuid.UUID, status string) (gen.Batch, error) {
	return s.updateBatchAnd(ctx, gen.UpdateBatchParams{ID: batchID, Status: &status}, status == production.BatchCompleted)
}

// --- fail -----------------------------------------------------------------

// FailJobInput is one print failure: why, optionally with what it cost. The
// waste figures are optional because an operator often does not weigh the
// scrap.
type FailJobInput struct {
	Reason              string
	Notes               *string
	FilamentWastedGrams *float64
	TimeWastedMinutes   *int32
}

// FailProductionJob records that a job failed on the bed: the job moves to
// failed, a failure row captures the reason and waste, the wasted filament is
// debited from stock across the job's colours, and an urgent reprint carrying
// the original's full planner data is queued in the same transaction.
//
// A print can be failed while it is still on the bed (in_production) or after
// it comes off (completed) - a part is very often only found to be bad at
// assembly, once someone picks it up. Restricting this to in_production made
// the whole endpoint unreachable in practice, because batch completion sets
// every job on the bed to 'completed'.
func (s *Server) FailProductionJob(
	ctx context.Context, id uuid.UUID, in FailJobInput, actor string,
) (failed, reprint gen.ProductionJob, err error) {
	if !production.ValidFailureReason(in.Reason) {
		return failed, reprint, statusErr(http.StatusUnprocessableEntity, "That failure reason is not valid.")
	}
	if in.FilamentWastedGrams != nil && *in.FilamentWastedGrams < 0 {
		return failed, reprint, statusErr(http.StatusUnprocessableEntity, "Filament wasted cannot be negative.")
	}
	if in.TimeWastedMinutes != nil && *in.TimeWastedMinutes < 0 {
		return failed, reprint, statusErr(http.StatusUnprocessableEntity, "Time wasted cannot be negative.")
	}

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return failed, reprint, statusErrf(http.StatusNotFound, "That production job does not exist.", err)
		}
		return failed, reprint, statusErrf(http.StatusInternalServerError, "Could not load the production job.", err)
	}
	if job.Status != production.StatusInProduction && job.Status != production.StatusCompleted {
		return failed, reprint, statusErr(http.StatusConflict, "Only a job that is printing or printed can be failed.")
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		reprintNumber, err := q.NextJobNumber(ctx)
		if err != nil {
			return err
		}
		reprintParams := reprintParamsFor(job, job.Quantity, reprintNumber)
		failed, err = q.SetProductionJobStatus(ctx, gen.SetProductionJobStatusParams{
			ID: id, Status: production.StatusFailed,
		})
		if err != nil {
			return err
		}
		if _, err := q.InsertProductionJobFailure(ctx, gen.InsertProductionJobFailureParams{
			ID: uuid.New(), JobID: id, Stage: production.FailureStagePrint, Reason: in.Reason,
			Notes: in.Notes, FilamentWastedGrams: in.FilamentWastedGrams,
			TimeWastedMinutes: in.TimeWastedMinutes, CreatedBy: actor,
		}); err != nil {
			return err
		}
		// Decrement the wasted filament from stock, split across the job's
		// colours (best-effort per bucket, no-op if untracked) - the same fix
		// as the colour-blind shortage check batch approval used to have.
		if in.FilamentWastedGrams != nil {
			waste := make(map[filamentKey]float64)
			filamentSplit(waste, job.Material, decodeColours(job.Colours), *in.FilamentWastedGrams)
			if err := s.adjustFilamentByColour(ctx, q, waste, -1); err != nil {
				return err
			}
		}
		reprint, err = q.InsertProductionJob(ctx, reprintParams)
		if err != nil {
			return err
		}
		if err := recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventPrintFailed, Stage: production.FailureStagePrint,
			Reason: in.Reason, Comment: in.Notes, ActorID: actor, BatchID: job.BatchID,
		}); err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventReprintCreated, Stage: production.FailureStagePrint,
			Reason: in.Reason, ActorID: actor, RelatedJobID: &reprint.ID,
			Metadata: map[string]any{"quantity": reprint.Quantity, "job_number": reprint.JobNumber},
		})
	})
	if err != nil {
		return failed, reprint, statusErrf(http.StatusInternalServerError, "Could not fail the production job.", err)
	}
	// A fresh urgent reprint just entered the queue - worth a replan regardless
	// of how much else is currently queued, unlike the threshold-gated per-job
	// triggers (see triggerBatchPlan's doc comment).
	s.triggerBatchPlan(ctx)
	return failed, reprint, nil
}

// updateBatchAnd is SetBatchStatus's body with the params left open, so
// patchBatch can also set the machine in the same UPDATE.
func (s *Server) updateBatchAnd(ctx context.Context, params gen.UpdateBatchParams, completing bool) (gen.Batch, error) {
	var b gen.Batch
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		b, err = q.UpdateBatch(ctx, params)
		if err != nil {
			return err
		}
		if completing {
			if _, err := q.CompleteProductionJobsForBatch(ctx, &params.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return gen.Batch{}, statusErrf(http.StatusInternalServerError, "Could not update the batch.", err)
	}
	if completing {
		// A machine just freed up - worth a replan regardless of how much else
		// is queued, unlike the threshold-gated per-job triggers (see
		// triggerBatchPlan's doc comment).
		s.triggerBatchPlan(ctx)
	}
	return b, nil
}
