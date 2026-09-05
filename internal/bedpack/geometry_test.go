package bedpack

// The two properties a plate must have, whatever the packer decides: nothing
// hangs off the bed, and nothing touches its neighbour.
//
// Written when the fit rule changed to stop charging a clearance gap on each
// part's trailing edge - the change that took a 256mm bed from three 200x50
// planks to the four that physically fit. Those are the two things such a change
// could plausibly break, and neither is caught anywhere downstream: a plate with
// overlapping parts slices happily and crashes the print.

import (
	"fmt"
	"math"
	"testing"
)

// footprints returns n copies of one footprint, distinctly labelled.
func footprints(n int, x, y, z float64) []UnitFootprint {
	out := make([]UnitFootprint, 0, n)
	for i := range n {
		out = append(out, UnitFootprint{RefID: fmt.Sprintf("u%d", i), XMM: x, YMM: y, ZMM: z})
	}
	return out
}

// sizeOf returns a placement's on-bed extent, honouring the 90-degree turn.
func sizeOf(u UnitFootprint, p Placement) (w, h float64) {
	if p.Rotated {
		return u.YMM, u.XMM
	}
	return u.XMM, u.YMM
}

func checkPlate(t *testing.T, bed Bed, units []UnitFootprint, placements []Placement) {
	t.Helper()
	bed = bed.Normalised()
	byRef := map[string]UnitFootprint{}
	for _, u := range units {
		byRef[u.RefID] = u
	}

	type box struct {
		ref            string
		x0, y0, x1, y1 float64
	}
	var boxes []box
	for _, p := range placements {
		u, ok := byRef[p.RefID]
		if !ok {
			t.Fatalf("placement %q matches no unit", p.RefID)
		}
		w, h := sizeOf(u, p)
		b := box{p.RefID, p.XOffsetMM, p.YOffsetMM, p.XOffsetMM + w, p.YOffsetMM + h}

		// Inside the margin-inset envelope, never over the physical edge.
		if b.x0 < bed.EdgeMarginMM-1e-9 || b.y0 < bed.EdgeMarginMM-1e-9 ||
			b.x1 > bed.XMM-bed.EdgeMarginMM+1e-9 || b.y1 > bed.YMM-bed.EdgeMarginMM+1e-9 {
			t.Errorf("%s occupies (%.1f,%.1f)-(%.1f,%.1f), outside the %.0fx%.0f bed's %.0fmm margin",
				b.ref, b.x0, b.y0, b.x1, b.y1, bed.XMM, bed.YMM, bed.EdgeMarginMM)
		}
		boxes = append(boxes, b)
	}

	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			// Separation on at least one axis, by at least the gap. Overlap is
			// the failure that crashes a print; under-separation is the one that
			// stops a finished plank being lifted off without levering.
			apartX := b.x0-a.x1 >= bed.GapMM-1e-9 || a.x0-b.x1 >= bed.GapMM-1e-9
			apartY := b.y0-a.y1 >= bed.GapMM-1e-9 || a.y0-b.y1 >= bed.GapMM-1e-9
			if !apartX && !apartY {
				overlapX := math.Min(a.x1, b.x1) - math.Max(a.x0, b.x0)
				overlapY := math.Min(a.y1, b.y1) - math.Max(a.y0, b.y0)
				t.Errorf("%s and %s are not a %.0fmm gap apart (overlap %.1f x %.1f): "+
					"%s (%.1f,%.1f)-(%.1f,%.1f), %s (%.1f,%.1f)-(%.1f,%.1f)",
					a.ref, b.ref, bed.GapMM, overlapX, overlapY,
					a.ref, a.x0, a.y0, a.x1, a.y1, b.ref, b.x0, b.y0, b.x1, b.y1)
			}
		}
	}
}

func TestPackedPlatesNeverOverlapOrOverhang(t *testing.T) {
	beds := map[string]Bed{
		"P2S":      {XMM: 256, YMM: 256, ZMM: 256},
		"A2L":      DefaultBed,
		"H2C":      {XMM: 350, YMM: 320, ZMM: 325},
		"tightgap": {XMM: 330, YMM: 320, ZMM: 300, GapMM: 2},
	}
	shapes := map[string][3]float64{
		"dnp plank":  {200, 50, 40},
		"square":     {80, 80, 40},
		"tall thin":  {20, 300, 40},
		"tiny":       {5, 5, 5},
		"mixed edge": {236, 40, 40},
	}

	for bedName, bed := range beds {
		for shapeName, s := range shapes {
			for _, n := range []int{1, 2, 4, 7, 12} {
				units := footprints(n, s[0], s[1], s[2])
				placements, _ := PackOn(bed, units)
				t.Run(fmt.Sprintf("%s/%s/%d", bedName, shapeName, n), func(t *testing.T) {
					checkPlate(t, bed, units, placements)
				})
			}
		}
	}
}

// A mixed plate is the real case: a bed of planks with a couple of small parts
// tucked in beside them.
func TestPackedMixedPlateNeverOverlaps(t *testing.T) {
	units := append(footprints(4, 200, 50, 40),
		UnitFootprint{RefID: "hook1", XMM: 40, YMM: 30, ZMM: 20},
		UnitFootprint{RefID: "hook2", XMM: 40, YMM: 30, ZMM: 20},
		UnitFootprint{RefID: "plate", XMM: 90, YMM: 90, ZMM: 15},
	)
	placements, _ := Pack(units)
	checkPlate(t, DefaultBed, units, placements)
}
