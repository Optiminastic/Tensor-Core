package httpapi

// The River worker that consumes production.CreateJobsArgs (Stage 2 + Stage 3
// combined - see internal/production/queue.go and Server.CreateJobsForOrder).
// Wraps *Server rather than living in internal/production because it needs
// s.store and s.storage; internal/production stays a pure, DB-free package by
// design (see its package doc comment).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// JobCreationWorker builds production jobs for one order per River job. It
// holds only the Server, so River can run several copies of Work concurrently.
type JobCreationWorker struct {
	river.WorkerDefaults[production.CreateJobsArgs]

	server *Server
	logger *slog.Logger
}

// NewJobCreationWorker builds the worker. logger may be nil (falls back to
// slog's default).
func NewJobCreationWorker(server *Server, logger *slog.Logger) *JobCreationWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &JobCreationWorker{server: server, logger: logger}
}

// Work creates every production job for one order. errJobsAlreadyCreated is
// treated as success (not an error to retry) - a webhook replay or a manual
// backfill racing the worker both land on the same idempotent outcome the HTTP
// endpoint already relies on. Any other error tells River to retry with
// backoff, matching SliceWorker's convention.
func (w *JobCreationWorker) Work(ctx context.Context, job *river.Job[production.CreateJobsArgs]) error {
	orderID := job.Args.OrderID
	w.logger.Info("job creation start", "order", orderID, "attempt", job.Attempt)

	jobs, err := w.server.CreateJobsForOrder(ctx, orderID)
	if errors.Is(err, errJobsAlreadyCreated) {
		w.logger.Info("job creation skipped: jobs already exist", "order", orderID)
		return nil
	}
	if err != nil {
		w.logger.Error("job creation failed", "order", orderID, "attempt", job.Attempt, "error", err)
		return fmt.Errorf("create jobs for order %s: %w", orderID, err)
	}

	w.logger.Info("job creation done", "order", orderID, "jobs", len(jobs))
	w.server.triggerBatchPlanIfThresholdMet(ctx)
	return nil
}
