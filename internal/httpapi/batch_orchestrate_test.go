package httpapi

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// TestBatchMachineFamilyDistinguishesItsTwoFailures pins the reason string,
// not just the decision.
//
// Both failures leave the batch with machine_id null, and an unassigned batch
// is permanently stranded: ListApprovableDraftsForMachine selects a machine's
// drafts *by* machine_id, so no printer can ever see it. The batch sits in
// Draft while the floor idles, and nothing errors.
//
// They have opposite causes, though. "No family at all" means the jobs' designs
// carry no printer profile - a data problem in the design catalogue. "Disagree"
// means the planner put incompatible jobs on one bed - a grouping problem in
// production.groupKey. Collapsing both into one "spans more than one machine
// family" message cost real debugging time on exactly this stall: the log
// described a mixed bed, while every job on it actually had no family at all.
func TestBatchMachineFamilyDistinguishesItsTwoFailures(t *testing.T) {
	tests := []struct {
		name       string
		families   []string
		wantFamily string
		wantOK     bool
		wantWhy    string // substring
	}{
		{
			name:       "all jobs agree",
			families:   []string{"H2C", "H2C"},
			wantFamily: "H2C",
			wantOK:     true,
		},
		{
			name:       "unknown families are ignored, not treated as distinct",
			families:   []string{"H2C", "", "H2C"},
			wantFamily: "H2C",
			wantOK:     true,
		},
		{
			name:     "no job records a family at all",
			families: []string{"", "", ""},
			wantOK:   false,
			wantWhy:  "no printer profile",
		},
		{
			// Disagreement is RESOLVED, not refused. The class is chosen when
			// the bed is queued, and refusing here stranded a bed nobody
			// could see. Two H2C units against one H2S: the H2C wins.
			name:       "jobs disagree and the majority of units wins",
			families:   []string{"H2C", "H2S", "H2C"},
			wantFamily: "H2C",
			wantOK:     true,
		},
		{
			// Tie broken by the earliest job, which is oldest-order-first, so
			// the answer does not move between planning passes.
			name:       "an even split goes to the earliest job",
			families:   []string{"H2S", "H2C"},
			wantFamily: "H2S",
			wantOK:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jobs := make([]production.PlanJob, len(tc.families))
			for i, f := range tc.families {
				jobs[i] = production.PlanJob{MachineFamily: f}
			}

			family, ok, why := batchMachineFamily(jobs)

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (why=%q)", ok, tc.wantOK, why)
			}
			if family != tc.wantFamily {
				t.Errorf("family = %q, want %q", family, tc.wantFamily)
			}
			if tc.wantOK {
				if why != "" {
					t.Errorf("why = %q, want empty on success", why)
				}
				return
			}
			if !strings.Contains(why, tc.wantWhy) {
				t.Errorf("why = %q, want it to mention %q; the log message is the only signal "+
					"a human gets that this batch is permanently stranded, so it must name the real cause",
					why, tc.wantWhy)
			}
		})
	}
}

// The filament a mixed bed prints in, and why it has to be said out loud.
//
// One plate is stamped with one material for every part and printed at one bed
// temperature. Now that a bed is a colour and nothing else, a bed CAN hold two
// materials - so something has to win, and it used to be whichever job came
// first, silently. It is the material most of the plate is, and a mixed bed
// logs it: PLA wants about 55C where PETG wants 70 and ABS 90, so getting this
// wrong does not merely slice badly, it warps one of the two.
func TestBatchMaterialTakesWhatMostOfThePlateIs(t *testing.T) {
	row := func(material string, qty int32) gen.ProductionJob {
		j := gen.ProductionJob{Quantity: qty}
		if material != "" {
			m := material
			j.Material = &m
		}
		return j
	}

	for _, c := range []struct {
		name string
		jobs []gen.ProductionJob
		want string
	}{
		{"one material", []gen.ProductionJob{row("PLA", 1), row("PLA", 2)}, "PLA"},
		// By UNITS, not by jobs: one PETG job of three planks outweighs two
		// PLA jobs of one each, because most of the plate is PETG.
		{"units decide, not job count", []gen.ProductionJob{row("PLA", 1), row("PETG", 3), row("PLA", 1)}, "PETG"},
		// A tie goes to the earliest job, so the answer is stable.
		{"a tie goes to the earliest", []gen.ProductionJob{row("PETG", 2), row("PLA", 2)}, "PETG"},
		// A job recording nothing is ignored rather than counted as a material.
		{"blank materials are ignored", []gen.ProductionJob{row("", 5), row("PLA", 1)}, "PLA"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := batchMaterialFromRows(c.jobs)
			if got == nil {
				t.Fatalf("material = nil, want %q", c.want)
			}
			if *got != c.want {
				t.Errorf("material = %q, want %q", *got, c.want)
			}
		})
	}

	// A bed whose jobs record nothing has no material, which is not the same
	// as a bed that prints in the first thing we thought of.
	if got := batchMaterialFromRows([]gen.ProductionJob{row("", 1), row("", 2)}); got != nil {
		t.Errorf("material = %q, want nil when no job records one", *got)
	}
}
