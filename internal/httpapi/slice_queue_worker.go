package httpapi

// Waiting for a slice, then putting the result on the printer that was chosen.
//
// The second half of a send. Tensor asks BambuBuddy to slice a plate with one
// printer's presets and that printer's own spool colours; the slice answers 202
// and finishes minutes later. Only the finished slice has a library file id, and
// only a queue item carries printer_id - so the binding to a machine can only
// happen here, and an HTTP request cannot be held open long enough to do it.
//
// Returning an error asks River to retry with backoff, which is the NORMAL path
// for the first several attempts: "still slicing" is the expected answer, not a
// fault.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// recordFailureTimeout bounds the detached write that records a final failure.
// Reaching the last attempt usually means River cancelled the job, and a
// cancelled context cannot commit.
const recordFailureTimeout = 10 * time.Second

type SliceQueueWorker struct {
	river.WorkerDefaults[production.QueueSlicedPlateArgs]

	server *Server
	logger *slog.Logger
}

// NewSliceQueueWorker builds the worker. logger may be nil (falls back to
// slog's default).
func NewSliceQueueWorker(server *Server, logger *slog.Logger) *SliceQueueWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SliceQueueWorker{server: server, logger: logger}
}

func (w *SliceQueueWorker) Work(ctx context.Context, job *river.Job[production.QueueSlicedPlateArgs]) error {
	a := job.Args

	if !w.server.bambu.Configured() {
		return nil // nothing to queue against, and retrying cannot change that
	}

	slice, err := w.server.bambu.GetSliceJob(ctx, a.SliceJobID)
	if err != nil {
		return w.retryOrGiveUp(ctx, job, fmt.Errorf("read slice job %d: %w", a.SliceJobID, err))
	}

	if slice.Failed() {
		// It will never produce a file. Say why on the batch and release it so
		// the bed can be sent again, rather than leaving it permanently
		// un-sendable with a slice nobody is running.
		reason := strings.TrimSpace(slice.ErrorMessage)
		if reason == "" {
			reason = fmt.Sprintf("BambuBuddy could not slice this plate for %s.", a.MachineName)
		}
		w.releaseAfterFailure(ctx, a.BatchID, reason)
		w.logger.Warn("slice failed; the bed is sendable again",
			"batch", a.BatchID, "slice_job", a.SliceJobID, "reason", reason)
		return nil
	}

	if !slice.Done() {
		// The ordinary answer early on. River backs off and asks again.
		return w.retryOrGiveUp(ctx, job,
			fmt.Errorf("slice job %d has not finished yet", a.SliceJobID))
	}
	slicedFileID := slice.Result.LibraryFileID

	// The spools may have moved while the slice ran, and the plate is now
	// sliced declaring the colours that WERE loaded. Queueing it anyway would
	// print slot 2 from whatever is in that tray now.
	if err := w.trayCheck(ctx, a); err != nil {
		w.releaseAfterFailure(ctx, a.BatchID, err.Error())
		w.logger.Warn("spools moved while the plate was slicing; not queued",
			"batch", a.BatchID, "machine", a.MachineName, "error", err)
		return nil
	}

	// What the sliced file itself says it needs, for BambuBuddy's own check.
	// Best-effort: losing it costs the extra check, not the print.
	var requiredTypes []string
	if req, err := w.server.bambu.FilamentRequirements(ctx, slicedFileID); err == nil {
		requiredTypes = req.Types()
	} else {
		w.logger.Info("could not read the sliced plate's filament requirements",
			"file", slicedFileID, "error", err)
	}

	item, err := w.server.bambu.QueueForPrinting(ctx, slicedFileID, bambubuddy.QueueOptions{
		PrinterID:             a.PrinterID,
		AMSMapping:            a.AmsMapping,
		RequiredFilamentTypes: requiredTypes,
		// Never true. The whole point of slicing against this printer's trays is
		// that the check passes honestly; forcing past it would reinstate the
		// failure this path exists to remove.
		SkipFilamentCheck: false,
		// Stated rather than left to the zero value, because the opposite is
		// the tempting choice and it is wrong here: BambuBuddy holds the entire
		// queue behind one failed print and skips what is waiting, so a single
		// bad first layer stops the printer silently. A bed that cannot print
		// will fail on its own and say so.
		RequirePreviousSuccess: false,
	})
	if err != nil {
		return w.retryOrGiveUp(ctx, job, fmt.Errorf("queue sliced plate on %s: %w", a.MachineName, err))
	}

	queueID := int32Ptr(item.ID)
	if err := w.server.store.Q.ClearBatchPrintError(ctx, gen.ClearBatchPrintErrorParams{
		ID: a.BatchID, QueueItemID: queueID,
	}); err != nil {
		// The plate is queued on the right printer, which is what matters on the
		// floor. Failing the job would re-queue it and print the bed twice.
		w.logger.Warn("queued the plate but could not record its queue item",
			"batch", a.BatchID, "queue_item", item.ID, "error", err)
	}
	if err := w.server.store.Q.ClearBatchSliceJob(ctx, a.BatchID); err != nil {
		w.logger.Warn("could not clear the slice job id", "batch", a.BatchID, "error", err)
	}

	w.logger.Info("sliced plate queued on the chosen printer",
		"batch", a.BatchID, "machine", a.MachineName, "printer", a.PrinterID,
		"queue_item", item.ID, "file", slicedFileID, "ams_mapping", a.AmsMapping)
	return nil
}

