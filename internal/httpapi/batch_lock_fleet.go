package httpapi

// Whether a printer could take a bed RIGHT NOW - the second half of the lock
// gate.
//
// The shop's rule, stated by them: a bed locks when it holds at least the floor
// number of products AND a printer of its class is standing empty with the
// right colours loaded. Until all three hold it stays a Draft, which is the
// only state that can still absorb the next order in its colour.
//
// Why the fleet belongs in the lock decision at all, when the send path already
// checks it: locking is irreversible and sending is not. An approved bed has
// its filament reserved, its machine stamped, its plate merged and its jobs
// taken out of the replanning pool for good. Doing all that for a bed no
// printer can take produced exactly what the shop complained about - a pile of
// locked beds carrying a red "no printer can take this bed yet" note, none of
// which could absorb the next order, while Drafts that could have grown sat
// behind them.
//
// This is the SAME judgement the send path makes, deliberately: weighMachine
// decides eligibility for both, so a bed that locks here is a bed that sends.
// What differs is the cost - one fleet read is shared by every bed in the pass
// (fleetSnapshot) rather than re-read per bed, and the live per-printer status
// refresh is skipped, because a pass that walks twenty Drafts cannot afford
// twenty rounds of fourteen status reads. The mirror is a sync old at worst and
// the send path re-reads the trays before anything is sliced.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// fleetSnapshot is one read of the fleet, shared by every bed in a pass.
//
// Everything weighMachine needs that does not depend on the bed. Read once
// because ranking is cheap and READING is not: the queue is a BambuBuddy
// round-trip, the pipelines are another, and the printer index a third.
type fleetSnapshot struct {
	rows       []gen.ListFleetMachinesWithFamilyRow
	identities []colourIdentity
	load       map[int]queueLoad
	inFlight   map[uuid.UUID]int
	sliceable  func(model string) string
	printerIDs map[string]int
	at         time.Time
}

// fleetSnapshotNow reads the fleet as it stands.
//
// Returns nil when the fleet cannot be read at all, which is a DB failure -
// every other input degrades in place exactly as rankMachinesForPlate lets it,
// because a BambuBuddy blip must not decide whether a bed locks.
func (s *Server) fleetSnapshotNow(ctx context.Context) *fleetSnapshot {
	log := obs.FromContext(ctx)

	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		log.Warn("could not read the fleet for the lock gate", "error", err)
		return nil
	}
	identities, err := s.colourIdentities(ctx)
	if err != nil {
		// Only ever WIDENS what counts as a colour, so losing it costs beds
		// whose plate hex disagrees with the spool, not correctness.
		log.Warn("colour map unavailable for the lock gate; exact hex matches only", "error", err)
	}
	printerIDs, err := s.bambuCache.printerIndex(ctx, s.bambu.ListPrinters)
	if err != nil {
		log.Warn("could not map printers to BambuBuddy ids for the lock gate", "error", err)
	}
	return &fleetSnapshot{
		rows:       rows,
		identities: identities,
		load:       s.fleetQueueLoad(ctx),
		inFlight:   s.bedsInFlightPerMachine(ctx),
		sliceable:  s.sliceablePipelines(ctx),
		printerIDs: printerIDs,
		at:         time.Now(),
	}
}

// claim marks a printer as taken for the rest of this pass.
//
// Counted into inFlight, which freeAtFor already reads as "beds Tensor has sent
// that BambuBuddy's queue cannot see yet" - a pass-local claim is the same kind
// of fact, one step earlier, so the cap fires on it without a second mechanism.
//
// This is what stops one free printer being promised to several beds. The
// snapshot is read once per pass, so without it two Drafts of the same class
// and colour both saw the same idle machine, both locked, and only one could
// ever be sent - leaving the other committed, unable to print and unable to
// grow, which is the exact failure the lock gate was added to prevent.
func (snap *fleetSnapshot) claim(machineID uuid.UUID) {
	if snap == nil {
		return
	}
	if snap.inFlight == nil {
		snap.inFlight = map[uuid.UUID]int{}
	}
	snap.inFlight[machineID]++
}

// bedHasFreePrinter reports whether any printer could take this bed now: which
// one, and why not when none can.
//
// The machine is returned so the caller can claim it - see claim. The note is
// for the log, not the operator: a bed held open is not a fault, and writing
// "no printer can take this bed yet" onto a Draft would turn the ordinary state
// of waiting for a machine into a red flag on the Batches page. The send path
// still writes that note, for a bed that is locked and stuck.
func (s *Server) bedHasFreePrinter(
	ctx context.Context, b gen.Batch, snap *fleetSnapshot,
) (uuid.UUID, bool, string) {
	if snap == nil {
		return uuid.Nil, false, "the fleet could not be read"
	}
	slots := s.queueSlotsFor(ctx, b)
	if len(slots) == 0 {
		// No plate, or an unreadable one. Not lockable: approval merges the
		// plate and there is nothing here to bind to a tray.
		return uuid.Nil, false, "this bed's plate declares no filament yet"
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &b.ID)
	if err != nil {
		return uuid.Nil, false, "the bed's jobs could not be read"
	}

	bed := bedColoursOf(s.queueColoursFor(ctx, jobs))
	family := deref(b.MachineFamily)
	plate := plateSlotsOf(slots)

	options := make([]machineOption, 0, len(snap.rows))
	for _, r := range snap.rows {
		options = append(options, s.weighMachine(weighInputs{
			Row: r, Slots: plate, Identities: snap.identities, Bed: bed,
			Load: snap.load, InFlight: snap.inFlight, Sliceable: snap.sliceable,
			PrinterIDs: snap.printerIDs, Now: snap.at,
			WaitingCap: maxBedsPerMachine,
			BedFamily:  family,
		}))
	}
	// chooseMachine, not "any eligible": it is the same two-tier choice the
	// send path makes, so this cannot say yes to a printer the send would then
	// decline to pick.
	if best := chooseMachine(options); best >= 0 {
		return options[best].Machine.ID, true, ""
	}
	return uuid.Nil, false, noPrinterNote(options)
}
