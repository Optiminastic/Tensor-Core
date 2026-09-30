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

	"github.com/Optiminastic/tensor-core/internal/bedpack"
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
	// HoldsColours is whether this printer's trays could have printed the bed,
	// regardless of whether it was allowed to. Recorded on REFUSED printers on
	// purpose: "A2 was the only printer holding this bed's colours" and "A5
	// holds them too but its last print failed" are different situations, and
	// only the second one is worth walking over to the machine about. The bind
	// is pure arithmetic over trays already in hand, so knowing this for every
	// printer costs nothing.
	HoldsColours bool
	// Oversized marks a printer whose bed is LARGER than the class this plate
	// was laid out for. Eligible, and deliberately ranked below every exact
	// match: the shop's rule is that the big beds earn their keep, so a
	// three-plank plate takes an H2C only when nothing of its own class can.
	Oversized bool
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
	ctx context.Context, slots []meshio.Slot, bed bedColours, family string,
) (queuePlan, []machineOption, error) {
	options, err := s.rankMachinesForPlate(ctx, slots, bed, family)
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
		Reason:    chosenReason(won, options) + oversizedNote(won, family),
	}, options, nil
}

// rankMachinesForPlate weighs every printer in the fleet against this plate.
func (s *Server) rankMachinesForPlate(
	ctx context.Context, slots []meshio.Slot, bed bedColours, family string,
) ([]machineOption, error) {
	log := obs.FromContext(ctx)

	// Read the fleet from the printers themselves before ranking, rather than
	// trusting the minute-old mirror. Somebody pressing Queue has usually just
	// changed something - swapped a spool, cleared a failed plate - and the
	// whole complaint this answers is "a free machine with the right colours
	// was skipped", which a stale row produces exactly.
	//
	// Refresh, never the full sync: syncFleet's prune path ends in
	// DeleteFleetMachinesNotIn, and a button press is not a reason to risk
	// deleting a printer on one partial read.
	//
	// Best-effort. A refresh that fails leaves the mirror as it was, which is
	// what ranking used to run on anyway - degraded, not wrong. Refusing to
	// rank because a status read timed out would strand the bed.
	if _, err := s.RefreshFleetFromBambuBuddy(ctx); err != nil {
		log.Warn("could not refresh the fleet before ranking; using the last sync", "error", err)
	}

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
			Row: r, Slots: slots, Identities: identities, Bed: bed,
			Load: load, InFlight: inFlight, Sliceable: sliceable,
			PrinterIDs: printerIDs, Now: now,
			WaitingCap: maxBedsWaitingPerMachine,
			BedFamily:  family,
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
	// Bed is what the ORDER asked for, which rescues a plate whose hex was
	// baked in before its colour was mapped.
	Bed       bedColours
	Load      map[int]queueLoad
	InFlight  map[uuid.UUID]int
	Sliceable func(model string) string
	// PrinterIDs maps a machine's serial number to BambuBuddy's id for it. The
	// load map is keyed by that id and machines are keyed by serial, so
	// something has to join them; this is the cached fleet index, read once.
	PrinterIDs map[string]int
	Now        time.Time
	// WaitingCap is how many beds may be waiting on one printer before it
	// stops being offered another. See maxBedsWaitingPerMachine.
	WaitingCap int
	// BedFamily is the printer class this plate was laid out for, and the only
	// one that can print it. Empty means a bed planned before classes existed
	// or built by the optimiser, which is offered to any machine as before.
	BedFamily string
}

// maxBedsWaitingPerMachine is how many beds may sit waiting on one printer.
//
// One, because the shop works that way: a bed goes on a machine, and the next
// is only sent once that one is actually laying plastic. Stacking a queue per
// printer commits a bed to a machine hours before it runs, which is exactly
// when the reason for choosing that machine stops being true - the spools get
// swapped, a job goes on hold, a faster printer frees up.
//
// Counted from BambuBuddy's PENDING items plus the beds Tensor has sent that
// its queue cannot see yet. A plate that is PRINTING is not counted: that is
// the printer working, not a backlog, and the whole point is that the next bed
// goes as soon as the last one starts.
const maxBedsWaitingPerMachine = 1

