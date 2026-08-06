package personalise

import (
	"strings"
	"testing"
)

func TestBuildScadShape(t *testing.T) {
	got := buildScad(Spec{Text: "ignored via -D", SizeMM: 12, DepthMM: 2, Font: "DejaVu Sans"})

	for _, want := range []string{
		`name = "PREVIEW";`,
		"linear_extrude(height = 2.0000)",
		"size = 12.0000",
		`font = "DejaVu Sans"`,
		`halign = "center"`,
		`valign = "center"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildScad output missing %q\n---\n%s", want, got)
		}
	}
	// The untrusted text must never be interpolated into the script source.
	if strings.Contains(got, "ignored via -D") {
		t.Errorf("buildScad leaked the text into the script:\n%s", got)
	}
}

func TestBuildScadDefaultsAndSafeFont(t *testing.T) {
	// Zero size/depth fall back to sane defaults; an unsafe font is rejected.
	got := buildScad(Spec{Text: "x", SizeMM: 0, DepthMM: 0, Font: `evil"); cube(99); //`})
	if !strings.Contains(got, "size = 10.0000") || !strings.Contains(got, "linear_extrude(height = 1.0000)") {
		t.Errorf("defaults not applied:\n%s", got)
	}
	if !strings.Contains(got, `font = "DejaVu Sans"`) {
		t.Errorf("unsafe font not rejected:\n%s", got)
	}
	if strings.Contains(got, "cube(99)") {
		t.Errorf("font injection leaked into script:\n%s", got)
	}
}

func TestEscapeForD(t *testing.T) {
	cases := map[string]string{
		`Arkit`:        `Arkit`,
		`a"b`:          `a\"b`,
		`a\b`:          `a\\b`,
		"line1\nline2": "line1 line2",
	}
	for in, want := range cases {
		if got := escapeForD(in); got != want {
			t.Errorf("escapeForD(%q) = %q, want %q", in, got, want)
		}
	}
}
