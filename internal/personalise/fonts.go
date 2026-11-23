package personalise

// The font families a template names, and where an uploaded face comes in.
//
// OpenSCAD does not fail on a missing font. fontconfig substitutes another
// face and the render reports success, so a template that names a family the
// worker does not have prints in the wrong lettering - and the outline grown
// around the letters comes out the wrong shape with it. The Duo Snap did
// exactly that (JOB-116894-23): its file names "Ondfuturs", the worker had no
// such face, and the job was batched in a substitute.
//
// Two things follow from this file:
//
//   - A NEW template upload is checked: every family it names must be either
//     built in (BuiltinFontFamily) or uploaded (the registry's font list).
//     Templates already stored are never re-checked - they keep rendering
//     exactly as they do.
//   - A render stages the uploaded faces its template names, and only those.
//     A template whose families are all built in gets nothing new staged, so
//     its render is byte-for-byte what it was before uploads existed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// builtinFontFamilies are the families every worker can render, lower-cased.
//
// The first group is the faces committed in fonts/ (installed into the image)
// and scadassets/fonts/ (staged beside every render); fonts_test.go reads
// those files and fails if this list and the files disagree. The second group
// is what OpenSCAD and the image's fontconfig provide themselves, which the
// embedded plank templates already name ("Liberation Sans").
var builtinFontFamilies = map[string]bool{
	// fonts/
	"arial": true, "bahnschrift": true, "segoe ui": true, "segoe ui black": true, "century gothic": true,
	// scadassets/fonts/
	"lobster": true, "ondfuturs": true,
	// OpenSCAD's own and the image's fontconfig defaults
	"liberation sans": true, "liberation serif": true, "liberation mono": true,
	"dejavu sans": true, "dejavu sans mono": true, "dejavu serif": true,
}

// BuiltinFontFamily reports whether every worker already has this family.
func BuiltinFontFamily(family string) bool {
	return builtinFontFamilies[normaliseFamily(family)]
}

// BuiltinFontFamilies lists the built-in families, sorted, for showing people
// which faces need no upload.
func BuiltinFontFamilies() []string {
	out := make([]string, 0, len(builtinFontFamilies))
	for f := range builtinFontFamilies {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func normaliseFamily(family string) string {
	return strings.ToLower(strings.Join(strings.Fields(family), " "))
}

var (
	// font = "Family:style=Bold" or font = some_variable
	fontArgPattern = regexp.MustCompile(`\bfont\s*=\s*("(?:[^"\\]|\\.)*"|[A-Za-z_$][A-Za-z0-9_$]*)`)
)

// TemplateFontFamilies is every font family a template names, in first-seen
// order and as the file spells it.
//
// Read from `font = "..."` arguments, and through one variable:
// `name_font = "Ondfuturs"` used as `font = name_font`, which is how the
// shop's files are written. The style after ":" is dropped - fontconfig picks
// a style within a family, but it cannot conjure a family it does not have.
// A font chosen by an expression (str(...), a ternary) cannot be read without
// running the file and is not reported; that is the one case this misses.
func TemplateFontFamilies(source []byte) []string {
	code := string(stripSCADComments(source))
	var out []string
	seen := map[string]bool{}
	add := func(literal string) {
		family := strings.TrimSpace(strings.SplitN(unquoteSCAD(literal), ":", 2)[0])
		if family == "" || seen[normaliseFamily(family)] {
			return
		}
		seen[normaliseFamily(family)] = true
		out = append(out, family)
	}
	for _, m := range fontArgPattern.FindAllStringSubmatch(code, -1) {
		value := m[1]
		if strings.HasPrefix(value, `"`) {
			add(value)
			continue
		}
		assign := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$=!<>])` + regexp.QuoteMeta(value) +
			`\s*=\s*("(?:[^"\\]|\\.)*")`)
		for _, a := range assign.FindAllStringSubmatch(code, -1) {
			add(a[1])
		}
	}
	return out
}

