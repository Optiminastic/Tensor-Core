package httpapi

// River job timeouts for the workers in this package.
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
	// A dispatch pass reads a plate, uploads it and asks BambuBuddy to slice
	// it, once per bed, up to BATCH_AUTO_DISPATCH_MAX beds. A plate may be
	// 256 MB (maxPlateBytes) and a slice is minutes, so a minute is not close.
	//
	// This one is worse than being merely too short. The marker that says "this
	// bed is already slicing" is written in the same transaction that schedules
	// its queueing; a deadline firing there rolls the marker back while the
	// slice keeps running on BambuBuddy, so the next pass sees an idle bed and
	// slices it again. And again.
	defaultBatchDispatchTimeout = 20 * time.Minute
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

// Timeout bounds one dispatch pass. See defaultBatchDispatchTimeout.
func (w *DispatchWorker) Timeout(*river.Job[production.DispatchBatchesArgs]) time.Duration {
	return minutesOr(w.server.cfg.BatchDispatchTimeoutMinutes, defaultBatchDispatchTimeout)
}
