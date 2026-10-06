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
	// WipeTowerMM is the depth kept clear at the back of every bed for the
	// prime tower. 60mm is Bambu Studio's default prime-tower width; a plate
	// that leaves less than that has nowhere for the tower to stand, and the
	// slice fails after the upload rather than before it.
	WipeTowerMM = 60.0
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
	return PackOn(DefaultBed, units)
}

// PackOn is Pack on a named bed - see Bed.
func PackOn(bed Bed, units []UnitFootprint) (placements []Placement, rejected []UnitFootprint) {
	bed = bed.Normalised()
	// The tower needs a rectangle of its own somewhere on the bed, and which
	// side it comes off costs different amounts on different beds. Taking it
	// off the width leaves the full depth, which is what lets five planks
	// stand in one column on an H2C; taking it off the depth is the only
	// option on a P2S, where a 200mm plank leaves 36mm beside it.
	//
	// So the narrow envelope is tried first and the shallow one only if
	// something did not fit. Trying, rather than reasoning about the shapes:
	// the packer rotates parts, and which envelope suits a set is a question
	// about the set, not about the bed.
	if bed.WipeTowerMM > 0 {
		narrow := freeRect{bed.XOriginMM + bed.EdgeMarginMM, bed.EdgeMarginMM,
			bed.XMM - 2*bed.EdgeMarginMM - bed.WipeTowerMM, bed.YMM - 2*bed.EdgeMarginMM}
		shallow := freeRect{bed.XOriginMM + bed.EdgeMarginMM, bed.EdgeMarginMM,
			bed.XMM - 2*bed.EdgeMarginMM, bed.ModelYMM()}
		if p, r := packWithin(bed, narrow, units); len(r) == 0 {
			return p, r
		} else if ps, rs := packWithin(bed, shallow, units); len(rs) <= len(r) {
			return ps, rs
		} else {
			return p, r
		}
	}
	free := []freeRect{{
		bed.XOriginMM + bed.EdgeMarginMM, bed.EdgeMarginMM,
		bed.XMM - 2*bed.EdgeMarginMM, bed.YMM - 2*bed.EdgeMarginMM,
	}}

	return packWithin(bed, free[0], units)
}

