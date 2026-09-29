package bedpack

import "strings"

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
	XMM          float64
	YMM          float64
	ZMM          float64
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
// smaller classes physically cannot print. Four planks packed on the A2L bed
// come out 270x270, and a P2S is 256x256 - which is why a bed sent to one
// answered "G-code conflicts detected after slicing" with nowhere left for the
// wipe tower.
//
// Z is the build height, not a limit anything here enforces; it is carried so
// a caller asking for "the P2S bed" gets the whole answer.
var (
	// BedH2C is the largest: 7 planks fit, measured.
	BedH2C = Bed{XMM: 350, YMM: 320, ZMM: 325}
	// BedA2L holds 6. The same size this package assumed for every machine
	// before beds were per-class.
	BedA2L = Bed{XMM: 330, YMM: 320, ZMM: 325}
	// BedP2S holds 4, and is the one the old assumption was wrong about.
	BedP2S = Bed{XMM: 256, YMM: 256, ZMM: 256}
)

// BedForFamily is the bed a machine family prints on.
//
// An unknown family falls back to the SMALLEST bed, not the default one. A
// plate that fits the smallest bed fits them all, so the cost of not
// recognising a family is a less full plate; the cost of guessing large is a
// plate that cannot print on the machine it was built for.
func BedForFamily(family string) Bed {
	switch strings.ToUpper(strings.TrimSpace(family)) {
	case "H2C":
		return BedH2C.Normalised()
	case "A2L":
		return BedA2L.Normalised()
	case "P2S":
		return BedP2S.Normalised()
	default:
		return BedP2S.Normalised()
	}
}
