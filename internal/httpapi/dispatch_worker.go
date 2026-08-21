package httpapi

// The River worker that consumes production.DispatchPrintArgs - the stage that
// actually puts a sliced plate on a printer. Enqueued by the plate slice worker
// when a batch gains its G-code, and by the manual retry route.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// PrintDispatchWorker hands one batch's plate to BamBuddy per job.
type PrintDispatchWorker struct {
	river.WorkerDefaults[production.DispatchPrintArgs]

	server *Server
	logger *slog.Logger
}

// NewPrintDispatchWorker builds the worker. logger may be nil (falls back to
// slog's default).
func NewPrintDispatchWorker(server *Server, logger *slog.Logger) *PrintDispatchWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PrintDispatchWorker{server: server, logger: logger}
}

// Work dispatches one batch.
//
// A rejected API key or an unprintable batch is returned as an error so River
// retries with backoff and the reason lands on the dispatch row. What is NOT
// retried is a filament deficit: BamBuddy staged the plate anyway and flagged it,
// so retrying would not change anything a machine can decide.
func (w *PrintDispatchWorker) Work(ctx context.Context, job *river.Job[production.DispatchPrintArgs]) error {
	batchID := job.Args.BatchID
	w.logger.Info("print dispatch start", "batch", batchID, "attempt", job.Attempt)

	err := w.server.DispatchBatch(ctx, batchID)
	if err != nil {
		// A batch that is not dispatchable (unsliced, unmapped printer) will not
		// become dispatchable by retrying. Log it and close the job rather than
		// burning five attempts on a state only a human can change.
		var he *httpErr
		if errors.As(err, &he) && he.status != 500 {
			w.logger.Warn("print dispatch refused", "batch", batchID, "reason", he.msg)
			return nil
		}
		w.logger.Error("print dispatch failed", "batch", batchID, "attempt", job.Attempt, "error", err)
		return fmt.Errorf("dispatch batch %s: %w", batchID, err)
	}

	w.logger.Info("print dispatch done", "batch", batchID)
	return nil
}
