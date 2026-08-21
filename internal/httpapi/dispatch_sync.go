package httpapi

// Bringing print status back from BamBuddy into Tensor.
//
// Tensor POLLS rather than accepting a webhook. With the printer manager on the
// shop LAN behind a tunnel, the outbound direction is the one that already works;
// an inbound webhook would mean exposing a public endpoint whose only practical
// credential is a secret in a URL. A poll is also self-healing: a missed webhook
// is lost forever, while the next poll simply catches up after the link drops.
//
// This is what finally feeds the `machines` fleet table. Migration 0019 gave it
// live-print columns and sqlc gave it mutations, but nothing has ever called
// them - the dashboard has been rendering an empty live view ever since.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// syncActor is the author recorded on rows this loop writes, so a machine-made
// failure is never mistaken for an operator's.
const syncActor = "bambuddy-sync"

// SyncPrintDispatches brings every live dispatch up to date. One BamBuddy failure
// does not abort the rest: each dispatch is independent, and a printer being
// briefly unreachable should not stall the others.
func (s *Server) SyncPrintDispatches(ctx context.Context) error {
	if !s.cfg.BambuddyConfigured() {
		return nil
	}
	open, err := s.store.Q.ListOpenPrintDispatches(ctx)
	if err != nil {
		return fmt.Errorf("list open dispatches: %w", err)
	}
	log := obs.FromContext(ctx)
	for _, d := range open {
		if err := s.syncDispatch(ctx, d); err != nil {
			log.Error("dispatch sync failed", "dispatch", d.ID, "error", err)
		}
	}
	return nil
}

// syncDispatch advances one dispatch from BamBuddy's view of its queue item.
func (s *Server) syncDispatch(ctx context.Context, d gen.PrintDispatch) error {
	// Not queued yet (upload still pending): nothing to ask about.
	if d.QueueItemID == nil {
		return nil
	}
	item, err := s.bambuddy.GetQueueItem(ctx, s.cfg.BambuddyAPIKey, int(*d.QueueItemID))
	if err != nil {
		return fmt.Errorf("read queue item %d: %w", *d.QueueItemID, err)
	}

	switch item.Status {
	case bambuddy.StatusPrinting:
		return s.onPrintStarted(ctx, d)

	case bambuddy.StatusCompleted:
		return s.onPrintCompleted(ctx, d, item)

	case bambuddy.StatusFailed, bambuddy.StatusCancelled, bambuddy.StatusSkipped:
		return s.onPrintFailed(ctx, d, item)

	default:
		// Still pending - staged and waiting for someone to press start. Refresh
		// the fleet view anyway so an operator can see the printer is idle and
		// holding work.
		s.refreshFleetState(ctx, d, nil)
		return nil
	}
}

// onPrintStarted moves the batch and its jobs into production and writes the live
// print state onto the fleet machine.
func (s *Server) onPrintStarted(ctx context.Context, d gen.PrintDispatch) error {
	if d.Status != dispatchPrinting {
		if err := s.store.Q.MarkPrintDispatchPrinting(ctx, d.ID); err != nil {
			return fmt.Errorf("mark printing: %w", err)
		}
		if err := s.setBatchStatus(ctx, d.BatchID, production.BatchInProgress); err != nil {
			return err
		}
		if err := s.setJobStatuses(ctx, d.BatchID, production.StatusInProduction); err != nil {
			return err
		}
	}
	s.refreshFleetState(ctx, d, &d.BatchID)
	return nil
}

