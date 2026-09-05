package httpapi

// The River worker that renders a personalised model for one production job
// (see internal/production/queue.go's GenerateModelArgs).
//
// Its own queue, because a render is a 20-45 second OpenSCAD subprocess and
// job creation must not queue behind one - the same separation of resource
// profiles that puts cmd/sliceworker in its own process.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// modelGenOverhead is the slack between OpenSCAD's own deadline and River's.
//
// River's must sit OUTSIDE the subprocess's, or River kills the job first and
// the log records a cancelled worker instead of the render's own explanation.
// Same reasoning as slicing.sliceOverhead.
const modelGenOverhead = time.Minute

// ModelGenWorker renders one job's model per River job.
type ModelGenWorker struct {
	river.WorkerDefaults[production.GenerateModelArgs]

	server *Server
	logger *slog.Logger
}

func NewModelGenWorker(server *Server, logger *slog.Logger) *ModelGenWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ModelGenWorker{server: server, logger: logger}
}

// Timeout keeps River's deadline outside OpenSCAD's own.
func (w *ModelGenWorker) Timeout(*river.Job[production.GenerateModelArgs]) time.Duration {
	return personalise.DefaultTimeout + modelGenOverhead
}

// Work renders, stores and attaches one job's model.
//
// A product with no template is not a failure - most products are ordinary
// designs with an uploaded STL - so it returns nil rather than retrying.
//
// Every other failure records the reason on the job on the final attempt. A
// render fails for reasons retrying cannot fix (a name OpenSCAD cannot set, a
// missing personalisation option, a template bug), so the job is held with an
// explanation a person can act on instead of burning a worker slot on a 45s
// render that will fail identically every time.
func (w *ModelGenWorker) Work(ctx context.Context, job *river.Job[production.GenerateModelArgs]) error {
	jobID := job.Args.JobID
	w.logger.Info("model generation start", "job", jobID, "attempt", job.Attempt)

	err := w.server.GenerateModelForJob(ctx, jobID)
	if errors.Is(err, errNotPersonalisable) {
		w.logger.Info("model generation skipped: not a generated product", "job", jobID)
		return nil
	}
	if err != nil {
		w.logger.Error("model generation failed",
			"job", jobID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			// Best effort: failing to record the failure must not itself
			// become something River retries.
			if markErr := w.server.holdJobForModelFailure(ctx, jobID, err); markErr != nil {
				w.logger.Error("could not record the render failure",
					"job", jobID, "error", markErr)
			}
		}
		return fmt.Errorf("generate model for job %s: %w", jobID, err)
	}

	w.logger.Info("model generation done", "job", jobID)
	// The model just cleared this job's stl_missing, so it is batchable now.
	w.server.triggerBatchPlanIfThresholdMet(ctx)
	return nil
}
