// Package personalise turns a customer's name into real, printable 3D geometry
// using OpenSCAD's headless CLI. It renders ONLY the extruded text (a clean,
// manifold mesh OpenSCAD is reliable at); the caller positions it and merges it
// with the base model via internal/meshio, and the slicer fuses the overlapping
// bodies at slice time. We deliberately avoid OpenSCAD's import()+boolean path:
// CGAL booleans on arbitrary non-manifold decorative STLs fail or hang.
package personalise

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultFont / DefaultStyle are guaranteed to exist in the runtime. Liberation
// ships with OpenSCAD on every platform and the API image also installs DejaVu and
// FreeFont, so every family in AllowedFonts renders both in dev and in production.
const (
	DefaultFont  = "Liberation Sans"
	DefaultStyle = "Regular"
)

// AllowedFonts is the font families the editor may pick. Kept to families that are
// installed in the API image (fonts-liberation, fonts-dejavu, fonts-freefont-ttf)
// AND bundled with OpenSCAD, so the choice is honoured everywhere. Membership is an
// allowlist, so the family can be interpolated into the script safely.
var AllowedFonts = []string{
	"Liberation Sans",
	"Liberation Serif",
	"Liberation Mono",
	"DejaVu Sans",
	"DejaVu Serif",
	"FreeSans",
	"FreeSerif",
}

// AllowedStyles is the weight/slant options. Values are OpenSCAD ":style=" names.
var AllowedStyles = []string{"Regular", "Bold", "Italic", "Bold Italic"}

// Spec is one personalisation label to render. Text is the customer's free input;
// it is passed to OpenSCAD via -D (never interpolated into the script) so it can
// never break out into OpenSCAD code. Font and Style are allowlisted.
type Spec struct {
	Text    string
	Font    string
	Style   string
	SizeMM  float64
	DepthMM float64
}

// normalised returns the spec with sane, bounded values and an allowlisted font.
func (s Spec) normalised() Spec {
	out := s
	out.Text = strings.TrimSpace(s.Text)
	out.Font = safeFont(s.Font)
	out.Style = safeStyle(s.Style)
	if out.SizeMM <= 0 {
		out.SizeMM = 10
	}
	if out.DepthMM <= 0 {
		out.DepthMM = 1
	}
	return out
}

// safeFont returns the family only if it is in AllowedFonts, else the default.
func safeFont(font string) string {
	if f := strings.TrimSpace(font); slices.Contains(AllowedFonts, f) {
		return f
	}
	return DefaultFont
}

// safeStyle returns the style only if it is in AllowedStyles, else the default.
func safeStyle(style string) string {
	if s := strings.TrimSpace(style); slices.Contains(AllowedStyles, s) {
		return s
	}
	return DefaultStyle
}

// openscadFont builds the OpenSCAD font string from the allowlisted family and
// style, e.g. "Liberation Sans:style=Bold". A Regular style needs no suffix.
func (s Spec) openscadFont() string {
	if s.Style == "" || s.Style == DefaultStyle {
		return s.Font
	}
	return s.Font + ":style=" + s.Style
}

// buildScad returns the OpenSCAD program. Size/depth/font are baked in (numbers and
// an allowlisted font); `name` is a placeholder overridden at run time by -D, so
// the untrusted text never appears in the script source. Pure.
func buildScad(s Spec) string {
	s = s.normalised()
	return fmt.Sprintf(
		"name = \"PREVIEW\";\n"+
			"linear_extrude(height = %.4f)\n"+
			"  text(name, size = %.4f, font = \"%s\", halign = \"center\", valign = \"center\");\n",
		s.DepthMM, s.SizeMM, s.openscadFont(),
	)
}

// escapeForD escapes the text for an OpenSCAD -D 'name="..."' assignment: the
// value is wrapped in double quotes, so backslashes and double quotes must be
// escaped. Newlines are dropped (a label is a single line).
func escapeForD(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", "")
	return r.Replace(text)
}

// GenerateTextSTL renders the spec's text to a binary STL with OpenSCAD, returning
// the STL bytes. bin is the OpenSCAD executable (config OPENSCAD_BIN). It works in
// a temp dir that is removed on return. A non-nil error means OpenSCAD was missing,
// timed out (via ctx), or failed - the caller should fall back to the plain model.
func GenerateTextSTL(ctx context.Context, bin string, spec Spec) ([]byte, error) {
	spec = spec.normalised()
	if spec.Text == "" {
		return nil, fmt.Errorf("personalise: empty text")
	}
	if bin == "" {
		bin = "openscad"
	}

	dir, err := os.MkdirTemp("", "personalise-*")
	if err != nil {
		return nil, fmt.Errorf("personalise: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	scadPath := filepath.Join(dir, "label.scad")
	outPath := filepath.Join(dir, "label.stl")
	if err := os.WriteFile(scadPath, []byte(buildScad(spec)), 0o600); err != nil {
		return nil, fmt.Errorf("personalise: write scad: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin,
		"-o", outPath,
		"-D", fmt.Sprintf("name=\"%s\"", escapeForD(spec.Text)),
		scadPath,
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("personalise: openscad failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	stl, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("personalise: read output stl: %w", err)
	}
	if len(stl) == 0 {
		return nil, fmt.Errorf("personalise: openscad produced an empty stl")
	}
	return stl, nil
}