// onPrintCompleted closes out a finished print: the batch and its jobs complete,
// the machine goes idle, and the filament it consumed leaves inventory.
func (s *Server) onPrintCompleted(ctx context.Context, d gen.PrintDispatch, item bambuddy.QueueItem) error {
	if err := s.setBatchStatus(ctx, d.BatchID, production.BatchCompleted); err != nil {
		return err
	}
	if err := s.setJobStatuses(ctx, d.BatchID, production.StatusCompleted); err != nil {
		return err
	}
	// Filament leaves stock only on a completed print, and only with a number
	// from the printer - estimating it here would drift from reality.
	if item.FilamentUsedGrams != nil && *item.FilamentUsedGrams > 0 {
		s.consumeFilament(ctx, d.BatchID, *item.FilamentUsedGrams)
	}
	if err := s.store.Q.MarkPrintDispatchCompleted(ctx, d.ID); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	// Machine is free again: clear the live print state rather than leaving a
	// finished batch pinned to it.
	s.refreshFleetState(ctx, d, nil)
	obs.FromContext(ctx).Info("print completed", "dispatch", d.ID, "batch", d.BatchID)
	return nil
}

// onPrintFailed records the printer's own reason against every job in the batch,
// so a failed plate is visible per job rather than only on the dispatch.
func (s *Server) onPrintFailed(ctx context.Context, d gen.PrintDispatch, item bambuddy.QueueItem) error {
	reason := fmt.Sprintf("BamBuddy reported the print %s", item.Status)
	if item.ErrorMessage != nil && *item.ErrorMessage != "" {
		reason = *item.ErrorMessage
	}

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &d.BatchID)
	if err != nil {
		return fmt.Errorf("list batch jobs: %w", err)
	}
	notes := reason
	for _, j := range jobs {
		if _, err := s.store.Q.InsertProductionJobFailure(ctx, gen.InsertProductionJobFailureParams{
			ID: uuid.New(), JobID: j.ID,
			Stage: production.FailureStagePrint,
			// The catalogue has no "the printer said so" reason; machine_error is
			// the closest existing one, with BamBuddy's text kept in notes.
			Reason:    "machine_error",
			Notes:     &notes,
			CreatedBy: syncActor,
		}); err != nil {
			return fmt.Errorf("record job failure: %w", err)
		}
		if _, err := s.store.Q.SetProductionJobStatus(ctx, gen.SetProductionJobStatusParams{
			ID: j.ID, Status: production.StatusFailed,
		}); err != nil {
			return fmt.Errorf("fail job: %w", err)
		}
	}

	if err := s.store.Q.FailPrintDispatch(ctx, gen.FailPrintDispatchParams{
		ID: d.ID, Error: &reason,
	}); err != nil {
		return fmt.Errorf("fail dispatch: %w", err)
	}
	s.refreshFleetState(ctx, d, nil)
	obs.FromContext(ctx).Warn("print failed", "dispatch", d.ID, "batch", d.BatchID, "reason", reason)
	return nil
}

// refreshFleetState writes BamBuddy's live printer view onto the fleet machine.
// Best-effort: this is a dashboard read model, and failing to refresh it must
// never hold up the pipeline that actually moves work.
func (s *Server) refreshFleetState(ctx context.Context, d gen.PrintDispatch, batchID *uuid.UUID) {
	log := obs.FromContext(ctx)
	machine, err := s.store.Q.GetFleetMachineByBambuddyPrinter(ctx, &d.PrinterID)
	if err != nil {
		log.Warn("no fleet machine for printer", "printer", d.PrinterID)
		return
	}
	status, err := s.bambuddy.GetPrinterStatus(ctx, s.cfg.BambuddyAPIKey, int(d.PrinterID))
	if err != nil {
		log.Warn("could not read printer status", "printer", d.PrinterID, "error", err)
		return
	}

	params := gen.UpdateFleetMachineStateParams{ID: machine.ID, Status: fleetStatus(status, batchID)}
	if batchID != nil {
		params.CurrentBatchID = batchID
		params.CurrentLayer = int32Ptr(status.LayerNum)
		params.TotalLayers = int32Ptr(status.TotalLayers)
		params.PrintStartedAt = machine.PrintStartedAt
		if params.PrintStartedAt.Time.IsZero() && d.StartedAt.Valid {
			params.PrintStartedAt = d.StartedAt
		}
	}
	if _, err := s.store.Q.UpdateFleetMachineState(ctx, params); err != nil {
		log.Warn("could not update fleet state", "machine", machine.ID, "error", err)
		return
	}

	// The AMS trays are the shop's real filament picture; mirror them so the
	// fleet view shows what is actually loaded.
	if trays := amsFilaments(status); trays != nil {
		if _, err := s.store.Q.UpdateFleetMachineFilaments(ctx, gen.UpdateFleetMachineFilamentsParams{
			ID: machine.ID, Filaments: trays,
		}); err != nil {
			log.Warn("could not update fleet filaments", "machine", machine.ID, "error", err)
		}
	}
}

