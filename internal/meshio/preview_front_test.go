package meshio

// Which side of a product the customer is shown first.
//
// faceAxes finds the FACE but not its side: it returns the positive unit
// vector of the thinnest axis, so a piece whose detail faces -Y was
// photographed from +Y and the customer was shown the back of their diya
// holder under a countdown saying this is exactly how it will be made.
//
// Planks never showed it because planks are modelled with their relief in +Z
// by convention - and a convention is precisely what a product configured in
// the registry cannot be relied on to follow.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// plate builds a thin plate in XY with `detail` extra triangles standing off
// whichever face the sign names. The flat back is always two triangles, which
// is what a real back plate is.
func plate(detailSign float64, detail int) []shadedTriangle {
	out := []shadedTriangle{
		{tri: orientation.Triangle{
			V0: orientation.Vec3{}, V1: orientation.Vec3{X: 10}, V2: orientation.Vec3{X: 10, Y: 10},
			Normal: orientation.Vec3{Z: -1}, Area: 50}},
		{tri: orientation.Triangle{
			V0: orientation.Vec3{}, V1: orientation.Vec3{X: 10, Y: 10}, V2: orientation.Vec3{Y: 10},
			Normal: orientation.Vec3{Z: -1}, Area: 50}},
	}
	for i := 0; i < detail; i++ {
		out = append(out, shadedTriangle{tri: orientation.Triangle{
			V0: orientation.Vec3{Z: detailSign}, V1: orientation.Vec3{X: 1, Z: detailSign},
			V2:     orientation.Vec3{X: 1, Y: 1, Z: detailSign},
			Normal: orientation.Vec3{Z: detailSign}, Area: 0.5}})
	}
	return out
}

var (
	plateLo = orientation.Vec3{}
	plateHi = orientation.Vec3{X: 10, Y: 10, Z: 2}
)

func TestTheBusierFaceIsTheFront(t *testing.T) {
	// Detail in +Z, which is how every plank is modelled. The answer must be
	// the direction those models have always been drawn from.
	if got := frontSign(plate(1, 50), plateLo, plateHi); got != 1 {
		t.Errorf("frontSign = %v, want +1 for a plate with its relief in +Z", got)
	}
}

func TestAProductModelledTheOtherWayIsTurnedRound(t *testing.T) {
	// The same plate with its detail on the -Z face. Nothing about the
	// bounding box changes, which is the whole reason faceAxes could not tell
	// these apart.
	if got := frontSign(plate(-1, 50), plateLo, plateHi); got != -1 {
		t.Errorf("frontSign = %v, want -1; the customer is being shown the back", got)
	}
}

func TestAShapeWithNothingToGoOnKeepsTheOldDirection(t *testing.T) {
	// Equal geometry both ways. Guessing would be worse than leaving every
	// such model drawn exactly as it was before this existed.
	// Built directly rather than from two plates: plate() always carries the
	// two-triangle back, so stacking them is not a tie at all.
	var both []shadedTriangle
	for i := 0; i < 20; i++ {
		both = append(both,
			shadedTriangle{tri: orientation.Triangle{Normal: orientation.Vec3{Z: 1}, Area: 1}},
			shadedTriangle{tri: orientation.Triangle{Normal: orientation.Vec3{Z: -1}, Area: 1}})
	}
	if got := frontSign(both, plateLo, plateHi); got != 1 {
		t.Errorf("frontSign = %v, want +1 on a tie", got)
	}
}

func TestTheRimOfAPlateDoesNotVote(t *testing.T) {
	// Edge triangles face neither way and would otherwise decide it on
	// whichever side rounding sent them.
	tris := plate(1, 3)
	for i := 0; i < 40; i++ {
		tris = append(tris, shadedTriangle{tri: orientation.Triangle{
			Normal: orientation.Vec3{X: 1}, Area: 1}})
	}
	if got := frontSign(tris, plateLo, plateHi); got != 1 {
		t.Errorf("frontSign = %v, want +1 - the rim outvoted the face", got)
	}
}

func TestTheFrontViewLooksSquareOnAndTheOthersDoNot(t *testing.T) {
	// The plank's two views keep the angle they have always had; the front is
	// the new one, and it is the one that must be flat on.
	front := cameraFor(plateLo, plateHi, 900, 700, ViewFront, 1)
	left := cameraFor(plateLo, plateHi, 900, 700, ViewFromLeft, 1)
	right := cameraFor(plateLo, plateHi, 900, 700, ViewFromRight, 1)

	if front.forward.X != 0 {
		t.Errorf("the front view is yawed (forward.X = %v); it should look straight at the face",
			front.forward.X)
	}
	if left.forward.X == right.forward.X {
		t.Error("the left and right views are identical; there is no other side to show")
	}
	if left.forward.X == front.forward.X {
		t.Error("the left view is the front view; the button would do nothing")
	}
}

func TestTurningAModelRoundMovesTheCamera(t *testing.T) {
	// The sign has to reach the camera, not just be computed.
	ahead := cameraFor(plateLo, plateHi, 900, 700, ViewFront, 1)
	behind := cameraFor(plateLo, plateHi, 900, 700, ViewFront, -1)
	if ahead.forward == behind.forward {
		t.Error("facing made no difference to where the camera stands")
	}
}
