// Package duosnap is the Duo Snap: two names joined by a fixed "&", outlined
// in the customer's colour, beside a photo frame.
//
// The shop's own 115951- Duo Snap.scad is embedded unchanged; only name_1,
// name_2 and body_color are set per order. It draws three pieces - "body"
// and "frame" in body_color, "letters" in fixed white - rendered one at a
// time and written as one coloured 3MF, because OpenSCAD's STL carries no
// colour.
//
// THE FONT TRAVELS WITH THE FILE. The names are set in Ondfuturs, which no
// worker has installed, and OpenSCAD does not fail on a missing font: it
// substitutes one and reports success. Measured in the production worker
// image: 5,092 triangles of lettering in the substitute against 26,192 in
// Ondfuturs. So the font file is staged beside every render and loaded with
// `use <Ondfuturs-Demo.ttf>`, which needs nothing installed.
package duosnap

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode"
)

//go:embed assets/duo_snap.scad
var scad []byte

//go:embed assets/Ondfuturs-Demo.ttf
var font []byte

const fontFile = "Ondfuturs-Demo.ttf"

// Source is the shop's file with the font loaded from the staged file.
func Source() []byte {
	return append([]byte("use <"+fontFile+">\n"), scad...)
}

// Files are staged beside the source for every render.
func Files() map[string][]byte {
	return map[string][]byte{fontFile: font}
}

// LetterColour is the file's fixed white for the letters and "&".
const LetterColour = "#FAFAFA"

// Parts are the pieces the file draws, in order; BodyColoured ones take the
// order's colour, the rest LetterColour.
var Parts = []struct {
	Name         string
	BodyColoured bool
}{
	{"body", true},
	{"frame", true},
	{"letters", false},
}

// MaxRunes bounds each name; the file squeezes longer names to fit, and past
// this they stop being readable.
const MaxRunes = 14

// Unsupported lists characters the file has no letter widths for: it is
// fitted from a table of printable ASCII, and anything else is drawn at the
// wrong width.
func Unsupported(name string) []string {
	var bad []string
	seen := map[rune]bool{}
	for _, r := range name {
		if (r < ' ' || r > '~') && !seen[r] {
			seen[r] = true
			bad = append(bad, string(r))
		}
	}
	return bad
}

// Filename is the 3MF's name for an order.
func Filename(order string) string {
	return fmt.Sprintf("%s_duo_snap.3mf", order)
}

// NameCase writes a name the way the design is drawn: each word a capital
// then lower case - "SAVITA" becomes "Savita", "urs harsha" "Urs Harsha".
//
// Ondfuturs is a script face whose capitals are a different shape altogether,
// and the designer's file (order 115951, "Sandeep & Deepika") is set in this
// case. Customers mostly type in capitals, which set the whole name in the
// capital forms and look nothing like the product photographed.
func NameCase(name string) string {
	words := strings.Fields(name)
	for i, w := range words {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
