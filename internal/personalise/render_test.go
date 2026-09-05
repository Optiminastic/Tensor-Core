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
	for _, c := range []struct {
		axis      string
		got, want float64
	}{{"X", x, OutputXMM}, {"Y", y, OutputYMM}, {"Z", z, OutputZMM}} {
		if diff := c.got - c.want; diff > tol || diff < -tol {
			t.Errorf("%s: %s = %.4f mm, want %d mm", label, c.axis, c.got, int(c.want))
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
