package httpapi

import (
	"strings"
	"testing"

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
			name:     "jobs genuinely disagree",
			families: []string{"H2C", "H2S"},
			wantOK:   false,
			wantWhy:  "disagree",
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
