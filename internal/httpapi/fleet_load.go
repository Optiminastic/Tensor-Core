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
	// Items is how many plates this printer owes, the one it is PRINTING
	// included. Used for RANKING: a printer laying plastic is busier than an
	// idle one, and between two printers with the same projected finish the
	// emptier queue should win.
	//
	// It is no longer the one-bed-per-machine rule - see Pending.
	Items int
	// Pending is how many plates are WAITING in this printer's queue, with the
	// one on the bed excluded.
	//
	// This is the shop's rule: at most one QUEUED batch per machine on
	// BambuBuddy. A printer that is printing may still be handed its next bed,
	// which then starts the moment the current plate comes off - that is the
	// point of a queue, and it is how a machine stops idling between plates.
	//
	// It used to be Items, counting the printing plate as the machine's one
	// bed, so a fleet of thirteen printers that were all mid-print accepted
	// nothing at all. With an empty BambuBuddy queue and every printer running,
	// every bed in Tensor waited on a queue that nobody was filling.
	Pending int
	// Minutes is how long the WAITING ones will take. The plate on the bed is
	// deliberately absent - its remaining time is carried by
	// machines.remaining_minutes and counting it here would charge the printer
	// twice for one plate.
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

// queueMinutesByPrinter sums the work bound to each printer.
//
// The two halves of a queueLoad count different things, and the asymmetry is
// the point.
//
// ITEMS count QueuePending and QueuePrinting alike, because they rank a
// printer's busyness and a plate on the bed is real work in front of the next
// one. PENDING counts only what is waiting, because that is what the shop's
// one-queued-batch-per-machine rule is about.
//
// MINUTES count only the pending ones. A QueuePrinting item's REMAINING time is
// already carried by machines.remaining_minutes, so adding its full print time
// here would charge a machine twice for one plate and, worse, charge it the
// whole print when five minutes are left.
//
// An item bound to no printer is charged to nobody. It is waiting for whichever
// machine fits, so attributing it to one would invent load that does not exist;
// it will bind itself when a printer frees up.
func queueMinutesByPrinter(items []bambubuddy.QueueItem) map[int]queueLoad {
	out := map[int]queueLoad{}
	for _, it := range items {
		if it.PrinterID == nil {
			continue
		}
		switch it.Status {
		case bambubuddy.QueuePending:
			load := out[*it.PrinterID]
			load.Items++
			load.Pending++
			load.Minutes += queueItemMinutes(it)
			out[*it.PrinterID] = load
		case bambubuddy.QueuePrinting:
			load := out[*it.PrinterID]
			load.Items++
			out[*it.PrinterID] = load
		}
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
