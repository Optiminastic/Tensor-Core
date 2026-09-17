package httpapi

// Putting the plate on the printer the operator actually chose.
//
// The second half of a two-part move that BambuBuddy's API forces. Running a
// slicer pipeline targets a printer CLASS - its payload takes a file, a copy
// count and a force flag, with nowhere to name a machine - and the queue entry
// that carries printer_id does not exist until the slice finishes, which is
// minutes for a full bed. So the send returns as soon as the plate is accepted,
// and this comes back afterwards to tie it down.
//
// Without it the choice is decorative: BambuBuddy's own dispatcher places the
// plate on whichever printer of that class frees up first, which is exactly the
// automatic behaviour the shop asked to be rid of.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type QueuePinWorker struct {
	river.WorkerDefaults[production.PinQueueItemArgs]

	server *Server
	logger *slog.Logger
}

// NewQueuePinWorker builds the worker. logger may be nil (falls back to slog's
// default).
func NewQueuePinWorker(server *Server, logger *slog.Logger) *QueuePinWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &QueuePinWorker{server: server, logger: logger}
}

// Work pins one queued plate to one printer.
//
// Returning an error asks River to try again with backoff, which is the normal
// path here rather than the exceptional one: "the slice has not finished yet" is
// the expected answer for the first several attempts.
func (w *QueuePinWorker) Work(ctx context.Context, job *river.Job[production.PinQueueItemArgs]) error {
	a := job.Args

	if !w.server.bambu.Configured() {
		// Nothing to pin to, and no amount of retrying will change that.
		return nil
	}

	runs, err := w.server.bambu.ListRunsForPipeline(ctx, a.PipelineID)
	if err != nil {
		return fmt.Errorf("read pipeline %d runs: %w", a.PipelineID, err)
	}

	var run *bambubuddy.PipelineRun
	for i := range runs {
		if runs[i].ID == a.PipelineRunID {
			run = &runs[i]
			break
		}
	}
	if run == nil {
		// The run is gone. Retrying cannot bring it back, and the plate is
		// BambuBuddy's problem now.
		w.logger.Warn("pipeline run vanished before its plate could be pinned",
			"batch", a.BatchID, "pipeline_run", a.PipelineRunID)
		return nil
	}

	itemID := run.QueueEntryID()
	if itemID == nil {
		if run.Finished() {
			// Slicing failed. There will never be a queue entry, so stop.
			w.logger.Warn("slicing finished without queueing anything, nothing to pin",
				"batch", a.BatchID, "pipeline_run", a.PipelineRunID, "status", run.Status)
			return nil
		}
		// The ordinary case early on: still slicing. River backs off and asks
		// again.
		return fmt.Errorf("pipeline run %d has not queued its plate yet", a.PipelineRunID)
	}

	if err := w.server.bambu.AssignQueueItemToPrinter(ctx, *itemID, a.PrinterID); err != nil {
		return fmt.Errorf("pin queue item %d to printer %d: %w", *itemID, a.PrinterID, err)
	}

	// Record the queue item now it exists. The batch was already marked
	// dispatched by its pipeline run at send time; this fills in the id that
	// the machine board and the double-send guard both read.
	if err := w.server.store.Q.ClearBatchPrintError(ctx, gen.ClearBatchPrintErrorParams{
		ID: a.BatchID, QueueItemID: int32Ptr(*itemID), PipelineRunID: int32Ptr(a.PipelineRunID),
	}); err != nil {
		// The pin itself succeeded, which is the part that matters on the floor.
		// Failing the job would repeat the PATCH for a bookkeeping miss.
		w.logger.Warn("pinned the plate but could not record its queue item",
			"batch", a.BatchID, "queue_item", *itemID, "error", err)
	}

	w.logger.Info("plate pinned to the chosen printer",
		"batch", a.BatchID, "machine", a.MachineName,
		"queue_item", *itemID, "printer", a.PrinterID)
	return nil
}
