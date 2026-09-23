package httpapi

// Choosing the printer for a bed, so nobody has to read thirteen AMS bays off
// a screen.
//
// Constrained greedy earliest-completion: hard constraints decide who is
// ELIGIBLE, and a single projected finish time decides who WINS. The two halves
// are kept apart on purpose. A constraint that leaks into the score becomes a
// penalty something else can outweigh - which is how a bed ends up on a printer
// that does not hold its colours because that printer was free sooner.
//
// Nothing cleverer is warranted. An optimal assignment over the whole locked
// queue only pays when placements interact, and beds are placed one press at a
// time against inputs that are noisy - a print-time estimate good to perhaps
// twenty percent. Optimality on those numbers is false precision, and it costs
// the thing that matters more: an operator has to be able to read why this
// printer won. rankMachines is pure so the smarter version, when the background
// worker needs it, has one function to replace.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// machineOption is one printer weighed against one bed.
type machineOption struct {
	Machine gen.Machine
	// SlotTrays binds each plate slot to a tray, as ams_mapping integers.
	// Populated only when the machine is eligible.
	SlotTrays []int
	// FreeAt is when this printer finishes everything it already owes.
	FreeAt time.Time
	// PendingItems is how many plates are waiting on it, for the tie-break.
	PendingItems int
	Eligible     bool
	// Refusal says why not, in words an operator can act on.
	Refusal string
}

// queuePlan is the chosen printer and how to print the bed on it.
type queuePlan struct {
	Machine   gen.Machine
	SlotTrays []int
	// Reason is the one line shown beside the choice.
	Reason string
}

// planQueueForBatch picks the printer a bed should go to.
//
// Gin-free, and takes the plate's slots rather than reading them, so the
// eventual background worker is this plus sendBatchToMachine with nothing
// re-extracted from a handler.
//
// Returns every option, not just the winner, because the dialog shows the whole
// fleet with a reason against each ineligible one - and because "no machine can
// take this" has to be able to say why, thirteen times over, rather than
// shrugging.
func (s *Server) planQueueForBatch(
	ctx context.Context, slots []meshio.Slot,
) (queuePlan, []machineOption, error) {
	options, err := s.rankMachinesForPlate(ctx, slots)
	if err != nil {
		return queuePlan{}, nil, err
	}
	best := chooseMachine(options)
	if best < 0 {
		return queuePlan{}, options, nil
	}
	won := options[best]
	return queuePlan{
		Machine:   won.Machine,
		SlotTrays: won.SlotTrays,
		Reason:    chosenReason(won, options),
	}, options, nil
}

// rankMachinesForPlate weighs every printer in the fleet against this plate.
func (s *Server) rankMachinesForPlate(
	ctx context.Context, slots []meshio.Slot,
) ([]machineOption, error) {
	log := obs.FromContext(ctx)

	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read the fleet")
	}
	identities, err := s.colourIdentities(ctx)
	if err != nil {
		// The map only ever WIDENS what counts as a colour, so losing it costs
		// beds whose plate hex disagrees with the spool - not correctness.
		log.Warn("colour map unavailable; only exact hex matches will bind", "error", err)
	}

	// Both read once for the whole ranking rather than once per candidate:
	// this is one decision and should cost one round trip, not fourteen.
	load := s.fleetQueueLoad(ctx)
	inFlight := s.bedsInFlightPerMachine(ctx)
	sliceable := s.sliceablePipelines(ctx)
	printerIDs, err := s.bambuCache.printerIndex(ctx, s.bambu.ListPrinters)
	if err != nil {
		// Without the join, queue depth is unknown and every printer is ranked
		// on its current print alone. Degraded, not wrong.
		log.Warn("could not map printers to BambuBuddy ids; ranking without queue depth", "error", err)
	}

	now := time.Now()
	out := make([]machineOption, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.weighMachine(weighInputs{
			Row: r, Slots: slots, Identities: identities,
			Load: load, InFlight: inFlight, Sliceable: sliceable,
			PrinterIDs: printerIDs, Now: now,
		}))
	}
	return out, nil
}

// weighInputs is everything one printer is judged on. Grouped into a struct
// because they are read together and passing seven arguments invites a caller
// to transpose two of them.
type weighInputs struct {
	Row        gen.ListFleetMachinesWithFamilyRow
	Slots      []meshio.Slot
	Identities []colourIdentity
	Load       map[int]queueLoad
	InFlight   map[uuid.UUID]int
	Sliceable  func(model string) string
	// PrinterIDs maps a machine's serial number to BambuBuddy's id for it. The
	// load map is keyed by that id and machines are keyed by serial, so
	// something has to join them; this is the cached fleet index, read once.
	PrinterIDs map[string]int
	Now        time.Time
}

