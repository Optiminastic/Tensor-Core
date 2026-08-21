package httpapi

// The River worker that consumes production.CreateJobsArgs - the stage that
// turns an imported Shopify order into production jobs without an operator
// pressing anything. Enqueued transactionally by the orders/paid webhook, so an
// order and its scheduled job creation commit together.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// JobCreationWorker builds production jobs for one order per River job. It holds
// only the Server, so River can run several copies of Work concurrently.
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

// Work creates every production job for one order.
//
// errJobsAlreadyCreated is treated as success rather than a retry: a webhook
// replay, or an operator clicking Create Job while this job is queued, both land
// on the same idempotent outcome the HTTP route already guarantees. Any other
// error is returned so River retries with backoff, matching SliceWorker.
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
	return nil
}
