package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

func queued(status string, printerID *int, seconds int) bambubuddy.QueueItem {
	return bambubuddy.QueueItem{Status: status, PrinterID: printerID, PrintTimeSeconds: seconds}
}

func intPtr(v int) *int { return &v }

func TestQueueMinutesByPrinterSumsPendingWork(t *testing.T) {
	got := queueMinutesByPrinter([]bambubuddy.QueueItem{
		queued(bambubuddy.QueuePending, intPtr(7), 3600),
		queued(bambubuddy.QueuePending, intPtr(7), 1800),
		queued(bambubuddy.QueuePending, intPtr(9), 600),
	})

	if len(got) != 2 {
		t.Fatalf("queueMinutesByPrinter returned %d printers, want 2: %v", len(got), got)
	}
	if got[7].Minutes != 90 || got[7].Items != 2 {
		t.Errorf("printer 7 = %+v, want 90 minutes over 2 items", got[7])
	}
	if got[9].Minutes != 10 || got[9].Items != 1 {
		t.Errorf("printer 9 = %+v, want 10 minutes over 1 item", got[9])
	}
}

// The double-count this function exists to avoid. A printing item's remaining
// time is carried by machines.remaining_minutes; adding its full print time
// here would charge the machine twice for one plate, and charge it the whole
// print when five minutes are left.
func TestQueueMinutesByPrinterIgnoresThePlateAlreadyPrinting(t *testing.T) {
	got := queueMinutesByPrinter([]bambubuddy.QueueItem{
		queued(bambubuddy.QueuePrinting, intPtr(3), 7200),
		queued(bambubuddy.QueuePending, intPtr(3), 1200),
	})

	if got[3].Minutes != 20 {
		t.Errorf("printer 3 = %d minutes, want 20 - the printing plate is counted by "+
			"remaining_minutes, not here", got[3].Minutes)
	}
	if got[3].Items != 1 {
		t.Errorf("printer 3 = %d items, want 1", got[3].Items)
	}
}

func TestQueueMinutesByPrinterIgnoresFinishedAndCancelledWork(t *testing.T) {
	for _, status := range []string{
		bambubuddy.QueueCompleted, bambubuddy.QueueCancelled, bambubuddy.QueueFailed,
	} {
		got := queueMinutesByPrinter([]bambubuddy.QueueItem{queued(status, intPtr(1), 3600)})
		if len(got) != 0 {
			t.Errorf("a %q item was charged to a printer: %v", status, got)
		}
	}
}

// An unbound item is waiting for whichever machine fits. Charging it to one
// would invent load that machine does not have.
func TestQueueMinutesByPrinterChargesAnUnboundItemToNobody(t *testing.T) {
	got := queueMinutesByPrinter([]bambubuddy.QueueItem{
		queued(bambubuddy.QueuePending, nil, 3600),
	})
	if len(got) != 0 {
		t.Errorf("an unbound queue item was charged to a printer: %v", got)
	}
}

func TestQueueItemMinutesChargesUnknownWorkRatherThanNothing(t *testing.T) {
	if got := queueItemMinutes(queued(bambubuddy.QueuePending, intPtr(1), 0)); got != unestimatedBatchMinutes {
		t.Errorf("a plate with no reported time = %d minutes, want %d - unknown work is "+
			"not free, or the machine holding it looks emptier than one holding measured work",
			got, unestimatedBatchMinutes)
	}
}

// A queue of short plates must not weigh nothing.
func TestQueueItemMinutesRoundsUpRatherThanFlooringToZero(t *testing.T) {
	if got := queueItemMinutes(queued(bambubuddy.QueuePending, intPtr(1), 30)); got != 1 {
		t.Errorf("a 30-second plate = %d minutes, want 1", got)
	}
}