// weighMachine decides whether one printer can take the bed, and how soon.
func (s *Server) weighMachine(in weighInputs) machineOption {
	r := in.Row
	machine := fleetMachineOf(r)
	opt := machineOption{Machine: machine}

	switch {
	case r.Status == production.FleetMachineOff:
		opt.Refusal = "this printer is off"
		return opt
	case r.Status == production.FleetMachineError:
		opt.Refusal = "this printer is reporting a fault"
		return opt
	case r.MachineProfileID == nil:
		opt.Refusal = "this printer has no slicing profile yet; run a fleet sync"
		return opt
	case r.StatusReason != nil:
		// A printer whose last print FAILED is recorded as idle, deliberately:
		// it is connected, at 0%, and BambuBuddy's own UI calls it ready. That
		// is the right answer for batching - withholding it there took five of
		// thirteen machines out of planning over something that had already
		// happened.
		//
		// It is the wrong answer HERE. Idle is the best score this picker can
		// give, so a machine that fails every plate was ranked the most
		// available on the floor and handed bed after bed. One gold plate went
		// to the same printer five times while its Z axis could not home.
		//
		// Self-clearing: the sync writes this back to NULL as soon as the
		// printer leaves FAILED, so clearing the plate is all it takes.
		opt.Refusal = strings.TrimSuffix(strings.ToLower(*r.StatusReason), ".")
		return opt
	case r.ProfileStatus != nil && *r.ProfileStatus == production.MachineOffline:
		opt.Refusal = "this printer is marked offline"
		return opt
	case r.ProfileStatus != nil && *r.ProfileStatus == production.MachineMaintenance:
		opt.Refusal = "this printer is in maintenance"
		return opt
	}
	// Checked here rather than at send time, where it was a 409 raised AFTER
	// the bed had already been locked - an irreversible step taken for a
	// printer that was never going to work.
	if why := in.Sliceable(deref(machine.Model)); why != "" {
		opt.Refusal = why
		return opt
	}

	// The colour gate. Ranked on the mirrored machines.filaments column rather
	// than a live AMS read: ranking fourteen printers would be fourteen calls
	// through the tunnel for one dialog, and the mirror is at most one sync
	// interval stale. Two live re-checks already stand downstream -
	// assignmentsFromChoice when the operator presses Send, and the slice
	// worker's trayCheck before anything is queued - so staleness here costs a
	// clear refusal, never a wrong-coloured print. Do not "improve" this into a
	// live read.
	trays := decodeTrays(machine)
	slotTrays, err := bindPlateToTrays(in.Slots, trays, in.Identities)
	if err != nil {
		opt.Refusal = err.Error()
		return opt
	}

	opt.Eligible = true
	opt.SlotTrays = slotTrays
	opt.FreeAt, opt.PendingItems = freeAtFor(in, machine)
	return opt
}

// freeAtFor projects when this printer finishes what it already owes.
//
// Three sources, deliberately non-overlapping so nothing is counted twice:
// the plate on the bed right now (from the printer's own remaining time),
// the plates queued behind it in BambuBuddy, and the beds Tensor has sent that
// have not reached that queue yet.
func freeAtFor(in weighInputs, machine gen.Machine) (time.Time, int) {
	state := production.FleetMachineState{
		MachineID:           machine.ID,
		RemainingMinutes:    int32PtrToIntPtr(machine.RemainingMinutes),
		RemainingObservedAt: db.TimePtr(machine.RemainingObservedAt),
	}

	var queued []production.QueuedBatch
	var items int
	if printerID, ok := in.PrinterIDs[machine.MachineID]; ok {
		if l, known := in.Load[printerID]; known {
			items = l.Items
			queued = append(queued, production.QueuedBatch{TotalPrintTimeMinutes: l.Minutes})
		}
	}
	// A bed sliced but not yet queued is real work heading here, charged the
	// same nominal cost as any plate of unknown length. Without it, five beds
	// sent in the minutes before the first one is sliced all pick this printer.
	for i := 0; i < in.InFlight[machine.ID]; i++ {
		items++
		queued = append(queued, production.QueuedBatch{TotalPrintTimeMinutes: unestimatedBatchMinutes})
	}
	return production.MachineFreeAt(in.Now, state, queued), items
}

// chooseMachine returns the index of the printer that wins, or -1.
//
// Pure: everything it needs is already resolved onto the options. Earliest
// projected finish wins; ties go to the emptier queue, then to a stable name,
// so the same fleet in the same state always produces the same answer and an
// operator can be told why.
func chooseMachine(options []machineOption) int {
	best := -1
	for i, o := range options {
		if !o.Eligible {
			continue
		}
		if best < 0 || beats(o, options[best]) {
			best = i
		}
	}
	return best
}