// fleetStatus maps BamBuddy's view onto the fleet table's three-value status.
// The column is CHECK-constrained to idle/running/off, so nothing else is valid.
func fleetStatus(status bambuddy.PrinterStatus, batchID *uuid.UUID) string {
	if !status.Connected {
		return "off"
	}
	if batchID != nil {
		return "running"
	}
	return "idle"
}

// amsFilaments renders the printer's AMS trays into the shape the fleet row
// stores. Returns nil when the printer reports no trays, so an existing record
// is left alone rather than blanked.
func amsFilaments(status bambuddy.PrinterStatus) []byte {
	// remaining_percent, not remaining_grams: the printer reports spool fullness
	// as a percentage. Recording it as grams would put a wrong number straight
	// into an inventory field.
	type tray struct {
		Colour           string `json:"colour"`
		Type             string `json:"type"`
		RemainingPercent int    `json:"remaining_percent"`
	}
	out := make([]tray, 0, 4)
	for _, unit := range status.AMS {
		for _, t := range unit.Tray {
			if t.TrayType == nil && t.TrayColor == nil {
				continue
			}
			out = append(out, tray{
				Colour:           derefStr(t.TrayColor),
				Type:             derefStr(t.TrayType),
				RemainingPercent: t.Remain,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

// setBatchStatus moves a batch along without disturbing its other fields.
func (s *Server) setBatchStatus(ctx context.Context, batchID uuid.UUID, status string) error {
	if _, err := s.store.Q.UpdateBatch(ctx, gen.UpdateBatchParams{ID: batchID, Status: &status}); err != nil {
		return fmt.Errorf("set batch status %s: %w", status, err)
	}
	return nil
}

// setJobStatuses moves every job in a batch to the same status.
func (s *Server) setJobStatuses(ctx context.Context, batchID uuid.UUID, status string) error {
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		return fmt.Errorf("list batch jobs: %w", err)
	}
	for _, j := range jobs {
		if j.Status == status {
			continue
		}
		if _, err := s.store.Q.SetProductionJobStatus(ctx, gen.SetProductionJobStatusParams{
			ID: j.ID, Status: status,
		}); err != nil {
			return fmt.Errorf("set job status: %w", err)
		}
	}
	return nil
}

// consumeFilament takes a finished plate's filament out of inventory, against the
// material the batch's jobs were planned with. Best-effort: inventory drift must
// not stop a completed print from being recorded as complete.
func (s *Server) consumeFilament(ctx context.Context, batchID uuid.UUID, grams float64) {
	log := obs.FromContext(ctx)
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil || len(jobs) == 0 {
		return
	}
	material := batchMaterialFromJobs(jobs)
	if material == nil {
		log.Warn("no material on a completed batch; filament not decremented", "batch", batchID)
		return
	}
	if err := s.store.Q.AdjustFilamentStock(ctx, gen.AdjustFilamentStockParams{
		Material: *material, Colour: jobs[0].Colour, Delta: -grams,
	}); err != nil {
		log.Warn("could not decrement filament", "batch", batchID, "error", err)
	}
}

// int32Ptr narrows an optional int from the BamBuddy client to the int32 the
// database columns use.
func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v)
	return &n
}
