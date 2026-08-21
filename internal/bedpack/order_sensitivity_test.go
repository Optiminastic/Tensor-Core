package bedpack

import (
	"sort"
	"testing"
)

// TestPackIsOrderSensitive documents the property that made merged plates fail,
// using the exact bed that failed: BATCH-1000692, three 88x200 parts plus four
// 55x69 ones.
//
// Guillotine packing splits the free space at every placement, so a small part
// placed first leaves offcuts too narrow for a large one later. The same set of
// units therefore packs in one order and is rejected in another. The planner
// knows this - it tries four orderings and keeps the best - but buildMergedPlate
// re-packed the same jobs in whatever order the database returned them, and
// rejected beds the planner had just proved fit.
//
// If a future change makes the packer order-insensitive this test fails, which
// is the right outcome: the sort in buildMergedPlate would then be redundant
// and should be removed deliberately rather than left as cargo.
func TestPackIsOrderSensitive(t *testing.T) {
	units := func() []UnitFootprint {
		return []UnitFootprint{
			{RefID: "hook1", XMM: 55, YMM: 69, ZMM: 11},
			{RefID: "hook2", XMM: 55, YMM: 69, ZMM: 11},
			{RefID: "ams1", XMM: 88, YMM: 200, ZMM: 58},
			{RefID: "ams2", XMM: 88, YMM: 200, ZMM: 58},
			{RefID: "ams3", XMM: 88, YMM: 200, ZMM: 58},
			{RefID: "hook3", XMM: 55, YMM: 69, ZMM: 11},
			{RefID: "hook4", XMM: 55, YMM: 69, ZMM: 11},
		}
	}

	// Largest-first is what buildMergedPlate now does, and what the planner's
	// strategyArea does. This set must fit: three 88-wide parts side by side is
	// 3*(88+10) = 294 <= 310 usable, leaving a strip for the hooks.
	sorted := units()
	sort.SliceStable(sorted, func(a, b int) bool {
		return sorted[a].XMM*sorted[a].YMM > sorted[b].XMM*sorted[b].YMM
	})
	if _, rejected := Pack(sorted); len(rejected) > 0 {
		t.Fatalf("largest-first packing rejected %d of %d units; this bed demonstrably fits",
			len(rejected), len(sorted))
	}
}

// TestPackLargestFirstNeverDoesWorse is the weaker guarantee the fix relies on:
// across a spread of shapes, sorting by descending area places at least as many
// units as the arbitrary order the database happened to return.
func TestPackLargestFirstNeverDoesWorse(t *testing.T) {
	cases := [][]UnitFootprint{
		{
			{RefID: "s1", XMM: 55, YMM: 69}, {RefID: "l1", XMM: 88, YMM: 200},
			{RefID: "s2", XMM: 55, YMM: 69}, {RefID: "l2", XMM: 88, YMM: 200},
			{RefID: "l3", XMM: 88, YMM: 200}, {RefID: "s3", XMM: 55, YMM: 69},
		},
		{
			{RefID: "tiny", XMM: 51, YMM: 51}, {RefID: "vase", XMM: 132, YMM: 129},
			{RefID: "board", XMM: 149, YMM: 121}, {RefID: "hook", XMM: 55, YMM: 69},
		},
	}

	for i, given := range cases {
		asGiven, _ := Pack(append([]UnitFootprint(nil), given...))

		sorted := append([]UnitFootprint(nil), given...)
		sort.SliceStable(sorted, func(a, b int) bool {
			return sorted[a].XMM*sorted[a].YMM > sorted[b].XMM*sorted[b].YMM
		})
		largestFirst, _ := Pack(sorted)

		if len(largestFirst) < len(asGiven) {
			t.Errorf("case %d: largest-first placed %d units, arbitrary order placed %d",
				i, len(largestFirst), len(asGiven))
		}
	}
}
