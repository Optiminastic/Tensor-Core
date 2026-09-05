package production

import (
	"fmt"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// plankJob is a Dual Name Plank as the planner sees one: the finished 200x50x40
// the renderer produces, in one colour.
func plankJob(id, colour string) PlanJob {
	return PlanJob{
		ID: id, JobNumber: "JOB-" + id, Material: "PLA", MachineFamily: "A2L",
		Colours: []string{colour}, Quantity: 1, CreatedAt: testNow,
		Footprint: bedpack.UnitFootprint{RefID: id, XMM: 200, YMM: 50, ZMM: 40},
	}
}

// colourOf is the one colour on a bed, or a complaint if there is more than one.
func colourOf(t *testing.T, b PlannedBatch) string {
	t.Helper()
	seen := map[string]bool{}
	for _, j := range b.Jobs {
		for _, c := range j.Colours {
			seen[c] = true
		}
	}
	if len(seen) != 1 {
		t.Fatalf("bed holds %d colours (%v), want exactly 1", len(seen), seen)
	}
	for c := range seen {
		return c
	}
	return ""
}

// The rule in one test: one colour per bed, never more than four on it.
func TestGroupByColourNeverMixesColoursOrExceedsTheCap(t *testing.T) {
	var jobs []PlanJob
	// Interleaved deliberately - if grouping were positional rather than by
	// colour, this ordering would produce mixed beds.
	for i := range 9 {
		colour := []string{"BLUE", "GOLD", "WHITE"}[i%3]
		jobs = append(jobs, plankJob(fmt.Sprintf("j%d", i), colour))
	}

	batches, unb := GroupByColour(jobs, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 0 {
		t.Fatalf("unbatchable = %v, want none", unb)
	}
	placed := 0
	for _, b := range batches {
		colourOf(t, b) // fails the test if the bed mixes colours
		if b.UnitsPerBed > MaxColourBatchUnits {
			t.Errorf("bed holds %d units, over the cap of %d", b.UnitsPerBed, MaxColourBatchUnits)
		}
		if b.PackingStrategy != StrategyColour {
			t.Errorf("strategy = %q, want %q", b.PackingStrategy, StrategyColour)
		}
		placed += b.UnitsPerBed
	}
	if placed != len(jobs) {
		t.Errorf("placed %d units, want all %d", placed, len(jobs))
	}
}

// Oldest order first, and served in order: the caller hands jobs over in the
// order they should print, so the first bed of a colour must hold that colour's
// first four jobs - not whichever four a map iteration happened to reach.
func TestGroupByColourServesInInputOrder(t *testing.T) {
	var jobs []PlanJob
	for i := range 6 {
		jobs = append(jobs, plankJob(fmt.Sprintf("blue%d", i), "BLUE"))
	}

	batches, _ := GroupByColour(jobs, MaxColourBatchUnits, DefaultBedNester)
	if len(batches) != 2 {
		t.Fatalf("got %d beds for 6 planks at 4 per bed, want 2", len(batches))
	}
	want := [][]string{
		{"blue0", "blue1", "blue2", "blue3"},
		{"blue4", "blue5"},
	}
	for i, b := range batches {
		var got []string
		for _, j := range b.Jobs {
			got = append(got, j.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(want[i]) {
			t.Errorf("bed %d holds %v, want %v", i, got, want[i])
		}
	}
}

// Four planks must actually fit, and the utilisation the batch records must be
// the truth about them - roughly 38% of a 330x320 bed, well under the optimiser's
// 80% target. That gap is exactly why the optimiser held these jobs, and the
// number is kept honest rather than inflated to make the bed look full.
func TestGroupByColourFourPlanksFitAtTheirRealUtilisation(t *testing.T) {
	var jobs []PlanJob
	for i := range MaxColourBatchUnits {
		jobs = append(jobs, plankJob(fmt.Sprintf("j%d", i), "BLUE"))
	}
	batches, unb := GroupByColour(jobs, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 0 || len(batches) != 1 {
		t.Fatalf("got %d beds and %d unbatchable, want 1 and 0", len(batches), len(unb))
	}
	b := batches[0]
	if b.UnitsPerBed != MaxColourBatchUnits {
		t.Errorf("units = %d, want %d", b.UnitsPerBed, MaxColourBatchUnits)
	}
	if len(b.Placements) != MaxColourBatchUnits {
		t.Errorf("packed %d units onto the bed, want all %d - the merged plate re-packs "+
			"independently and 409s on a bed that does not fit",
			len(b.Placements), MaxColourBatchUnits)
	}
	// 4 * 200 * 50 / (330 * 320) = 37.88%.
	if b.BedUtilisationPercent < 37 || b.BedUtilisationPercent > 39 {
		t.Errorf("utilisation = %.2f%%, want about 37.88%%", b.BedUtilisationPercent)
	}
	if b.BedUtilisationPercent >= TargetBedUtilisationPercent {
		t.Errorf("utilisation %.2f%% is at or over the old %.0f%% target; this test's "+
			"premise - that the optimiser's gate is what held these jobs - no longer holds",
			b.BedUtilisationPercent, TargetBedUtilisationPercent)
	}
}

// A job with no colour has no bed to join. Grouping the colourless together
// would put a photo frame on a plate with a temple name plate purely because
// neither recorded a colour.
func TestGroupByColourRejectsAColourlessJob(t *testing.T) {
	j := plankJob("nocolour", "")
	j.Colours = nil
	batches, unb := GroupByColour([]PlanJob{j, plankJob("blue", "BLUE")}, MaxColourBatchUnits, DefaultBedNester)

	if len(unb) != 1 || unb[0].JobID != "nocolour" {
		t.Fatalf("unbatchable = %v, want just the colourless job", unb)
	}
	if unb[0].Reason != ReasonNoColour {
		t.Errorf("reason = %q, want %q", unb[0].Reason, ReasonNoColour)
	}
	// The colourless job must not take the batchable one down with it.
	if len(batches) != 1 || len(batches[0].Jobs) != 1 {
		t.Errorf("got %d beds, want 1 holding the blue job", len(batches))
	}
}

// Quantity is spent unit by unit, so a job bigger than one bed splits across
// beds rather than overflowing one - the same fragment shape the optimiser's
// splitter produces, which is what the orchestrator knows how to commit.
func TestGroupByColourSplitsAJobAcrossBeds(t *testing.T) {
	j := plankJob("bulk", "BLUE")
	j.Quantity = 6

	batches, unb := GroupByColour([]PlanJob{j}, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 0 {
		t.Fatalf("unbatchable = %v, want none", unb)
	}
	if len(batches) != 2 {
		t.Fatalf("got %d beds for a quantity of 6 at 4 per bed, want 2", len(batches))
	}
	total := 0
	for i, b := range batches {
		if len(b.Jobs) != 1 {
			t.Fatalf("bed %d holds %d job fragments, want 1", i, len(b.Jobs))
		}
		total += b.Jobs[0].Quantity
		if b.UnitsPerBed > MaxColourBatchUnits {
			t.Errorf("bed %d holds %d units, over the cap", i, b.UnitsPerBed)
		}
	}
	if total != 6 {
		t.Errorf("fragments total %d units, want the original 6", total)
	}
}

// A unit too big for the bed is rejected once, not retried against every bed,
// and never blocks the jobs after it.
func TestGroupByColourRejectsAnOversizedUnit(t *testing.T) {
	huge := plankJob("huge", "BLUE")
	huge.Footprint = bedpack.UnitFootprint{RefID: "huge", XMM: 900, YMM: 900, ZMM: 40}

	batches, unb := GroupByColour([]PlanJob{huge, plankJob("ok", "BLUE")}, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 1 || unb[0].Reason != ReasonTooLargeForBed {
		t.Fatalf("unbatchable = %v, want the oversized job with %q", unb, ReasonTooLargeForBed)
	}
	if len(batches) != 1 || batches[0].Jobs[0].ID != "ok" {
		t.Errorf("the oversized job blocked the one after it; beds = %v", batches)
	}
}

// Material and machine family are physical, not preferences: one plate is
// sliced once with one filament and printed on one machine. A bed mixing them
// describes a print that cannot happen - and one whose jobs disagree on family
// is left permanently unassigned by batchMachineFamily, so no printer ever
// picks it up.
func TestGroupByColourStillSeparatesWhatCannotPhysicallyShareABed(t *testing.T) {
	pla := plankJob("pla", "BLUE")
	petg := plankJob("petg", "BLUE")
	petg.Material = "PETG"
	other := plankJob("other", "BLUE")
	other.MachineFamily = "P2S"

	batches, _ := GroupByColour([]PlanJob{pla, petg, other}, MaxColourBatchUnits, DefaultBedNester)
	if len(batches) != 3 {
		t.Fatalf("got %d beds, want 3 - same colour, but different material and family", len(batches))
	}
}

// The whole reason the optimiser was turned off: a partial bed prints rather
// than waiting for volume that may never arrive.
func TestGroupByColourCreatesAnUnderFullBed(t *testing.T) {
	batches, _ := GroupByColour([]PlanJob{plankJob("lonely", "YELLOW")}, MaxColourBatchUnits, DefaultBedNester)
	if len(batches) != 1 {
		t.Fatalf("got %d beds for a single plank, want 1", len(batches))
	}
	if batches[0].UnitsPerBed != 1 {
		t.Errorf("units = %d, want 1", batches[0].UnitsPerBed)
	}
}

// A job must appear on exactly one bed. Two beds both believing they hold the
// same plank is not a scheduling inefficiency - it prints the customer's order
// twice and consumes filament reserved for somebody else.
func TestGroupByColourNeverPlacesAJobTwice(t *testing.T) {
	var jobs []PlanJob
	for i := range 9 {
		colour := []string{"BLUE", "GOLD", "WHITE"}[i%3]
		jobs = append(jobs, plankJob(fmt.Sprintf("j%d", i), colour))
	}
	// One job big enough to split across beds - the case where the same id
	// legitimately appears more than once, and where double-counting would hide.
	bulk := plankJob("bulk", "BLUE")
	bulk.Quantity = 6
	jobs = append(jobs, bulk)

	batches, unb := GroupByColour(jobs, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 0 {
		t.Fatalf("unbatchable = %v, want none", unb)
	}

	// Units placed per job id must equal the quantity that job actually had.
	want := map[string]int{"bulk": 6}
	for i := range 9 {
		want[fmt.Sprintf("j%d", i)] = 1
	}
	got := map[string]int{}
	for _, b := range batches {
		for _, j := range b.Jobs {
			got[j.ID] += j.Quantity
		}
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("job %s placed %d units, want %d", id, got[id], n)
		}
	}
	for id, n := range got {
		if want[id] == 0 {
			t.Errorf("job %s was placed (%d units) but was never offered", id, n)
		}
	}

	// A job of quantity 1 must not appear on two beds at all, split or not.
	beds := map[string]int{}
	for _, b := range batches {
		seen := map[string]bool{}
		for _, j := range b.Jobs {
			if seen[j.ID] {
				t.Errorf("job %s appears twice on the same bed", j.ID)
			}
			seen[j.ID] = true
			beds[j.ID]++
		}
	}
	for i := range 9 {
		id := fmt.Sprintf("j%d", i)
		if beds[id] != 1 {
			t.Errorf("job %s is on %d beds, want exactly 1", id, beds[id])
		}
	}
}

// Oldest first, and a full bed before a partial one.
//
// The rule the shop stated: fill four from the oldest orders, keep filling
// while there are four to be had, and let whatever is left over sit as an
// under-full bed that the next order in that colour completes.
func TestGroupByColourFillsFullBedsBeforeLeavingARemainder(t *testing.T) {
	// Nine blue planks in order: two full beds and a remainder of one.
	var jobs []PlanJob
	for i := range 9 {
		jobs = append(jobs, plankJob(fmt.Sprintf("blue%d", i), "BLUE"))
	}

	batches, unb := GroupByColour(jobs, MaxColourBatchUnits, DefaultBedNester)
	if len(unb) != 0 {
		t.Fatalf("unbatchable = %v, want none", unb)
	}
	if len(batches) != 3 {
		t.Fatalf("got %d beds for 9 planks at 4 per bed, want 3", len(batches))
	}

	// The full beds come first and hold the OLDEST planks; the remainder is
	// last and holds the newest.
	want := [][]string{
		{"blue0", "blue1", "blue2", "blue3"},
		{"blue4", "blue5", "blue6", "blue7"},
		{"blue8"},
	}
	for i, b := range batches {
		var got []string
		for _, j := range b.Jobs {
			got = append(got, j.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(want[i]) {
			t.Errorf("bed %d holds %v, want %v", i, got, want[i])
		}
	}
	if batches[2].UnitsPerBed != 1 {
		t.Errorf("the remainder bed holds %d, want the 1 left over", batches[2].UnitsPerBed)
	}
}
