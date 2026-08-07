// Package bedpack places print units onto a printer bed with a guillotine
// Best-Area-Fit bin packer, ported from print-queue-be's bed_packing_service. It
// is pure geometry: footprints in, placements out, no I/O. The result drives both
// batch planning (how many beds, how full) and the merged plate STL.
package bedpack

import "math"

// Bed dimensions and the gap kept between parts, in millimetres. A part must fit
// within (bed - its footprint - gap) to be placed. EdgeMarginMM is kept clear
// on all four sides of the bed too - a part's footprint never touches the
// physical bed edge, only the margin-inset placement envelope.
const (
	BedXMM       = 330.0
	BedYMM       = 320.0
	BedZMM       = 300.0
	GapMM        = 10.0
	EdgeMarginMM = 10.0
)

// bedAreaMM2 is the raw bed area used for the utilisation percentage - the
// full nominal bed, not the smaller margin-inset placement envelope, so
// "utilisation" reads as "share of the advertised bed," matching what a
// user sees printed on the machine's spec sheet.
const bedAreaMM2 = BedXMM * BedYMM

// UnitFootprint is one printable unit's bounding footprint. RefID links a
// placement back to its source (a job id, say).
type UnitFootprint struct {
	RefID string
	XMM   float64
	YMM   float64
	ZMM   float64
}

// Placement is where a unit was placed on the bed, and whether it was turned 90
// degrees to fit.
type Placement struct {
	RefID     string
	XOffsetMM float64
	YOffsetMM float64
	Rotated   bool
}

// freeRect is a free rectangle of bed still available for placement.
type freeRect struct {
	x, y, w, h float64
}

// Pack places the units in order using Best-Area-Fit (ties broken by the shorter
// leftover side), splitting each used rectangle guillotine-style. It returns the
// placements for the units that fit and the footprints of those that did not. A
// unit taller than the bed, or with no free rectangle large enough, is rejected;
// packing continues so later (smaller) units still get a chance. Every
// placement stays within the EdgeMarginMM-inset envelope, never the bed's
// physical edge.
func Pack(units []UnitFootprint) (placements []Placement, rejected []UnitFootprint) {
	free := []freeRect{{
		EdgeMarginMM, EdgeMarginMM,
		BedXMM - 2*EdgeMarginMM, BedYMM - 2*EdgeMarginMM,
	}}

	for _, u := range units {
		if u.ZMM > BedZMM {
			rejected = append(rejected, u)
			continue
		}
		idx, orient, ok := bestFit(free, u)
		if !ok {
			rejected = append(rejected, u)
			continue
		}
		fr := free[idx]
		placements = append(placements, Placement{
			RefID: u.RefID, XOffsetMM: fr.x, YOffsetMM: fr.y, Rotated: orient.rotated,
		})
		// Remove the chosen rectangle and add the guillotine leftovers.
		free = append(free[:idx:idx], free[idx+1:]...)
		free = append(free, splitFreeRect(fr, orient.w+GapMM, orient.h+GapMM)...)
	}
	return placements, rejected
}

// orientation is one candidate footprint orientation (0 or 90 degrees).
type orientation struct {
	w, h    float64
	rotated bool
}

// orientationsFor returns the axis-aligned orientations to try: just the original
// when square, otherwise the original and its 90-degree turn.
func orientationsFor(u UnitFootprint) []orientation {
	if u.XMM == u.YMM {
		return []orientation{{u.XMM, u.YMM, false}}
	}
	return []orientation{{u.XMM, u.YMM, false}, {u.YMM, u.XMM, true}}
}

// bestFit finds the free rectangle and orientation minimising (area waste, short
// leftover side). The first candidate wins on an exact tie, matching the source.
func bestFit(free []freeRect, u UnitFootprint) (int, orientation, bool) {
	bestIdx := -1
	var best orientation
	var bestWaste, bestShort float64
	for i, fr := range free {
		for _, o := range orientationsFor(u) {
			needW, needH := o.w+GapMM, o.h+GapMM
			if needW > fr.w || needH > fr.h {
				continue
			}
			waste := fr.w*fr.h - needW*needH
			short := math.Min(fr.w-needW, fr.h-needH)
			if bestIdx == -1 || waste < bestWaste || (waste == bestWaste && short < bestShort) {
				bestIdx, best, bestWaste, bestShort = i, o, waste, short
			}
		}
	}
	if bestIdx == -1 {
		return -1, orientation{}, false
	}
	return bestIdx, best, true
}

// splitFreeRect splits fr, from which a usedW x usedH area (including the gap) was
// taken from the top-left, into up to two leftover rectangles. The shorter
// leftover dimension decides which cut keeps the used height and which spans the
// full width. Only positive-area leftovers are returned.
func splitFreeRect(fr freeRect, usedW, usedH float64) []freeRect {
	leftoverW := fr.w - usedW
	leftoverH := fr.h - usedH

	var right, bottom freeRect
	if leftoverW <= leftoverH {
		right = freeRect{fr.x + usedW, fr.y, leftoverW, usedH}
		bottom = freeRect{fr.x, fr.y + usedH, fr.w, leftoverH}
	} else {
		right = freeRect{fr.x + usedW, fr.y, leftoverW, fr.h}
		bottom = freeRect{fr.x, fr.y + usedH, usedW, leftoverH}
	}

	out := make([]freeRect, 0, 2)
	for _, r := range [2]freeRect{right, bottom} {
		if r.w > 0 && r.h > 0 {
			out = append(out, r)
		}
	}
	return out
}

// UtilisationPercent is the share of the bed covered by the units' footprints
// (gaps and free space excluded), rounded to two decimals.
func UtilisationPercent(units []UnitFootprint) float64 {
	return Utilisation(units).Percent
}

// AreaStats is the raw occupied/free area in mm^2 behind UtilisationPercent,
// for callers (the batch API response) that want absolute figures alongside
// the percentage - derived on read, not stored, so no schema change was
// needed to add it.
type AreaStats struct {
	OccupiedMM2 float64
	FreeMM2     float64
	Percent     float64
}

// Utilisation computes occupied area, free area, and the percentage together
// from the same footprint sum, so the three numbers can never drift apart.
func Utilisation(units []UnitFootprint) AreaStats {
	var occupied float64
	for _, u := range units {
		occupied += u.XMM * u.YMM
	}
	free := bedAreaMM2 - occupied
	if free < 0 {
		free = 0
	}
	return AreaStats{
		OccupiedMM2: occupied,
		FreeMM2:     free,
		Percent:     math.Round(occupied/bedAreaMM2*100*100) / 100,
	}
}
