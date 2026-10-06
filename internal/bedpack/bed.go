package bedpack

// A bed, as a value rather than a compile-time constant.
//
// The package was written when the fleet was one machine class, so the bed was
// five constants and every caller inherited 330x320x300. The fleet is three
// classes with three build areas, and the difference is not cosmetic: the same
// 200x50 plank fits four times on a P2S, six on an A2L and seven on an H2C. With
// one global bed, every plate was built to the smallest of them, which threw
// away a third of the largest machine's throughput on every print.
//
// The constants stay, and stay authoritative, as DefaultBed - so every existing
// caller keeps the bed it had. What is new is that a caller CAN name a bed.

// Bed is one printer's build area and the clearances kept on it.
//
// GapMM is kept between neighbouring parts; EdgeMarginMM is kept clear on all
// four sides, so a footprint never touches the physical bed edge. ColumnGapMM is
// the wider clearance the single-column layout uses - enough to get fingers and a
// scraper between two finished planks.
type Bed struct {
	XMM float64
	YMM float64
	ZMM float64
	// XOriginMM is where the usable area STARTS, not where it is wide.
	//
	// Zero on a one-nozzle machine, where the bed is the bed. On an H2C the two
	// extruders reach different parts of it - the preset says extruder 1 covers
	// X 0..325 and extruder 2 covers X 25..330 - so a plate both nozzles must
	// reach has to live in the overlap, X 25..325.
	//
	// Nothing told the packer that. Plates were laid out from the usual 10mm
	// margin, and a two-colour plank starting at X=10 put its SECOND colour
	// where the second nozzle cannot go: "Found G-code in unprintable area of
	// multi-extruder printers after slicing", on 115459-PURPLE.
	XOriginMM    float64
	GapMM        float64
	EdgeMarginMM float64
	ColumnGapMM  float64
	// WipeTowerMM is the depth of the band kept clear at the back of the bed
	// for the prime tower. Every plate here prints in two filaments - a white
	// body and a coloured lettering pass - so the slicer builds a tower on
	// every one of them, and it has to stand somewhere.
	//
	// Nothing told the packer that. Models were packed to the bed edge, the
	// tower went where the preset put it, and BambuBuddy answered "G-code
	// conflicts detected after slicing ... try moving the wipe tower further
	// from other models" - BATCH-1002076 locally, then BATCH-1000598 on P2 in
	// production, a three-unit bed that can only ever go to a P2S and so
	// retried into the same wall every fifteen minutes.
	WipeTowerMM float64
}

// DefaultBed is the bed this package assumed before beds were values: the
// original constants, unchanged.
var DefaultBed = Bed{
	XMM: BedXMM, YMM: BedYMM, ZMM: BedZMM,
	GapMM: GapMM, EdgeMarginMM: EdgeMarginMM, ColumnGapMM: ColumnGapMM,
	WipeTowerMM: WipeTowerMM,
}

// Normalised fills in anything the caller left at zero from DefaultBed.
//
// A partly-filled Bed is the dangerous shape: XMM of zero is not "no limit", it
// is a bed nothing fits on, and a config file naming only the build area would
// otherwise silently set every clearance to nothing. Filling the gaps is what
// lets a caller say "350 by 320" and mean only that.
func (b Bed) Normalised() Bed {
	if b.XMM <= 0 {
		b.XMM = DefaultBed.XMM
	}
	if b.YMM <= 0 {
		b.YMM = DefaultBed.YMM
	}
	if b.ZMM <= 0 {
		b.ZMM = DefaultBed.ZMM
	}
	if b.GapMM <= 0 {
		b.GapMM = DefaultBed.GapMM
	}
	if b.EdgeMarginMM <= 0 {
		b.EdgeMarginMM = DefaultBed.EdgeMarginMM
	}
	if b.ColumnGapMM <= 0 {
		b.ColumnGapMM = DefaultBed.ColumnGapMM
	}
	// Filled from the default like every other clearance, because a partly
	// written Bed silently losing the reservation is the exact failure this
	// field exists to prevent. A plate that genuinely needs no tower says so
	// with NoWipeTower rather than by leaving the field alone.
	switch {
	case b.WipeTowerMM == 0:
		b.WipeTowerMM = DefaultBed.WipeTowerMM
	case b.WipeTowerMM < 0:
		b.WipeTowerMM = 0
	}
	return b
}

// NoWipeTower is the band for a plate that prints in a single filament: none.
//
// A value rather than 0, because 0 means "unset" for every other clearance on
// Bed and is filled in from the default.
const NoWipeTower = -1.0

// ModelYMM is how deep the bed is for MODELS: the build area, less the edge
// margins, less the band kept for the prime tower.
//
// Never negative, and never the whole bed: a tower band wider than the bed
// would silently pack nothing at all, and "no bed" is not what a misconfigured
// reservation means.
func (b Bed) ModelYMM() float64 {
	usable := b.YMM - 2*b.EdgeMarginMM
	if b.WipeTowerMM <= 0 || b.WipeTowerMM >= usable {
		return usable
	}
	return usable - b.WipeTowerMM
}

// AreaMM2 is the raw build area, the denominator for utilisation.
//
// The full nominal bed, not the smaller margin-inset envelope, so "utilisation"
// reads as "share of the advertised bed" - matching what is printed on the
// machine's spec sheet.
func (b Bed) AreaMM2() float64 { return b.XMM * b.YMM }

