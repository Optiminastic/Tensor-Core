package personalise

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// The whole point of this package is that a plank comes out of OpenSCAD at its
// finished size, so nobody has to open Bambu Studio and untick "uniform scale".
// These tests render for real and measure the triangles, because that claim is
// only worth anything if the geometry actually says 200 x 50 x 40.
//
// OpenSCAD's own echo reports the scale it applied, and that is NOT what is
// checked here - a template could report one thing and emit another. The
// assertion is on the loaded mesh.

// openscadBin finds the renderer, or skips. OPENSCAD_BIN overrides; the default
// is where the Windows installer puts it, which is never on PATH.
func openscadBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("OPENSCAD_BIN"); bin != "" {
		return bin
	}
	const win = `C:\Program Files\OpenSCAD\openscad.exe`
	if _, err := os.Stat(win); err == nil {
		return win
	}
	return "openscad"
}

func rendererOrSkip(t *testing.T) *Renderer {
	t.Helper()
	r := NewRenderer(openscadBin(t), "", 0)
	if !r.Available() {
		t.Skip("OpenSCAD is not installed; set OPENSCAD_BIN to run this")
	}
	return r
}

// measure writes the STL to a temp file and reads its bounding box back through
// the same loader the rest of the pipeline uses.
func measure(t *testing.T, stl []byte) (x, y, z float64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.stl")
	if err := os.WriteFile(path, stl, 0o600); err != nil {
		t.Fatalf("write stl: %v", err)
	}
	m, err := orientation.LoadModel(path, ".stl")
	if err != nil {
		t.Fatalf("load stl: %v", err)
	}
	return m.Max.X - m.Min.X, m.Max.Y - m.Min.Y, m.Max.Z - m.Min.Z
}

func assertFinishedSize(t *testing.T, label string, stl []byte) {
	t.Helper()
	x, y, z := measure(t, stl)
	// A tenth of a millimetre. The scale is exact arithmetic, but the STL
	// stores float32 vertices, so demanding bit-equality would be asserting
	// something about IEEE rounding rather than about the model.
	const tol = 0.1

	// HEIGHT IS THE ONE AXIS THAT IS NOT EXACT, and only on the joinery
	// template.
	//
	// The old templates drew the plank at whatever size the letters needed and
	// Tensor scaled the finished bounding box into 200x50x40, so all three
	// axes landed exactly. The joinery file is told the dimensions instead and
	// lays the letters out inside them, scaling by the font's NOMINAL cap
	// height - and the round glyphs do not respect it: S, O, C, G and Q reach
	// 9.8871 against a nominal 9.7243, which is 1.67% over.
	//
	// The model is an INTERSECTION of the two names, so a single round glyph
	// is clipped by whatever letter it meets in the other name and nothing
	// shows. The overshoot survives only when a round glyph meets another
	// round glyph in the same slot - SUBHANJANA x SUBHANTIKA pairs S with S -
	// and then the plank comes out 40.65mm instead of 40mm.
	//
	// Allowed rather than corrected: this file is what the shop opens in
	// OpenSCAD and prints from by hand, so forcing Z to 40 here would make
	// Tensor's output differ from theirs - the exact drift this change exists
	// to remove. Pinned at the measured worst case so a REGRESSION still
	// fails, and the shop can decide separately whether 0.65mm matters.
	const capHeightOvershoot = 0.7

	// ASYMMETRIC, and only upward. A plank SHORTER than its stated height is a
	// fault on any template - the overshoot can only ever add - so the slack
	// goes on the high side alone.
	over := tol
	if label == templateJoinery {
		over = capHeightOvershoot
	}
	for _, c := range []struct {
		axis      string
		got, want float64
		under     float64
		over      float64
	}{
		{"X", x, OutputXMM, tol, tol},
		{"Y", y, OutputYMM, tol, tol},
		{"Z", z, OutputZMM, tol, over},
	} {
		if diff := c.got - c.want; diff > c.over || diff < -c.under {
			t.Errorf("%s: %s = %.4f mm, want %d mm (-%.2f/+%.2f)",
				label, c.axis, c.got, int(c.want), c.under, c.over)
		}
	}
}

// Every template, at the size the customer receives.
func TestRenderProducesFinishedSize(t *testing.T) {
	r := rendererOrSkip(t)

	cases := []struct {
		name   string
		hearts string
		left   string
		right  string
	}{
		{"two hearts", "2 RED HEART", "VASU", "PADMANABH"},
		{"one heart", "1 RED HEART", "PRIYA", "RITESH"},
		{"no hearts", "NO HEART", "AMY", "BOB"},
		// The template's natural geometry grows with the text - a long pair
		// measures about 427mm before scaling - so this is the case where a
		// missing OUT_* would be most obvious.
		{"longest names", "1 RED HEART", "SUBHANJANA", "SUBHANTIKA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if testing.Short() {
				t.Skip("each render takes about 25s")
			}
			p, err := ParamsFromProperties(props(
				"STEP 3-", c.hearts,
				"STEP 4-First Name-", c.left,
				"STEP 5-Second Name", c.right,
			))
			if err != nil {
				t.Fatalf("params: %v", err)
			}
			stl, err := r.RenderSTL(context.Background(), p.Template, p.Args())
			if err != nil {
				t.Fatalf("render %s: %v", p.Template, err)
			}
			assertFinishedSize(t, p.Template, stl)
		})
	}
}

// An unknown template must not fall through to a default one.
func TestUnknownTemplateIsAnError(t *testing.T) {
	r := NewRenderer(openscadBin(t), "", 0)
	_, err := r.RenderSTL(context.Background(), "no_such_template", nil)
	if err == nil {
		t.Fatal("rendering an unknown template must fail")
	}
}
