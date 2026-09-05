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
	XMM          float64
	YMM          float64
	ZMM          float64
	GapMM        float64
	EdgeMarginMM float64
	ColumnGapMM  float64
}

// DefaultBed is the bed this package assumed before beds were values: the
// original constants, unchanged.
var DefaultBed = Bed{
	XMM: BedXMM, YMM: BedYMM, ZMM: BedZMM,
	GapMM: GapMM, EdgeMarginMM: EdgeMarginMM, ColumnGapMM: ColumnGapMM,
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
	return b
}

// AreaMM2 is the raw build area, the denominator for utilisation.
//
// The full nominal bed, not the smaller margin-inset envelope, so "utilisation"
// reads as "share of the advertised bed" - matching what is printed on the
// machine's spec sheet.
func (b Bed) AreaMM2() float64 { return b.XMM * b.YMM }