// packWithin is PackOn's loop, run against one placement envelope.
func packWithin(bed Bed, envelope freeRect, units []UnitFootprint) (placements []Placement, rejected []UnitFootprint) {
	free := []freeRect{envelope}
	for _, u := range units {
		if u.ZMM > bed.ZMM {
			rejected = append(rejected, u)
			continue
		}
		idx, orient, ok := bestFit(free, u, bed.GapMM)
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
		free = append(free, splitFreeRect(fr, orient.w+bed.GapMM, orient.h+bed.GapMM)...)
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
func bestFit(free []freeRect, u UnitFootprint, gap float64) (int, orientation, bool) {
	bestIdx := -1
	var best orientation
	var bestWaste, bestShort float64
	for i, fr := range free {
		for _, o := range orientationsFor(u) {
			// The part itself has to fit; the gap does not. A gap is clearance
			// BETWEEN neighbours, and it is charged when the rectangle is split
			// below - so the next part starts past it. Charging it here as well
			// made every part cost an extra gap of bed on its trailing edge,
			// which is what held a 256mm bed to three 200x50 planks when four
			// physically fit (4*50 + 3*10 = 230mm of 236mm usable).
			if o.w > fr.w || o.h > fr.h {
				continue
			}
			// The metric is measured on the clamped claim, so a part that ends
			// flush against the far edge is not scored as if it wasted the gap
			// it never used. The SPLIT below still charges the full gap, so a
			// neighbour is always a full gap away.
			needW, needH := math.Min(o.w+gap, fr.w), math.Min(o.h+gap, fr.h)
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

// UtilisationPercentOn is UtilisationPercent against a named bed. A bed of four
// planks reads 37.9% on an A2L and 61.0% on a P2S - the same four planks, a
// smaller bed - so the number only means anything alongside the bed it was
// measured on.
func UtilisationPercentOn(bed Bed, units []UnitFootprint) float64 {
	return UtilisationOn(bed, units).Percent
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
	return UtilisationOn(DefaultBed, units)
}

// UtilisationOn is Utilisation against a named bed.
func UtilisationOn(bed Bed, units []UnitFootprint) AreaStats {
	bed = bed.Normalised()
	area := bed.AreaMM2()
	var occupied float64
	for _, u := range units {
		occupied += u.XMM * u.YMM
	}
	free := area - occupied
	if free < 0 {
		free = 0
	}
	return AreaStats{
		OccupiedMM2: occupied,
		FreeMM2:     free,
		Percent:     math.Round(occupied/area*100*100) / 100,
	}
}

// ColumnGapMM is the clearance PackColumn keeps between parts, wider than the
// general GapMM.
//
// 15mm at the shop's instruction: enough to get fingers and a scraper between
// two finished planks without levering against a neighbour. It applies only to
// the column layout, which is deliberate - raising GapMM itself would push some
// beds that demonstrably pack today over the edge (three 88x200 parts plus four
// hooks, the bed order_sensitivity_test documents, stops fitting), and a bed
// that no longer fits is a worse outcome than one packed 5mm tighter. A column
// has room to spare either way: four 50mm-deep planks need 4*50 + 3*15 = 245mm
// of the 300mm envelope.
const ColumnGapMM = 15.0

// PackColumn places every unit in one column, unrotated, in the order given.
//
// A deliberately worse packer than Pack, chosen for what the bed has to look
// like rather than for how much fits on it. Pack is a guillotine best-area-fit:
// on a bed of four identical planks it turns some of them 90 degrees and grids
// them two-by-two, because that wastes least area. The result is a plate whose
// planks face different ways, which is harder to read, harder to lift off in
// order, and not what the shop asked for.
//
// Every unit keeps its orientation and its x, so the column reads top to bottom
// in the order the caller supplied - which for a colour batch is oldest order
// first. Units that do not fit are rejected rather than turned or moved into a
// second column: this returns a column or nothing, and the caller falls back to
// Pack when nothing is what it gets.
func PackColumn(units []UnitFootprint) (placements []Placement, rejected []UnitFootprint) {
	return PackColumnOn(DefaultBed, units)
}

// PackColumnOn is PackColumn on a named bed.
func PackColumnOn(bed Bed, units []UnitFootprint) (placements []Placement, rejected []UnitFootprint) {
	bed = bed.Normalised()
	y := bed.EdgeMarginMM
	// Where the column starts, and where it must stop.
	//
	// Both are measured from XOriginMM, which this function used to ignore
	// entirely - it placed every unit at EdgeMarginMM and compared against
	// XMM - EdgeMarginMM, as though the usable area always began at zero. On
	// the one bed where it does not, that is the whole bug: an H2C's second
	// nozzle cannot reach before X=25, every plank was laid from X=10, and a
	// two-colour plate put its lettering where that nozzle cannot go.
	//
	// XMM is a WIDTH, not a right edge, which is what made the old arithmetic
	// look correct: on a bed whose origin is zero the two are the same number.
	// PackOn had this right (see the envelope it builds); only this layout did
	// not, and this is the layout a bed of identical planks actually takes.
	left := bed.XOriginMM + bed.EdgeMarginMM
	// A column is one plank wide, so the tower usually fits beside it - and
	// beside is free, where behind costs the wide gap between planks. Only a
	// bed too narrow for both pays in depth.
	limitX := bed.XOriginMM + bed.XMM - bed.EdgeMarginMM
	limitY := bed.YMM - bed.EdgeMarginMM
	if bed.WipeTowerMM > 0 && !FitsBesideTower(bed, units) {
		limitY = bed.EdgeMarginMM + bed.ModelYMM()
	} else if bed.WipeTowerMM > 0 {
		limitX -= bed.WipeTowerMM
	}
	gap := columnGapFor(bed, limitY-bed.EdgeMarginMM, units)

	for _, u := range units {
		if u.ZMM > bed.ZMM || left+u.XMM > limitX || y+u.YMM > limitY {
			rejected = append(rejected, u)
			continue
		}
		placements = append(placements, Placement{
			RefID: u.RefID, XOffsetMM: left, YOffsetMM: y, Rotated: false,
		})
		y += u.YMM + gap
	}
	return placements, rejected
}

// columnGapFor is the clearance between planks in a single column.
//
// ColumnGapMM by default - wide enough to get fingers and a scraper between two
// finished planks, which is what the shop asked for. Tightened to the ordinary
// part gap only when the wide one would push the column past the tower band:
// three planks on a P2S need 180mm at 15mm apart and 170mm at 10mm, and the
// band leaves 176mm. Rejecting the third plank instead would leave a bed of two
// - below the floor, so it would never print at all - over 10mm of finger room.
func columnGapFor(bed Bed, depthAvailable float64, units []UnitFootprint) float64 {
	if len(units) < 2 {
		return bed.ColumnGapMM
	}
	depth := 0.0
	for _, u := range units {
		depth += u.YMM
	}
	if depth+bed.ColumnGapMM*float64(len(units)-1) <= depthAvailable {
		return bed.ColumnGapMM
	}
	return bed.GapMM
}

// FitsBesideTower reports whether a single column of these units leaves the
// tower room beside it, rather than behind it.
//
// Width-relative and deliberately origin-free: it asks how much room there is,
// not where that room begins. Adding XOriginMM here would subtract the offset
// twice, since the caller's limitX already starts from it.
func FitsBesideTower(bed Bed, units []UnitFootprint) bool {
	width := bed.XMM - 2*bed.EdgeMarginMM - bed.WipeTowerMM
	for _, u := range units {
		if u.XMM > width {
			return false
		}
	}
	return true
}
