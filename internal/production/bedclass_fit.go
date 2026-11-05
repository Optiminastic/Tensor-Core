package production

import "github.com/Optiminastic/tensor-core/internal/bedpack"

// bedEscalation is the classes a plate may be promoted through, smallest
// usable area first.
//
// Not the same order as BedFamilyForUnits reaches them by COUNT, and that is
// the point: that function answers "how many units am I carrying", this one
// answers "do they physically fit". A bed of one normally routes to the P2S
// because the smallest plate prints on every machine on the floor; a bed of
// one carrying a 298 mm part cannot.
//
// The H2C is before the A2L although it is 5 mm narrower, because there are
// three of them and one A2L: a plate that fits both should not be sent to
// the single machine everything else is already queued behind.
var bedEscalation = []string{"P2S", "H2C", "A2L"}

// bedFamilyFitting is the class this plate should print on: the one its unit
// count asks for, promoted until the units actually fit.
//
// THE BUG THIS FIXES. The class came from the count alone, and the packing
// loop then shrank its trial until the units fitted that class - so a part
// too big for the P2S reduced a bed of one to a bed of NONE. An empty bed is
// dropped rather than emitted, nothing is recorded against the job, and the
// loop opens a fresh empty bed and asks again. For ever.
//
// Measured on The Social Mirror: its name piece is 298.3 x 226.1 mm, which
// fits the H2C's 300 x 320 and so passes the "too large for any bed" guard,
// and does not fit the P2S's 231 x 256 in either orientation. The first
// render that gave that job a bounding box stopped every batch on the floor,
// with 54 plan_batches runs queued behind the one that never returned.
//
// Falls back to the largest class rather than to nothing: a bed with no
// class is a bed no printer picks up, and the caller has already refused
// anything that fits no bed at all.
func bedFamilyFitting(units []bedpack.UnitFootprint, nest BedNester) string {
	byCount := BedFamilyForUnits(len(units))
	if nest == nil || len(units) == 0 {
		return byCount
	}
	// Never DEMOTE: a bed of five is on the H2C because five is what it
	// holds, and a count-driven class that already fits is the right answer.
	start := 0
	for i, family := range bedEscalation {
		if family == byCount {
			start = i
			break
		}
	}
	if _, rejected := nest(bedpack.BedForFamily(byCount), units); len(rejected) == 0 {
		return byCount
	}
	for _, family := range bedEscalation[start+1:] {
		if _, rejected := nest(bedpack.BedForFamily(family), units); len(rejected) == 0 {
			return family
		}
	}
	return largestBedFamily
}

// largestBedFamily is the class with the most room, for a plate that fits
// none of them cleanly. The A2L is widest at 305 mm.
const largestBedFamily = "A2L"