// trayCheck confirms the spools this plate was sliced for are still in place.
//
// Compares by POSITION as well as colour: the mapping names a tray for each
// plate slot, so a spool swapped between two slots is as wrong as one removed,
// even though the machine still "holds" both colours.
func (w *SliceQueueWorker) trayCheck(ctx context.Context, a production.QueueSlicedPlateArgs) error {
	machine, err := w.server.store.Q.GetFleetMachine(ctx, a.MachineID)
	if err != nil {
		// Cannot prove it moved; do not block the print on a database blip.
		w.logger.Info("could not re-read the machine before queueing", "machine", a.MachineName, "error", err)
		return nil
	}

	nowHolds := map[int]string{}
	for _, tray := range w.server.liveTraysFor(ctx, machine) {
		index, ok := amsSlotIndex(tray)
		if !ok {
			continue
		}
		if hex, ok := normaliseHex(tray.Colour); ok {
			nowHolds[index] = hex
		}
	}
	if len(nowHolds) == 0 {
		return nil // nothing readable to compare against
	}

	for i, index := range a.AmsMapping {
		if i >= len(a.TrayHexes) {
			break
		}
		want := a.TrayHexes[i]
		got, present := nowHolds[index]
		if !present || got != want {
			return fmt.Errorf(
				"%s no longer holds %s in the slot this plate was sliced for; load it back or send the bed again",
				a.MachineName, want)
		}
	}
	return nil
}

// releaseAfterFailure records why and makes the bed sendable again.
func (w *SliceQueueWorker) releaseAfterFailure(ctx context.Context, batchID uuid.UUID, reason string) {
	w.server.recordPrintError(ctx, batchID, reason)
	if err := w.server.store.Q.ClearBatchSliceJob(ctx, batchID); err != nil {
		w.logger.Warn("could not release the bed after a failed slice", "batch", batchID, "error", err)
	}
}

// retryOrGiveUp returns the error so River retries, and on the final attempt
// leaves the reason on the batch rather than letting the bed sit dispatched
// with nothing running.
func (w *SliceQueueWorker) retryOrGiveUp(
	ctx context.Context, job *river.Job[production.QueueSlicedPlateArgs], cause error,
) error {
	if job.Attempt < job.MaxAttempts {
		return cause
	}
	// Detached: reaching the last attempt usually means River cancelled the job,
	// and a cancelled context cannot commit.
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordFailureTimeout)
	defer cancel()
	w.releaseAfterFailure(failCtx, job.Args.BatchID, fmt.Sprintf(
		"Gave up waiting for BambuBuddy to slice this plate for %s: %v", job.Args.MachineName, cause))
	return nil
}
