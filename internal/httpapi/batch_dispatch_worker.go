package httpapi

// The River worker that consumes production.DispatchBatchesArgs: one pass of
// DispatchReadyBatches, which approves a Draft or sends an approved and sliced
// plate to BambuBuddy, oldest order first.

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// DispatchWorker advances batches toward a printer. It holds only the Server,
// matching BatchPlanWorker; the pass itself is idempotent, so a second
// concurrent run simply finds less to do.
type DispatchWorker struct {
	river.WorkerDefaults[production.DispatchBatchesArgs]

	server *Server
	logger *slog.Logger
}

// NewDispatchWorker builds the worker. logger may be nil (falls back to slog's
// default).
func NewDispatchWorker(server *Server, logger *slog.Logger) *DispatchWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &DispatchWorker{server: server, logger: logger}
}

// Work runs one dispatch pass.
//
// Never returns an error, which is the difference from BatchPlanWorker. A batch
// that cannot be approved or sent has its reason recorded on the batch itself
// and is skipped; there is nothing for River to retry, and returning an error
// would re-run a pass that has already done its successful half - re-uploading
// plates that reached BambuBuddy on the first attempt.
func (w *DispatchWorker) Work(ctx context.Context, job *river.Job[production.DispatchBatchesArgs]) error {
	out := w.server.DispatchReadyBatches(ctx)
	if out.Considered == 0 {
		return nil
	}
	w.logger.Info("batch dispatch pass",
		"attempt", job.Attempt, "considered", out.Considered,
		"approved", out.Approved, "sent", out.Sent,
		"held_open", out.HeldOpen, "failed", out.Failed)
	return nil
}
