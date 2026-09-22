package httpapi

// What is already waiting on each physical printer, as BambuBuddy sees it.
//
// Tensor has a load figure of its own - queuedBatchLoad, which counts batches
// by machine_profile_id - and for ranking one PRINTER against another it is
// unusable. A profile is a slicing config shared by every unit of a model, and
// this fleet runs 14 printers on 4 profiles: five A2L units share one row, five
// P2S another. Every printer of a model is therefore charged the identical
// queue, so that term is a constant within a class and cancels out of the
// comparison entirely. Ranking on it picks a model, which is the thing the
// operator already knows.
//
// BambuBuddy's queue is the only place a per-printer answer exists, because
// QueueItem carries the printer it is bound to. It is also the only place that
// can see a plate somebody queued in BambuBuddy's own UI, which Tensor's batch
// table cannot - and that plate occupies the machine just the same.

import (
	"context"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// queueLoad is the work standing in front of one printer.
type queueLoad struct {
	// Items is how many plates are waiting, used to break a tie between two
	// printers that are both free now.
	Items int
	// Minutes is how long they will take.
	Minutes int
}

// fleetQueueLoad reads the whole queue once and groups it by printer.
//
// One call for the fleet rather than one per candidate: ranking 14 machines is
// a single decision and should cost a single round trip. A failure returns an
// empty map, which reads as "nothing queued anywhere" - the ranking then falls
// back to remaining print time alone, which is degraded but not wrong. Refusing
// to rank because a status read failed would strand every bed.
func (s *Server) fleetQueueLoad(ctx context.Context) map[int]queueLoad {
	items, err := s.bambu.ListQueue(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("fleet queue load unavailable, ranking on print time alone",
			"error", err)
		return map[int]queueLoad{}
	}
	return queueMinutesByPrinter(items)
}

// queueMinutesByPrinter sums the pending work bound to each printer.
//
// Only QueuePending counts. A QueuePrinting item is the plate on the bed right
// now, and its REMAINING time is already carried by machines.remaining_minutes
// - adding its full print time here would charge a machine twice for one plate
// and, worse, charge it the whole print when five minutes are left.
//
// An item bound to no printer is charged to nobody. It is waiting for whichever
// machine fits, so attributing it to one would invent load that does not exist;
// it will bind itself when a printer frees up.
func queueMinutesByPrinter(items []bambubuddy.QueueItem) map[int]queueLoad {
	out := map[int]queueLoad{}
	for _, it := range items {
		if it.Status != bambubuddy.QueuePending || it.PrinterID == nil {
			continue
		}
		load := out[*it.PrinterID]
		load.Items++
		load.Minutes += queueItemMinutes(it)
		out[*it.PrinterID] = load
	}
	return out
}

// queueItemMinutes is how long a queued plate occupies its printer.
//
// A plate with no reported time is charged unestimatedBatchMinutes rather than
// zero, for the reason queuedBatchLoad already records: skipping unknown work
// made the machine holding it look EMPTIER than one holding measured work, so
// it attracted more of it. Unknown is not free.
func queueItemMinutes(it bambubuddy.QueueItem) int {
	if it.PrintTimeSeconds <= 0 {
		return unestimatedBatchMinutes
	}
	// Round up: a plate reporting 30 seconds occupies the machine, and flooring
	// it to zero would make a queue of short plates weigh nothing.
	return (it.PrintTimeSeconds + 59) / 60
}
