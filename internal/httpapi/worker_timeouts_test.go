package httpapi

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/config"
)

// Both workers previously inherited river.WorkerDefaults' zero Timeout(), which
// River reads as its 1-minute JobTimeoutDefault. A replan that merges and
// uploads a plate per created batch does not fit in a minute; it was cancelled
// mid-transaction and retried into the same wall. These lock in both the
// configured value and the fallback for a missing or nonsensical setting.
func TestWorkerTimeoutsUseConfiguredValues(t *testing.T) {
	srv := &Server{cfg: config.Settings{
		BatchPlanTimeoutMinutes:   20,
		JobCreationTimeoutMinutes: 9,
	}}
	batch := &BatchPlanWorker{server: srv}
	creation := &JobCreationWorker{server: srv}

	if got, want := batch.Timeout(nil), 20*time.Minute; got != want {
		t.Errorf("BatchPlanWorker.Timeout() = %s, want %s", got, want)
	}
	if got, want := creation.Timeout(nil), 9*time.Minute; got != want {
		t.Errorf("JobCreationWorker.Timeout() = %s, want %s", got, want)
	}
}

func TestWorkerTimeoutsFallBackWhenUnset(t *testing.T) {
	// Zero is what an unset env var yields, and negative is a typo. Neither may
	// pass through: zero would hand River its 1-minute default (the bug), and
	// negative would mean no timeout at all.
	for _, minutes := range []int{0, -5} {
		srv := &Server{cfg: config.Settings{
			BatchPlanTimeoutMinutes:   minutes,
			JobCreationTimeoutMinutes: minutes,
		}}
		batch := &BatchPlanWorker{server: srv}
		creation := &JobCreationWorker{server: srv}

		if got := batch.Timeout(nil); got != defaultBatchPlanTimeout {
			t.Errorf("BatchPlanWorker.Timeout() with %d minutes = %s, want the %s default",
				minutes, got, defaultBatchPlanTimeout)
		}
		if got := creation.Timeout(nil); got != defaultJobCreationTimeout {
			t.Errorf("JobCreationWorker.Timeout() with %d minutes = %s, want the %s default",
				minutes, got, defaultJobCreationTimeout)
		}
		if batch.Timeout(nil) <= time.Minute || creation.Timeout(nil) <= time.Minute {
			t.Error("a fallback at or below River's 1-minute JobTimeoutDefault defeats the point")
		}
	}
}