// weighMachine decides whether one printer can take the bed, and how soon.
func (s *Server) weighMachine(in weighInputs) machineOption {
	r := in.Row
	machine := fleetMachineOf(r)
	opt := machineOption{Machine: machine}

	// The colour gate, run before the status checks so that HoldsColours is
	// known for every printer including the ones about to be refused. Pure
	// arithmetic over trays already fetched - no I/O - so it is free to ask.
	// The REFUSAL order below is untouched: a printer that is off says it is
	// off, not that its spools are wrong.
	//
	// Ranked on the mirrored machines.filaments column, which
	// rankMachinesForPlate has just refreshed from the printers themselves, so
	// it is as live as one round of status reads can make it. Two live
	// re-checks still stand downstream - assignmentsFromChoice on send, and the
	// slice worker's trayCheck before anything is queued - because a spool can
	// always be swapped between the ranking and the slice.
	slotTrays, bindErr := bindPlateToTrays(in.Slots, decodeTrays(machine), in.Identities, in.Bed)
	opt.HoldsColours = bindErr == nil

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

	if bindErr != nil {
		opt.Refusal = bindErr.Error()
		return opt
	}

	// The plate was laid out on one class's bed and the slicer is told not to
	// rearrange it, so this is a fit, not a preference: four planks packed on
	// an A2L's 330x320 come out 270x270 and a P2S is 256x256. Checked after
	// the health refusals so a printer that is off still says it is off.
	//
	// One-directional. A SMALLER bed is refused outright; a larger one takes
	// the plate unchanged and is kept as a fallback, so the shop's biggest
	// machines get work when nothing of the bed's own class can take it.
	if want := strings.TrimSpace(in.BedFamily); want != "" {
		switch bedpack.FitForFamily(want, strings.TrimSpace(deref(r.ProfileFamily))) {
		case bedpack.FitExact:
		case bedpack.FitOversized:
			// Bigger than the plate needs. Allowed, because a plate sits
			// inside a larger bed unchanged, and flagged so the picker takes
			// it only when the plate's own class cannot.
			opt.Oversized = true
		default:
			opt.Refusal = fmt.Sprintf("this bed is laid out for a %s", want)
			return opt
		}
	}

	opt.FreeAt, opt.PendingItems = freeAtFor(in, machine)

	// One bed at a time. Last, because it is the only refusal here that is not
	// a fault: the printer is fine, it simply already has work waiting, and
	// saying so beside the ones that are off or faulted would read as a
	// problem when it is the system doing what it was asked.
	if cap := in.WaitingCap; cap > 0 && opt.PendingItems >= cap {
		opt.Refusal = "this printer already has a bed waiting to start"
		return opt
	}

	opt.Eligible = true
	opt.SlotTrays = slotTrays
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
	// Two passes rather than one ordering: the plate's own class first, and a
	// larger bed only if that pass found nothing.
	//
	// Not a key inside beats, though it would sort the same. beats is also what
	// orders the list an operator reads in the queue dialog, and this file's own
	// rule is that a constraint must not leak into the score - a leading bool
	// there makes beats mean two things and invites the next person to weigh
	// one against the other. As two passes, every guarantee beats already makes
	// - earliest free, then emptier queue, then stable serial - holds inside
	// each tier untouched.
	if best := chooseWithin(options, false); best >= 0 {
		return best
	}
	return chooseWithin(options, true)
}

// chooseWithin is chooseMachine's loop over one tier of printers.
func chooseWithin(options []machineOption, oversized bool) int {
	best := -1
	for i, o := range options {
		if !o.Eligible || o.Oversized != oversized {
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
// Three things, because between them they answer every form of "why that one?"
// an operator actually asks: when this printer comes free, how many others
// could have taken the bed, and - the one that was missing - which printers
// hold the right spools but were not allowed to have it.
//
// That last clause is the whole point. Two gold beds went to A2 in nineteen
// minutes and it looked arbitrary; the truth was that A2 was the only working
// printer on the floor with gold loaded, because A5 had gold too and was
// locked out after failing a print. Nothing on screen said so, so the fleet
// looked like a scheduling bug.
func chosenReason(won machineOption, all []machineOption) string {
	var eligible int
	blocked := make([]string, 0, 4)
	for _, o := range all {
		switch {
		case o.Eligible:
			eligible++
		case o.HoldsColours && o.Refusal != "":
			blocked = append(blocked, fmt.Sprintf("%s (%s)", o.Machine.Name, o.Refusal))
		}
	}
	sort.Strings(blocked)

	when := "free now"
	if wait := time.Until(won.FreeAt).Round(time.Minute); wait > 0 {
		when = fmt.Sprintf("free in about %s", humanMinutes(wait))
	}

	var head string
	if eligible <= 1 {
		head = fmt.Sprintf("%s and the only printer holding this bed's colours", when)
	} else {
		head = fmt.Sprintf("%s, and holds this bed's colours - %d other %s could also take it",
			when, eligible-1, plural(eligible-1, "printer"))
	}
	if len(blocked) == 0 {
		return head
	}
	// Named, not counted. "1 other printer holds these colours" sends somebody
	// to the Machine Management page to work out which; naming it and saying
	// why sends them to the machine.
	return fmt.Sprintf("%s. %s also %s this bed's colours but %s unavailable: %s",
		head,
		plural2(len(blocked), "One other printer", "Other printers"),
		plural2(len(blocked), "holds", "hold"),
		plural2(len(blocked), "it is", "they are"),
		strings.Join(blocked, "; "))
}

// plural2 picks between two whole phrasings. English does not inflect these
// regularly enough for the suffix-adding plural() to reach them.
func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
		// The same tier the picker uses, so the list reads in the order the
		// bed will actually be offered.
		if a.Oversized != b.Oversized {
			return !a.Oversized
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
		// Carried, because decodeTrays reads them: a two-nozzle machine's
		// external spool is invisible without these, and leaving them behind
		// here would refuse every H2C a bed while the column said white.
		FixedNozzleColour: r.FixedNozzleColour, FixedNozzleIndex: r.FixedNozzleIndex,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// oversizedNote explains a bed that went to a class larger than it was laid
// out for. Empty for an ordinary choice.
//
// Worth a sentence because the fleet otherwise looks like it is ignoring its
// own rule: a three-plank bed on an H2C is exactly what "the big beds earn
// their keep" says should not happen, and the reason it happened - nothing of
// its own class could take it - is not visible anywhere else.
//
// The machine's name is already printed beside this line, so the sentence names
// the class the PLATE was built for rather than the one it landed on.
func oversizedNote(won machineOption, bedFamily string) string {
	if !won.Oversized || strings.TrimSpace(bedFamily) == "" {
		return ""
	}
	return fmt.Sprintf(". This bed was laid out for a %s; no %s printer could take it, so it goes on a larger bed",
		bedFamily, bedFamily)
}