func beats(a, b machineOption) bool {
	if !a.FreeAt.Equal(b.FreeAt) {
		return a.FreeAt.Before(b.FreeAt)
	}
	if a.PendingItems != b.PendingItems {
		return a.PendingItems < b.PendingItems
	}
	return a.Machine.MachineID < b.Machine.MachineID
}

// chosenReason is the line shown beside the choice.
//
// Says when the printer is free and how many others could have taken the bed,
// because "why this one" is the first thing anybody asks and the honest answer
// is usually "the others were busy" or "any of four would have done".
func chosenReason(won machineOption, all []machineOption) string {
	var eligible int
	for _, o := range all {
		if o.Eligible {
			eligible++
		}
	}

	when := "free now"
	if wait := time.Until(won.FreeAt).Round(time.Minute); wait > 0 {
		when = fmt.Sprintf("free in about %s", humanMinutes(wait))
	}
	if eligible <= 1 {
		return fmt.Sprintf("%s and the only printer holding this bed's colours", when)
	}
	return fmt.Sprintf("%s, and holds this bed's colours - %d other %s could also take it",
		when, eligible-1, plural(eligible-1, "printer"))
}

func humanMinutes(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d %s", minutes, plural(minutes, "minute"))
	}
	hours := minutes / 60
	rest := minutes % 60
	if rest == 0 {
		return fmt.Sprintf("%d %s", hours, plural(hours, "hour"))
	}
	return fmt.Sprintf("%dh %dm", hours, rest)
}

// sliceablePipelines answers, per model, whether BambuBuddy can slice for it.
//
// Read once and closed over rather than called per machine. A failed read
// disables the check rather than failing every machine: one blip would
// otherwise mark the whole fleet ineligible and empty the dialog, which reads
// to an operator as "no printer can do this" when the truth is "Tensor could
// not ask".
func (s *Server) sliceablePipelines(ctx context.Context) func(string) string {
	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("slicer pipelines unavailable; not filtering on them", "error", err)
		return func(string) string { return "" }
	}
	return func(model string) string {
		eligible := pipelinesFor(pipelines, model)
		if len(eligible) == 0 {
			return pipelineTargetNote(model)
		}
		if p := eligible[0]; p.PrinterPreset == nil || p.ProcessPreset == nil {
			return fmt.Sprintf("BambuBuddy's %q pipeline has no printer or process preset",
				strings.TrimSpace(p.Name))
		}
		return ""
	}
}

// bedsInFlightPerMachine counts beds sent to each printer that BambuBuddy's
// queue cannot see yet. A failed read returns an empty map - the ranking is
// then merely less fair, not wrong.
func (s *Server) bedsInFlightPerMachine(ctx context.Context) map[uuid.UUID]int {
	rows, err := s.store.Q.CountBedsInFlightPerFleetMachine(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not count beds in flight", "error", err)
		return map[uuid.UUID]int{}
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		if r.FleetMachineID != nil {
			out[*r.FleetMachineID] = int(r.Beds)
		}
	}
	return out
}

// sortOptions puts the eligible printers first, soonest-free first, so a list
// rendered straight from this reads in the order a person would choose.
func sortOptions(options []machineOption) {
	sort.SliceStable(options, func(i, j int) bool {
		a, b := options[i], options[j]
		if a.Eligible != b.Eligible {
			return a.Eligible
		}
		if !a.Eligible {
			return a.Machine.MachineID < b.Machine.MachineID
		}
		return beats(a, b)
	})
}

// fleetMachineOf narrows the family-joined row back to the machine itself.
//
// ListFleetMachinesWithFamily returns machines.* plus two profile columns, but
// as its own generated type - so decodeTrays and everything else that speaks
// gen.Machine cannot read it. Converting once here is cheaper than widening
// four helpers to an interface, and keeps the profile columns visibly separate
// from the machine's own.
func fleetMachineOf(r gen.ListFleetMachinesWithFamilyRow) gen.Machine {
	return gen.Machine{
		ID: r.ID, MachineID: r.MachineID, Name: r.Name, ImageUrl: r.ImageUrl,
		Status: r.Status, Filaments: r.Filaments,
		CurrentBatchID: r.CurrentBatchID, CurrentLayer: r.CurrentLayer, TotalLayers: r.TotalLayers,
		BatchTotalTimeMinutes: r.BatchTotalTimeMinutes, PrintStartedAt: r.PrintStartedAt,
		TotalWasteGrams:  r.TotalWasteGrams,
		MachineProfileID: r.MachineProfileID, StatusReason: r.StatusReason,
		RemainingMinutes: r.RemainingMinutes, RemainingObservedAt: r.RemainingObservedAt,
		Model: r.Model, Location: r.Location, IpAddress: r.IpAddress,
		NozzleCount: r.NozzleCount,
		CreatedAt:   r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
