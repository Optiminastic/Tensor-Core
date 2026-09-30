package production

// Which class of printer a bed is built for, decided by how full it is.
//
// The shop runs three classes and wants the big beds earning their keep: a bed
// of five goes to an H2C, four to an A2L, three to a P2S. Not a fit rule -
// measured with the packer, an H2C takes seven planks, an A2L six and a P2S
// four - but a routing rule, so a three-plank plate does not occupy the only
// machine that can take a five-plank one.
//
// Routing, though, is a preference and not a law: bedpack.FitForFamily is the
// fit half, and a plate whose own class is unavailable may go to a larger bed
// rather than wait. This function decides what a bed is BUILT for; it does not
// decide alone where it ends up.
//
// It decides the PACKING too, and that half is not a preference. A plate is
// laid out at fixed offsets and the slicer is told not to rearrange it, so a
// plate built for one class is a plate the smaller classes cannot print. Four
// planks packed on the A2L bed come out 270x270; a P2S is 256x256.

// Bed fill, in units - a job for three planks fills three places, not one.
const (
	// MinBedUnits is how empty a bed may be and still print. Below it the bed
	// waits for company, because a plate with one plank on it costs the same
	// machine-hour as a plate with five.
	//
	// A bed carrying a priority job locks anyway: somebody paid to jump the
	// queue and waiting for company would spend that money on nothing.
	MinBedUnits = 3
	// MaxBedUnits is the fullest a bed gets. Five, not the seven an H2C could
	// hold, because the classes below it must be able to take a bed too - and
	// a bed nothing but the three H2Cs can print is a bed that waits.
	MaxBedUnits = 5
)

// BedFamilyForUnits is the printer class a bed of this many units is built for.
//
// Sizes outside the range resolve to the nearest class that can hold them
// rather than to nothing: the planner should never produce them, and a bed
// with no class is a bed that never prints.
func BedFamilyForUnits(units int) string {
	switch {
	case units >= MaxBedUnits:
		return "H2C"
	case units == 4:
		return "A2L"
	default:
		// Three, and anything smaller that reached here anyway. The smallest
		// bed, so the plate fits every machine on the floor.
		return "P2S"
	}
}
