package personalise

// The colours a template declares for its own pieces.
//
// A design file that draws a lotus in purple, pink and gold already says so:
//
//	BACK_COLOR    = "#A235D8";
//	OUTLINE_COLOR = "#FF7FFF";
//	TEXT_COLOR    = "#FFBF00";
//
// Those values never reach a slicer - OpenSCAD's color() is a preview and CGAL
// discards it on export - so Tensor paints each piece into the 3MF itself, and
// until now it painted them from colours somebody re-typed into the registry.
//
// Re-typing is the problem. The designer picked those three hexes and the
// shop then wrote three more beside them; when the designer changes one, two
// records disagree and the picture the customer approves is the stale one.
// The file is the only place the design's own colours are written down, so it
// is the place to read them from.
//
// NOT USED FOR PRINTING. The colours that reach a print model stay the
// registry's, because those are matched against the filament shelf - see
// dnp_colour.go, where the catalogue's blue (#1560BD) against a shelf holding
// #2850E0 made BambuBuddy refuse plate after plate. A picture has no such
// constraint, and is the only thing this feeds.

import (
	"strings"
)

// colourSuffix is the convention: the piece "back" is coloured by BACK_COLOR.
//
// A convention rather than a configured mapping because the alternative is a
// second thing to fill in that can only ever repeat the first. The files this
// reads already follow it.
const colourSuffix = "_COLOR"

// PartColours is each piece's declared colour, keyed by lower-cased piece name.
//
// Only well-formed "#RRGGBB" values are returned. A variable holding an
// expression, a named colour OpenSCAD understands ("gold"), or a malformed
// hex is left out rather than guessed at: the caller falls back to the
// registry, which is a colour somebody chose, where a guess is not.
func PartColours(source []byte) map[string]string {
	out := map[string]string{}
	for _, p := range DeclaredParams(source) {
		name := strings.ToUpper(strings.TrimSpace(p.Name))
		if !strings.HasSuffix(name, colourSuffix) {
			continue
		}
		part := strings.TrimSuffix(name, colourSuffix)
		if part == "" {
			continue
		}
		hex, ok := hexValue(p.Default)
		if !ok {
			continue
		}
		out[strings.ToLower(part)] = hex
	}
	return out
}

// hexValue reads "#RRGGBB" out of a declaration's value as written.
//
// The value arrives with its quotes still on, because Param.Default is what
// the author typed - which is the point of carrying it verbatim.
func hexValue(written string) (string, bool) {
	v := strings.TrimSpace(written)
	v = strings.Trim(v, `"'`)
	v = strings.TrimSpace(v)
	if len(v) != 7 || v[0] != '#' {
		return "", false
	}
	for i := 1; i < 7; i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'f',
			c >= 'A' && c <= 'F':
		default:
			return "", false
		}
	}
	return "#" + strings.ToUpper(v[1:]), true
}
