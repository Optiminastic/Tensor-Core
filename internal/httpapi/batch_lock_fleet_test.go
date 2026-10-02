package httpapi

// One free printer is promised to one bed, even within a single pass.
//
// The fleet is read once per dispatch pass and the lock gate then asks "is a
// printer free?" for every Draft in it. Without a pass-local claim every Draft
// of the same class and colour got the same answer about the same idle machine,
// all of them locked, and only one could ever be sent - leaving the rest
// committed, unable to print and unable to grow, which is the exact failure the
// gate was added to prevent.
//
// Tested through weighMachine rather than through readyToLock, which needs a
// database, a plate in object storage and a fleet: the claim is an arithmetic
// fact about the snapshot, and this is the layer where that arithmetic is
// decided.

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClaimingAPrinterTakesItForTheRestOfThePass(t *testing.T) {
	s := &Server{}
	row := healthyRow("A2L-3")
	snap := &fleetSnapshot{at: time.Now()}

	weigh := func() machineOption {
		return s.weighMachine(weighInputs{
			Row: row, Slots: redBed, Now: snap.at, WaitingCap: maxBedsPerMachine,
			Sliceable: func(string) string { return "" },
			InFlight:  snap.inFlight,
		})
	}

	if opt := weigh(); !opt.Eligible {
		t.Fatalf("an idle printer was refused before anything claimed it: %q", opt.Refusal)
	}

	snap.claim(row.ID)

	opt := weigh()
	if opt.Eligible {
		t.Error("a printer already claimed by an earlier bed in this pass was offered " +
			"to a second one; one batch per machine has to hold within a pass")
	}
	if opt.Refusal == "" {
		t.Error("the refusal gave no reason")
	}
}

// claim on a nil snapshot is a no-op, not a panic. fleetSnapshotNow returns nil
// when the fleet cannot be read, and every caller of claim sits on a path that
// has already handled that - but a nil-receiver crash in the dispatch worker
// would take down the pass for every bed behind it.
func TestClaimingOnANilSnapshotIsHarmless(t *testing.T) {
	var snap *fleetSnapshot
	snap.claim(uuid.New())
}

// The claim accumulates, so a printer is not freed by a second bed looking at
// it. Pinned because the map is shared and mutated in place.
func TestClaimsAccumulatePerPrinter(t *testing.T) {
	snap := &fleetSnapshot{}
	id := uuid.New()

	snap.claim(id)
	snap.claim(id)

	if got := snap.inFlight[id]; got != 2 {
		t.Errorf("two claims recorded %d beds in flight, want 2", got)
	}
	if got := snap.inFlight[uuid.New()]; got != 0 {
		t.Errorf("an unclaimed printer recorded %d beds in flight, want 0", got)
	}
}