// Bed sizes per machine family.
//
// One bed per class, because a plate is packed at fixed offsets and the slicer
// is told not to rearrange it: a plate laid out for one class is a plate the
// smaller classes physically cannot print. Only the smaller ones - a larger bed
// takes it unchanged, which is what FitForFamily answers and what keeps the
// biggest machines from sitting idle. Which of them should have it is a routing
// question, not a geometric one, and is decided in httpapi. Four planks packed on the A2L bed
// come out 270x270, and a P2S is 256x256 - which is why a bed sent to one
// answered "G-code conflicts detected after slicing" with nowhere left for the
// wipe tower.
//
// Z is the build height, not a limit anything here enforces; it is carried so
// a caller asking for "the P2S bed" gets the whole answer.
var (
	// BedH2C is the area BOTH its nozzles can reach, not the whole bed.
	//
	// It used to read 350x320x325, which is bigger than the machine: the
	// printer's own preset gives printable_area 330x320 and printable_height
	// 325 for extruder 2 but only 320 for extruder 1. Worse for a two-colour
	// plate, the extruders cover DIFFERENT strips - extruder 1 is X 0..325,
	// extruder 2 is X 25..330 - so anything both must reach lives in the
	// overlap: X 25..325, 300mm wide, starting 25mm in.
	//
	// Z is extruder 1's 320, not extruder 2's 325: a plate is only as tall as
	// the shorter nozzle can clear.
	//
	// Narrower than before, so a bed that used to take seven planks may now
	// take six. That is the machine's real reach; the old number simply packed
	// parts where a nozzle could not follow.
	BedH2C = Bed{XOriginMM: 25, XMM: 300, YMM: 320, ZMM: 320}
	// BedA2L holds 6. The same size this package assumed for every machine
	// before beds were per-class, less the 25mm shared origin below.
	BedA2L = Bed{XOriginMM: 25, XMM: 305, YMM: 320, ZMM: 325}
	// BedP2S holds 4, and is the one the old assumption was wrong about.
	BedP2S = Bed{XOriginMM: 25, XMM: 231, YMM: 256, ZMM: 256}
)

// Every bed starts at X=25, including the two that have one nozzle.
//
// A one-nozzle machine has no reason of its own to avoid the first 25mm - the
// A2L really does reach X=0, and the P2S too. They give it up so that ONE
// PLATE IS PRINTABLE ON ANY MACHINE.
//
// The alternative is to pack each plate for its own class and re-lay it when
// it goes somewhere else, which is what the code claimed to do and never did:
// a bed of three units is classed P2S, laid out from X=10, and then handed to
// an H2C by the two-tier fallback because no P2S was free. On that machine the
// second nozzle cannot reach before X=25, so the lettering of a two-colour
// plank sat 2mm inside a strip no nozzle could follow, and every such plate was
// refused with "Found G-code in unprintable area of multi-extruder printers".
//
// Sharing the origin costs nothing that matters. These plates are columns of
// identical planks stacked in Y, so the limit on how many fit is the bed's
// DEPTH; 25mm off the width changes the count on none of the three. It narrows
// only the mixed-footprint layout, which this shop's beds do not use.
//
// The right edges are untouched: A2L still ends at 330 and P2S at 256, so the
// widths above are 25mm smaller than the machines, not the areas.

// BedForFamily is the bed a machine family prints on.
//
// An unknown family falls back to the SMALLEST bed, not the default one. A
// plate that fits the smallest bed fits them all, so the cost of not
// recognising a family is a less full plate; the cost of guessing large is a
// plate that cannot print on the machine it was built for.
func BedForFamily(family string) Bed {
	if bed, ok := bedForKnownFamily(family); ok {
		return bed
	}
	return BedP2S.Normalised()
}

// TowerOrigin reports where the prime tower belongs on this bed: the corner of
// the band every layout here already keeps clear for it.
//
// THE BUG THIS FIXES. Tensor asked for the tower and never said where, so
// BambuBuddy used its own default - wipe_tower_x 15 - and an H2C's second
// extruder cannot reach before X=25. The tower is 60mm wide, so it straddled
// that line, and every purge move the second nozzle made was off its own
// printable area. BambuBuddy then refused the whole plate:
//
//	Found G-code in unprintable area of multi-extruder printers after slicing.
//
// Measured on batch 115483's plate, same file, same presets, only this moved:
//
//	wipe_tower_x 15   ->  refused
//	wipe_tower_x 165  ->  sliced, filament_maps "2 1", 54.91g + 46.57g
//
// So the parts were never the problem - they sit at X 35..235 - and neither was
// the model. It was the one place on the plate nobody had placed.
//
// WHICH BAND. The reservation comes off the width when the plate fits beside
// the tower and off the depth when it does not. PackColumnOn asks
// FitsBesideTower outright; PackOn tries the narrow envelope first, and that
// envelope's width is the very quantity FitsBesideTower measures. So asking it
// here follows the layout rather than guessing at it - and a tower dropped in
// the band that was NOT kept clear lands on top of a part.
//
// ok is false for a single-filament plate, which reserves no band and never
// purges. Nothing should be sent for those: they slice correctly today.
func (b Bed) TowerOrigin(units []UnitFootprint) (xMM, yMM float64, ok bool) {
	b = b.Normalised()
	if b.WipeTowerMM <= 0 {
		return 0, 0, false
	}
	if FitsBesideTower(b, units) {
		// Beside, flush with the right edge of the usable area - which is
		// where limitX -= WipeTowerMM stopped the parts.
		return b.XOriginMM + b.XMM - b.EdgeMarginMM - b.WipeTowerMM, b.EdgeMarginMM, true
	}
	// Behind, past the depth the parts were held to.
	return b.XOriginMM + b.EdgeMarginMM, b.EdgeMarginMM + b.ModelYMM(), true
}
