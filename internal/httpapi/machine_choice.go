package httpapi

// Choosing the physical printer a plate goes to: the one free soonest that has
// the right filament in it.
//
// BambuBuddy already decides this for itself, by fanout strategy across a
// class. The shop wants a different rule and a stated one: whichever machine
// finishes what it is doing first, counting everything already queued behind
// it, among the machines whose AMS actually holds the plate's colours.
//
// The two halves are kept apart on purpose. The RULE is pure and lives in
// production.PickEarliestFree, where it can be tested without a printer. This
// file is only the errand: gather a snapshot, hand it over, and act on the
// answer.
//
// Acting on it is a PATCH after the fact rather than an instruction up front,
// because a pipeline run cannot be told which printer to use - its payload
// carries the file, a copy count and a force flag, nothing else. So the plate is
// sliced for its class, and the item that comes out is then pointed at the
// machine Tensor picked. That order is also the safe one: slicing depends on the
// class, and the class is not what we are overriding.

import (
	"context"
	"fmt"
	"time"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// fleetSnapshot reads every printer's live state and the queue behind it.
//
// One queue read for the whole fleet, one status read per printer. Status is not
// available in bulk, and thirteen small calls on a local tailnet is a few
// hundred milliseconds - worth it, because the alternative is scheduling from
// the machines table, which the fleet sync writes minutes apart.
func (s *Server) fleetSnapshot(ctx context.Context) ([]production.MachineState, error) {
	printers, err := s.bambu.ListPrinters(ctx)
	if err != nil {
		return nil, err
	}
	queue, err := s.bambu.ListQueue(ctx)
	if err != nil {
		return nil, err
	}

	// Everything still waiting, summed per printer. Only work that has yet to
	// run counts: a completed or cancelled item is not ahead of anything.
	queued := map[int]time.Duration{}
	for _, item := range queue {
		if item.PrinterID == nil || !item.Waiting() {
			continue
		}
		queued[*item.PrinterID] += time.Duration(item.PrintTimeSeconds) * time.Second
	}

	out := make([]production.MachineState, 0, len(printers))
	for _, p := range printers {
		state := production.MachineState{
			PrinterID: p.ID, Name: p.Name, Model: p.Model,
			QueuedAhead: queued[p.ID],
		}
		status, err := s.bambu.GetStatus(ctx, p.ID)
		if err != nil {
			// Unreachable, so not a candidate - and deliberately not treated as
			// idle, which is what a zero-value state would have meant.
			obs.FromContext(ctx).Debug("could not read a printer's status for scheduling",
				"printer", p.Name, "error", err)
			out = append(out, state)
			continue
		}
		state.Online = status.Connected
		// RemainingTime is minutes, and only means anything while it prints.
		if status.Printing() && status.RemainingTime > 0 {
			state.RemainingOnCurrent = time.Duration(status.RemainingTime) * time.Minute
		}
		for _, ams := range status.AMS {
			for _, tray := range ams.Trays {
				if tray.Exists && tray.Colour != "" {
					state.LoadedColours = append(state.LoadedColours, tray.Colour)
				}
			}
		}
		out = append(out, state)
	}
	return out, nil
}

// assignQueuedItemToBestMachine points a freshly queued plate at the printer
// that will be free soonest and holds its colours.
//
// Best-effort by design, and it returns a note rather than an error. The plate
// is already sliced and queued by this point: if the snapshot cannot be read, or
// no machine has the colours, the RIGHT outcome is that BambuBuddy's own fanout
// keeps the item - not that Tensor withdraws a plate it has already committed.
// The note travels back to the operator either way.
func (s *Server) assignQueuedItemToBestMachine(
	ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob, queueItemID int,
) string {
	log := obs.FromContext(ctx)

	required := s.plateColourHexes(ctx, jobs)
	machines, err := s.fleetSnapshot(ctx)
	if err != nil {
		log.Warn("could not read the fleet to choose a printer",
			"batch", batch.BatchNumber, "error", err)
		return ""
	}

	choice := production.PickEarliestFree(machines, required)
	if choice.Machine == nil {
		log.Info("no printer chosen for a queued plate",
			"batch", batch.BatchNumber, "reason", choice.Reason)
		// Said out loud, because it is the one thing an operator can fix: the
		// plate is in BambuBuddy's queue and will sit there until a machine
		// carries the colour.
		return "queued, but " + choice.Reason
	}

	if err := s.bambu.AssignQueueItemToPrinter(ctx, queueItemID, choice.Machine.PrinterID); err != nil {
		log.Warn("could not pin a queued plate to the chosen printer",
			"batch", batch.BatchNumber, "printer", choice.Machine.Name, "error", err)
		return ""
	}
	log.Info("plate assigned to the printer free soonest",
		"batch", batch.BatchNumber, "printer", choice.Machine.Name,
		"free_in_minutes", int(choice.FreeIn.Minutes()))
	return "sent to " + choice.Machine.Name + " (" + freeInWords(choice.FreeIn) + ")"
}

// plateColourHexes are the colours this bed needs loaded, as hex.
//
// Resolved through the same path that colours the model itself, so the printer
// is matched against exactly what the plate declares in its AMS slots rather
// than against a colour NAME the trays do not carry - every tray on this fleet
// reports an empty name and a generic GFL99 profile, so hex is the only thing
// there is to compare.
func (s *Server) plateColourHexes(ctx context.Context, jobs []gen.ProductionJob) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		for _, name := range decodeColours(j.Colours) {
			hex, err := s.resolveColourHex(ctx, name)
			if err != nil || hex == "" {
				continue
			}
			key := production.NormaliseHex(hex)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// freeInWords is a wait an operator can read at a glance.
func freeInWords(d time.Duration) string {
	switch {
	case d <= 0:
		return "free now"
	case d < time.Hour:
		return fmt.Sprintf("free in %dm", int(d.Minutes()))
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("free in %dh", h)
		}
		return fmt.Sprintf("free in %dh %dm", h, m)
	}
}
