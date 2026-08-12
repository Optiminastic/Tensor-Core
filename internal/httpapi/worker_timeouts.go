package httpapi

// River job timeouts for the two workers in this package.
//
// A worker that embeds river.WorkerDefaults inherits a Timeout() of zero, which
// River reads as "use JobTimeoutDefault" - one minute. That is far too short
// for either of these: a replan can merge and upload a plate per created batch,
// and job creation does a design lookup per line item. When the deadline fires
// the job context is cancelled mid-transaction, the work is rolled back, and it
// retries into the same wall.
//
// Both are configured rather than derived. The slice worker can derive its
// timeout from SLICE_TIMEOUT_SECONDS because RunSlice has its own inner
// deadline to sit outside of; neither of these has an equivalent inner bound,
// and their cost scales with the backlog rather than with any single input.

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

const (
	defaultBatchPlanTimeout   = 15 * time.Minute
	defaultJobCreationTimeout = 5 * time.Minute
)

// minutesOr converts a configured minute count to a duration, falling back when
// it is unset or nonsensical - a zero would mean "use River's 1-minute default"
// and a negative would mean "no timeout at all", and neither is what an
// operator who mis-set the env var intended.
func minutesOr(minutes int, fallback time.Duration) time.Duration {
	if minutes <= 0 {
		return fallback
	}
	return time.Duration(minutes) * time.Minute
}

// Timeout bounds one replan pass. See defaultBatchPlanTimeout's rationale above.
func (w *BatchPlanWorker) Timeout(*river.Job[production.PlanBatchesArgs]) time.Duration {
	return minutesOr(w.server.cfg.BatchPlanTimeoutMinutes, defaultBatchPlanTimeout)
}

// Timeout bounds one order's job creation.
func (w *JobCreationWorker) Timeout(*river.Job[production.CreateJobsArgs]) time.Duration {
	return minutesOr(w.server.cfg.JobCreationTimeoutMinutes, defaultJobCreationTimeout)
}
