package httpapi

// The River worker behind the print-status poll. It runs on a periodic tick
// registered in cmd/productionworker rather than continuously, so an idle shop
// costs one cheap query per interval and nothing else.

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// SyncDispatchesWorker refreshes every live print dispatch per job.
type SyncDispatchesWorker struct {
	river.WorkerDefaults[production.SyncDispatchesArgs]

	server *Server
	logger *slog.Logger
}

// NewSyncDispatchesWorker builds the worker. logger may be nil (falls back to
// slog's default).
func NewSyncDispatchesWorker(server *Server, logger *slog.Logger) *SyncDispatchesWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncDispatchesWorker{server: server, logger: logger}
}

// Work runs one poll.
//
// It returns nil even when individual dispatches failed to refresh: those are
// logged inside SyncPrintDispatches, and retrying the whole pass would just
// re-poll the ones that already succeeded. The next tick is the retry.
func (w *SyncDispatchesWorker) Work(ctx context.Context, job *river.Job[production.SyncDispatchesArgs]) error {
	if err := w.server.SyncPrintDispatches(ctx); err != nil {
		w.logger.Error("print status sync failed", "error", err)
	}
	return nil
}
