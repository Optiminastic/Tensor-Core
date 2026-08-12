package slicing

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// TestSliceWorkerTimeoutExceedsSliceDeadline is a regression lock, not a
// tautology. Before this existed the worker only embedded
// river.WorkerDefaults, whose Timeout() returns zero - which River reads as
// "use JobTimeoutDefault", one minute. So the configured 300s slice deadline
// could never fire: River cancelled the context first and the failure surfaced
// as "slicer produced no result.json" rather than a timeout.
//
// The assertion that matters is the inequality. If anyone drops Timeout()
// again, or derives it from something smaller than the slice deadline, this
// fails.
func TestSliceWorkerTimeoutExceedsSliceDeadline(t *testing.T) {
	for _, sliceTimeout := range []time.Duration{30 * time.Second, 300 * time.Second, 20 * time.Minute} {
		w := NewSliceWorker(nil, nil, "", sliceTimeout, 0.1, orientation.Options{}, nil, true)

		got := w.Timeout(nil)
		if got <= sliceTimeout {
			t.Errorf("Timeout() = %s for a %s slice deadline; River's deadline must outlast the slicer's own or the inner timeout can never fire",
				got, sliceTimeout)
		}
		if want := sliceTimeout + sliceOverhead; got != want {
			t.Errorf("Timeout() = %s, want %s (slice deadline + overhead)", got, want)
		}
	}
}
