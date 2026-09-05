package production

// Colour batching: the simple rule that replaced the optimiser.
//
// The planner in planner.go is a general optimiser - four packing strategies, a
// weighted score over utilisation, throughput, priority, due date and waiting
// time, then a 2-opt local search. It is the right tool for a mixed catalogue of
// arbitrary shapes.
//
// It is the wrong tool for what this shop actually prints. A Dual Name Plank is
// 200 x 50 mm on a 330 x 320 bed, so four of them cover 37.9% of it - and the
// planner refuses to create any batch under TargetBedUtilisationPercent (80).
// The jobs sat queued. Worse, colour was only a 0.05 scoring penalty rather than
// a rule, so the beds it did make mixed colours, which on a two-filament machine
// means somebody changes the spool mid-plate.
//
// So the rule here is stated rather than searched for: one colour per bed, at
// most MaxColourBatchUnits products on it, oldest order first. It is not an
// approximation of the optimiser - it is a different, deliberately simpler
// policy, and the optimiser is kept intact beside it (see BatchStrategy) for the
// day the catalogue is mixed enough to need it again.

import (
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// MaxColourBatchUnits is how many products may share one bed.
//
// Four, per the shop's own instruction. It is a policy number, not a geometric
// one: four planks physically fit with room to spare, and the cap exists so a
// bed is a manageable, quickly-turned-around unit of work rather than the most
// the packer could cram on.
const MaxColourBatchUnits = 4

// StrategyColour is the packing_strategy recorded on batches this produces, so a
// batch on the floor says which policy built it. Batches from the optimiser keep
// their own strategy names (fcfs, area_desc, ...).
const StrategyColour = "colour"

// Reasons a job cannot be placed by the colour rule.
const (
	// ReasonNoColour: colour is the grouping key, so a job without one has no
	// bed to join. Grouping every colourless job together would put unrelated
	// products on a plate purely because neither recorded a colour.
	ReasonNoColour = "No filament colour recorded, so it cannot be grouped by colour."
	// ReasonTooLargeForBed: the unit does not fit even alone.
	ReasonTooLargeForBed = "The model is too large for the printer bed."
)

// GroupByColour lays jobs onto beds by colour, oldest first.
//
// jobs must already be in the order they should be served - the caller's query
// orders by the order's placed_at, so the oldest customer's plank is the first
// one onto a bed. Order is preserved exactly: this walks the list once and never
// reorders, which is what makes "oldest order first" a property of the output
// rather than a hope.
//
// A job's quantity is spent unit by unit, so a quantity of six becomes a full
// bed of four and two on the next. The fragments come back as PlanJobs with
// reduced Quantity, which is the same shape the optimiser's splitter produces
// and which the orchestrator already knows how to commit.
//
// maxPerBed caps how many products share one bed - the same number for every
// machine, because a plate is built before anything knows which printer will
// take it. Sizing a bed to a machine class was tried and reverted: it made the
// contents of a plate depend on live fleet state, and a bed built for one class
// is a bed no other class can print.
//
// nest packs a bed, injected for the same reason PlanWithNester takes a Nester:
// so a test can prove the fit rule without depending on bedpack's heuristics.
func GroupByColour(jobs []PlanJob, maxPerBed int, nest BedNester) ([]PlannedBatch, []Unbatchable) {
	if maxPerBed < 1 {
		maxPerBed = MaxColourBatchUnits
	}
	if nest == nil {
		nest = DefaultBedNester
	}

	// open beds, keyed by what physically cannot be mixed. Insertion order is
	// tracked separately so the finished batches come out oldest-first too - a
	// map alone would shuffle them and lose the property the whole rule is for.
	type bed struct {
		key   string
		jobs  []PlanJob
		units []bedpack.UnitFootprint
	}
	open := map[string]*bed{}
	var openOrder []string

	var batches []PlannedBatch
	var unbatchable []Unbatchable

	// close finishes a bed and appends it to the output.
	closeBed := func(b *bed) {
		if len(b.jobs) == 0 {
			return
		}
		batches = append(batches, finaliseOn(bedpack.DefaultBed, b.jobs, b.units, StrategyColour, nest))
	}

	for _, j := range jobs {
		key, ok := colourBedKey(j)
		if !ok {
			unbatchable = append(unbatchable, Unbatchable{
				JobID: j.ID, JobNumber: j.JobNumber, Reason: ReasonNoColour,
			})
			continue
		}

		unit := bedpack.UnitFootprint{
			RefID: j.ID, XMM: j.Footprint.XMM, YMM: j.Footprint.YMM, ZMM: j.Footprint.ZMM,
		}
		// A unit that does not fit an empty bed will never fit any bed, so it is
		// rejected once here rather than re-tried against every partial bed.
		if _, rejected := nest(bedpack.DefaultBed, []bedpack.UnitFootprint{unit}); len(rejected) > 0 {
			unbatchable = append(unbatchable, Unbatchable{
				JobID: j.ID, JobNumber: j.JobNumber, Reason: ReasonTooLargeForBed,
			})
			continue
		}

		remaining := quantityOf(j)
		for remaining > 0 {
			b := open[key]
			if b == nil {
				b = &bed{key: key}
				open[key] = b
				openOrder = append(openOrder, key)
			}

			space := maxPerBed - len(b.units)
			take := min(space, remaining)

			// The cap is a policy limit, not a guarantee of fit: a bed of four
			// larger products could still overflow. Adding only what actually
			// packs keeps the plate buildable, which matters because
			// buildMergedPlate re-packs independently and refuses a bed that
			// does not fit.
			for take > 0 {
				trial := append(append([]bedpack.UnitFootprint{}, b.units...), repeat(unit, take)...)
				if _, rejected := nest(bedpack.DefaultBed, trial); len(rejected) == 0 {
					break
				}
				take--
			}
			if take == 0 {
				// Nothing more fits on this bed. Close it and let the next
				// iteration open a fresh one for the same colour.
				closeBed(b)
				delete(open, key)
				openOrder = removeFirst(openOrder, key)
				continue
			}

			frag := j
			frag.Quantity = take
			b.jobs = append(b.jobs, frag)
			b.units = append(b.units, repeat(unit, take)...)
			remaining -= take

			if len(b.units) >= maxPerBed {
				closeBed(b)
				delete(open, key)
				openOrder = removeFirst(openOrder, key)
			}
		}
	}

	// Beds that never reached the cap are still created. That is the whole point
	// of turning the optimiser off: a bed of three planks prints today rather
	// than waiting for a fourth order in the same colour that may not come.
	for _, key := range openOrder {
		if b, ok := open[key]; ok {
			closeBed(b)
			delete(open, key)
		}
	}
	return batches, unbatchable
}

// colourBedKey is what a bed may not mix.
//
// Colour is the rule the shop asked for. Material and machine family are added
// not as optimisation but because they are physical: one plate is sliced once
// with one filament and printed on one machine, so a bed mixing PLA with PETG,
// or a job for an A2L with one for a P2S, describes a print that cannot happen.
// A batch whose jobs disagree on family is left permanently unassigned by
// batchMachineFamily and no printer ever picks it up - so partitioning on it
// here costs an extra bed and avoids a batch that silently never prints.
//
// An empty family or material does not force its own bucket: unset means
// unknown, not different, matching batchMachineFamily's own reading.
func colourBedKey(j PlanJob) (string, bool) {
	colours := make([]string, 0, len(j.Colours))
	for _, c := range j.Colours {
		if c = strings.TrimSpace(c); c != "" {
			colours = append(colours, strings.ToUpper(c))
		}
	}
	if len(colours) == 0 {
		return "", false
	}
	// Sorted so a job recording ["WHITE","BLUE"] joins one recording
	// ["BLUE","WHITE"] - the set is what matters, not the order it was written.
	sort.Strings(colours)
	return strings.Join(colours, "+") + "|" + j.Material + "|" + j.MachineFamily, true
}

// repeat is n copies of one footprint - a job's quantity expanded into units.
func repeat(u bedpack.UnitFootprint, n int) []bedpack.UnitFootprint {
	out := make([]bedpack.UnitFootprint, n)
	for i := range out {
		out[i] = u
	}
	return out
}

// removeFirst drops the first occurrence of key, keeping the rest in order.
func removeFirst(keys []string, key string) []string {
	for i, k := range keys {
		if k == key {
			return append(keys[:i:i], keys[i+1:]...)
		}
	}
	return keys
}