// unquoteSCAD turns an OpenSCAD string literal into its text.
func unquoteSCAD(literal string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(literal, `"`), `"`)
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// FontFace is what Tensor reads out of an uploaded font file.
type FontFace struct {
	// Families are the names a template can use for this face: the font's
	// family name and, when it declares one, its typographic family ("Segoe
	// UI" for Segoe UI Black). Never empty.
	Families []string
	// Style is the face's subfamily - "Regular", "Bold" - for showing people.
	Style string
}

// ReadFontFace parses a TrueType or OpenType file and returns its names.
//
// Refuses anything that is not a single sfnt face: a WOFF/WOFF2 web font, a
// .ttc collection, or a file that is not a font at all.
func ReadFontFace(data []byte) (FontFace, error) {
	f, err := sfnt.Parse(data)
	if err != nil {
		return FontFace{}, fmt.Errorf("not a TrueType or OpenType font file")
	}
	var buf sfnt.Buffer
	name := func(id sfnt.NameID) string {
		s, err := f.Name(&buf, id)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	face := FontFace{Style: name(sfnt.NameIDTypographicSubfamily)}
	if face.Style == "" {
		face.Style = name(sfnt.NameIDSubfamily)
	}
	seen := map[string]bool{}
	for _, id := range []sfnt.NameID{sfnt.NameIDFamily, sfnt.NameIDTypographicFamily} {
		if fam := name(id); fam != "" && !seen[normaliseFamily(fam)] {
			seen[normaliseFamily(fam)] = true
			face.Families = append(face.Families, fam)
		}
	}
	if len(face.Families) == 0 {
		return FontFace{}, fmt.Errorf("the font file does not name its family")
	}
	return face, nil
}

// FontLoader returns the uploaded faces for the given families, as file name
// to bytes. Families nobody uploaded are simply absent from the result.
type FontLoader func(ctx context.Context, families []string) (map[string][]byte, error)

// WithFontLoader returns a Renderer that stages uploaded faces.
//
// A copy, for the same reason as WithTemplateLoader: the Renderer is shared.
func (r *Renderer) WithFontLoader(load FontLoader) *Renderer {
	clone := *r
	clone.loadFonts = load
	return &clone
}

// uploadedFamiliesFor is the families a template names that are not built in:
// the only ones an upload could supply.
func uploadedFamiliesFor(source []byte) []string {
	var out []string
	for _, f := range TemplateFontFamilies(source) {
		if !BuiltinFontFamily(f) {
			out = append(out, f)
		}
	}
	return out
}

// stageUploadedFonts writes the uploaded faces a template names into the
// render's fonts/ directory, beside whatever the asset directory staged.
//
// A family nobody uploaded is not an error here: the render goes ahead the way
// it always has. Refusing a template for a missing face is the upload's job,
// and only for new uploads - a template already in use keeps rendering as it
// does today.
func (r *Renderer) stageUploadedFonts(ctx context.Context, dir string, source []byte) error {
	if r.loadFonts == nil {
		return nil
	}
	families := uploadedFamiliesFor(source)
	if len(families) == 0 {
		return nil
	}
	faces, err := r.loadFonts(ctx, families)
	if err != nil {
		// A face exists and could not be fetched. Rendering on would print in
		// a substitute while reporting success, which is the failure this
		// whole file exists to prevent.
		return fmt.Errorf("load uploaded fonts %v: %w", families, err)
	}
	if len(faces) == 0 {
		return nil
	}
	fontDir := filepath.Join(dir, fontDirName)
	if err := os.MkdirAll(fontDir, 0o700); err != nil {
		return fmt.Errorf("stage fonts: %w", err)
	}
	for name, data := range faces {
		if filepath.Base(name) != name || name == "." || name == "" {
			return fmt.Errorf("uploaded font name %q must be a bare file name", name)
		}
		if err := os.WriteFile(filepath.Join(fontDir, name), data, 0o600); err != nil {
			return fmt.Errorf("stage font %s: %w", name, err)
		}
	}
	return nil
}
