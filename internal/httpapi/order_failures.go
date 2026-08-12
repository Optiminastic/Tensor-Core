package httpapi

// Visibility for an order whose job creation gave up.
//
// A create_jobs_from_order job that exhausts its River attempts is discarded,
// and nothing else in the system reacts: the order keeps status 'queued', has
// zero production jobs, and carries no record of what went wrong. The only way
// to notice was for a human to spot the absence.
//
// Two complementary surfaces, covering disjoint failure modes:
//   - orders.job_creation_error records WHY the worker gave up, which only the
//     worker knows.
//   - ListOrdersWithoutJobs finds orders the worker never reached at all - one
//     whose River job was never enqueued, or whose line_items were empty so it
//     "succeeded" with nothing to show. The column can never know about those.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// orderFailureTimeout bounds the detached write below. Short, for the same
// reason as slicing.failJobTimeout: this runs on a path that has already
// failed, and holding a worker slot open for it helps nobody.
const orderFailureTimeout = 10 * time.Second

// markOrderJobCreationFailed records why job creation gave up on an order.
//
// It runs on a context detached from the caller's. The commonest way to reach
// the final attempt is River cancelling the job on its timeout, and a cancelled
// context cannot commit - so writing the marker on the caller's context would
// silently do nothing on exactly the path that most needs it. The error is
// returned so the worker can log it; a failure to record a failure must not
// itself become a retry.
func (s *Server) markOrderJobCreationFailed(ctx context.Context, orderID uuid.UUID, cause error) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orderFailureTimeout)
	defer cancel()

	return s.store.Q.MarkOrderJobCreationFailed(writeCtx, gen.MarkOrderJobCreationFailedParams{
		ID: orderID, Reason: ptr(cause.Error()),
	})
}
