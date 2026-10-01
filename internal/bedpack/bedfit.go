package bedpack

import "strings"

// Whether a plate laid out for one machine class can print on another.
//
// The class rule was written as an equality and enforced as one: a plate says
// which class it was packed for, and only that class may take it. That is right
// in one direction and wrong in the other. A plate is packed at fixed offsets
// and sliced with AutoOrient and AutoArrange both false, so it cannot be
// rearranged onto a smaller bed - four planks packed on an A2L come out 270x270
// and a P2S is 256x256, which is the failure migration 0083 exists for. But a
// plate packed for a P2S sits inside an H2C's bed with room to spare, and
// refusing it there is not physics, it is an equality test standing in for one.
//
// The cost of that stand-in is measurable: the shop's three H2Cs take a bed
// only when one is laid out for them, and a bed is laid out for an H2C only at
// five units, which the planner's cap makes impossible. Those machines have
// therefore never printed anything Tensor planned.
//
// So the geometry answers the question, and the CALLER decides what to do with
// "bigger": the shop's rule is that the big beds earn their keep, so a larger
// class is a fallback rather than a peer. This package only says which it is.

// FamilyFit is how a machine class stands to a plate laid out for another.
type FamilyFit int

const (
	// FitNone is "this plate cannot print there". The zero value deliberately:
	// a class this package does not recognise fails closed, rather than
	// resolving to something plausible and printing on it.
	FitNone FamilyFit = iota
	// FitExact is the class the plate was laid out for.
	FitExact
	// FitOversized is a larger bed. The plate prints, with bed left over.
	FitOversized
)

// FitsWithin reports whether a plate laid out on b can print on other.
//
// Every axis, including Z. X and Y are the constraint the fixed offsets create;
// Z is checked per part at packing time against the source bed, so a class with
// a wide bed and a short gantry would otherwise be called a safe fallback for a
// plate whose parts only fit under a taller one. No such class exists today,
// which is exactly when the check is cheap to add.
func (b Bed) FitsWithin(other Bed) bool {
	b, other = b.Normalised(), other.Normalised()
	// SIZE only, deliberately - not where the plate currently starts.
	//
	// Origin matters enormously to whether a plate prints: a P2S plate is laid
	// out from X=0 and an H2C's second nozzle cannot reach before X=25, which
	// is how 115450-PURPLE sliced and then failed with "Found G-code in
	// unprintable area of multi-extruder printers". Checking it HERE was the
	// wrong place, though, and briefly made things worse than the bug: this
	// answer gates which printers the queue dialog will even offer, so
	// refusing on origin greyed out every H2C for every small bed and left no
	// machine to select - while the fix for the origin, re-plating for the
	// chosen machine, lives in the send path that selection leads to.
	//
	// So the question here is only "are these units able to live on that bed",
	// and sendBatchToMachine re-lays the plate for whatever machine wins.
	return b.XMM <= other.XMM && b.YMM <= other.YMM && b.ZMM <= other.ZMM
}

// FitForFamily answers whether a plate packed for one class can print on a
// machine of another, and whether that machine is the plate's own class.
//
// Both names are matched the way BedForFamily matches them - upper-cased and
// trimmed - so a profile recorded as "p2s" is the same class as "P2S".
func FitForFamily(packedFor, target string) FamilyFit {
	plate, ok := bedForKnownFamily(packedFor)
	if !ok {
		return FitNone
	}
	machine, ok := bedForKnownFamily(target)
	if !ok {
		return FitNone
	}
	if normaliseFamily(packedFor) == normaliseFamily(target) {
		return FitExact
	}
	if plate.FitsWithin(machine) {
		return FitOversized
	}
	return FitNone
}

// bedForKnownFamily is BedForFamily without the fallback: ok is false for a
// name this package does not recognise.
//
// The fallback is right for PACKING - an unknown class gets the smallest bed,
// so the plate fits everything - and wrong for FITTING, where it would quietly
// make every unrecognised name a synonym for P2S and let a plate print on a
// machine nobody identified.
func bedForKnownFamily(family string) (Bed, bool) {
	switch normaliseFamily(family) {
	case "H2C":
		return BedH2C.Normalised(), true
	case "A2L":
		return BedA2L.Normalised(), true
	case "P2S":
		return BedP2S.Normalised(), true
	default:
		return Bed{}, false
	}
}

func normaliseFamily(family string) string {
	return strings.ToUpper(strings.TrimSpace(family))
}

// BedForKnownFamily is bedForKnownFamily for callers outside this package.
//
// Exported because "is this a class we have real geometry for?" is a question
// the HTTP layer has to ask before laying a plate out for a chosen machine: an
// unrecognised family must fall back to the unit-count rule rather than quietly
// resolve to some default bed.
func BedForKnownFamily(family string) (Bed, bool) { return bedForKnownFamily(family) }
