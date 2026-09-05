package httpapi

// The River worker that keeps the machines table honest without anyone pressing
// Sync.
//
// Before this, `machines.status` / `current_layer` only moved when an operator
// clicked "Sync from BambuBuddy". That is not just a stale UI: the batch planner
// and machine scheduler read these rows, so a printer that finished an hour ago
// still looked busy to the thing deciding what to print next.
//
// It runs the prune-FREE refresh. See RefreshFleetFromBambuBuddy for why a timer
// must never carry the reconciliation that deletes rows.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// FleetSyncWorker refreshes the fleet from BambuBuddy once per River job.
type FleetSyncWorker struct {
	river.WorkerDefaults[production.SyncFleetArgs]

	server  *Server
	logger  *slog.Logger
	timeout time.Duration
}

// NewFleetSyncWorker builds the worker. logger may be nil (falls back to
// slog's default).
func NewFleetSyncWorker(server *Server, logger *slog.Logger, timeout time.Duration) *FleetSyncWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &FleetSyncWorker{server: server, logger: logger, timeout: timeout}
}

// Timeout overrides River's one-minute JobTimeoutDefault.
//
// A refresh is 1 + N SEQUENTIAL upstream calls, each with the client's own 10s
// timeout. A fleet of six unreachable printers therefore needs longer than a
// minute just to finish failing, and would be cancelled mid-pass every time -
// permanently, since the next attempt hits the same wall.
func (w *FleetSyncWorker) Timeout(*river.Job[production.SyncFleetArgs]) time.Duration {
	return w.timeout
}

// Work runs one refresh pass.
func (w *FleetSyncWorker) Work(ctx context.Context, job *river.Job[production.SyncFleetArgs]) error {
	// Not an error: an install with no BambuBuddy is a valid deployment, and
	// returning one here would accrue a failed River job every interval
	// forever. The same distinction the HTTP route makes with its 409.
	if !w.server.BambuConfigured() {
		return nil
	}

	result, err := w.server.RefreshFleetFromBambuBuddy(ctx)
	if err != nil {
		// Returned so River retries with backoff. A printer host on a laptop
		// over a VPN is expected to be unreachable sometimes; the next tick
		// picks it up regardless.
		w.logger.Warn("fleet refresh failed", "attempt", job.Attempt, "error", err)
		return fmt.Errorf("refresh fleet: %w", err)
	}

	// The same trip to BambuBuddy answers a second question: how long the plates
	// it has sliced actually take. Nothing else asks, so without this every bed
	// carries no measured time and the machine scheduler projects availability
	// from an approximation - see plate_measurement.go.
	//
	// After the refresh, not instead of it: a failed refresh is worth retrying on
	// its own, and a measurement pass that never runs costs nothing beyond a
	// later number.
	plates := w.server.RecordPlateMeasurements(ctx)
	if plates.Measured > 0 || plates.Missing > 0 {
		w.logger.Info("plate measurements recorded",
			"measured", plates.Measured, "pending", plates.Pending,
			"missing", plates.Missing, "awaiting", plates.Awaiting)
	}

	w.logger.Info("fleet refreshed", "printers", result.Synced, "attempt", job.Attempt)
	return nil
}
