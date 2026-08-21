package httpapi

// The River worker that consumes production.PlanBatchesArgs - the batch grouping
// and optimisation pass, via Server.AutoCreateBatches. It runs in the background
// on a debounced trigger (see triggerBatchPlan) rather than synchronously on the
// request that made new jobs batchable, so "a job became ready" stays separate
// from "replan everything batchable".

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// BatchPlanWorker replans every batchable job per River job. It holds only the
// Server; in practice River never runs two copies at once because the triggers
// are debounced (see production.BatchPlanEnqueuer), but nothing here requires
// that - a second concurrent run simply finds fewer batchable jobs.
type BatchPlanWorker struct {
	river.WorkerDefaults[production.PlanBatchesArgs]

	server *Server
	logger *slog.Logger
}

// NewBatchPlanWorker builds the worker. logger may be nil (falls back to slog's
// default).
func NewBatchPlanWorker(server *Server, logger *slog.Logger) *BatchPlanWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &BatchPlanWorker{server: server, logger: logger}
}

// Work runs one replan pass. Any error tells River to retry with backoff,
// matching JobCreationWorker's convention.
func (w *BatchPlanWorker) Work(ctx context.Context, job *river.Job[production.PlanBatchesArgs]) error {
	w.logger.Info("batch plan start", "attempt", job.Attempt)

	created, unbatchable, err := w.server.AutoCreateBatches(ctx)
	if err != nil {
		w.logger.Error("batch plan failed", "attempt", job.Attempt, "error", err)
		return fmt.Errorf("auto create batches: %w", err)
	}

	w.logger.Info("batch plan done", "batches", len(created), "unbatchable", len(unbatchable))
	return nil
}
