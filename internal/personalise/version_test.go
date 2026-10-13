package personalise

import (
	"context"
	"strings"
	"testing"
)

func TestParseMajorVersion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		banner string
		want   int
		fails  bool
	}{
		{
			name:   "the release on the Windows box",
			banner: "OpenSCAD version 2021.01\n",
			want:   2021,
		},
		{
			name:   "the dated snapshot the production image pins",
			banner: "OpenSCAD version 2026.09.12\n",
			want:   2026,
		},
		{
			// --version goes to stderr on some builds, and a build can print a
			// line of its own first.
			banner: "Qt: Session management error\nOpenSCAD version 2023.12.31\n",
			name:   "a banner with noise before it",
			want:   2023,
		},
		{name: "nothing at all", banner: "", fails: true},
		{name: "not OpenSCAD", banner: "bash: openscad: command not found\n", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMajorVersion(tc.banner)
			if tc.fails {
				if err == nil {
					t.Fatalf("parseMajorVersion(%q) = %d, want an error", tc.banner, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMajorVersion(%q): %v", tc.banner, err)
			}
			if got != tc.want {
				t.Errorf("parseMajorVersion(%q) = %d, want %d", tc.banner, got, tc.want)
			}
		})
	}
}

func TestNeedsTextmetrics(t *testing.T) {
	// The real call, as the keychain template makes it.
	keychain := []byte(`TM_REF = textmetrics(REF_NAME, size = 1, font = FONT);`)
	if !needsTextmetrics(keychain) {
		t.Error("a template calling textmetrics() was not recognised as needing it")
	}
	// The plank, which measures nothing - it carries a hardcoded table of
	// glyph widths precisely BECAUSE 2021.01 has no textmetrics.
	plank := []byte(`W_TBL = [[0.5,0.68],[0.51,0.71]];` + "\n" + `NAME_L = "MONI";`)
	if needsTextmetrics(plank) {
		t.Error("the plank template was wrongly flagged as needing textmetrics")
	}
	// The word in prose must not count as a call. The keychain's own header
	// says "textmetrics : measures the name so any name is sized", and a
	// template whose only mention is a comment still renders fine.
	prose := []byte(`// textmetrics measures the name. Not used here.`)
	if needsTextmetrics(prose) {
		t.Error("a comment mentioning textmetrics was treated as a call")
	}
}

func TestCheckRendererVersionRefusesAnOldBinaryForATextmetricsTemplate(t *testing.T) {
	// A binary that cannot be run at all, which is also how an unreadable
	// version behaves: for a template that needs textmetrics this must FAIL,
	// not assume the best.
	r := NewRenderer("definitely-not-openscad-anywhere", "", 0)
	ctx := context.Background()

	err := r.checkRendererVersion(ctx, "sc_keychain", []byte(`x = textmetrics("A", size=1);`))
	if err == nil {
		t.Fatal("a textmetrics template was allowed on a renderer of unknown version")
	}
	if !strings.Contains(err.Error(), "sc_keychain") {
		t.Errorf("the refusal does not name the template: %v", err)
	}

	// And a template that does not need it is unaffected, so this guard cannot
	// stop a plank rendering.
	if err := r.checkRendererVersion(ctx, "dnp_with_no_heart", []byte(`NAME_L = "MONI";`)); err != nil {
		t.Errorf("a plank was refused by the textmetrics guard: %v", err)
	}
}

// TestTheLocalRendererAgreesWithTheTemplate runs the real binary, when there is
// one, and asserts the guard's verdict matches what the keychain template would
// decide for itself. They test the same thing two ways - Go reads --version,
// the template calls version() - and the whole bug was them disagreeing.
func TestTheLocalRendererAgreesWithTheTemplate(t *testing.T) {
	r := NewRenderer(openscadBin(t), "", 0)
	if !r.Available() {
		t.Skip("OpenSCAD is not installed; set OPENSCAD_BIN to run this")
	}
	major, err := r.MajorVersion(context.Background())
	if err != nil {
		t.Fatalf("read the local OpenSCAD version: %v", err)
	}
	t.Logf("the local OpenSCAD is %d", major)

	err = r.checkRendererVersion(
		context.Background(), "sc_keychain", []byte(`x = textmetrics("A", size=1);`))
	switch {
	case major < firstTextmetricsVersion && err == nil:
		t.Errorf("OpenSCAD %d has no textmetrics but the guard allowed the render", major)
	case major >= firstTextmetricsVersion && err != nil:
		t.Errorf("OpenSCAD %d has textmetrics but the guard refused: %v", major, err)
	}
}
