package production

// Does any machine actually have this bed's colours loaded?
//
// This existed before, was deleted on the reasoning that BambuBuddy's own
// dispatcher owns the filament check, and is back because that reasoning was
// wrong. Running a slicer pipeline with force=false checks printer class and
// nozzle; it does NOT compare the plate's colours against the AMS trays. A blue
// plate was accepted, sliced and printed on a machine loaded with red and white.
//
// So the check belongs here after all, and it must be EXACT. Nearest-colour
// matching was measured against the shop's own palette and is unsafe: Sky Blue
// (#87CEEB) sits closer to the loaded tray #46A8F9 than Blue (#1560BD) does, so
// any threshold loose enough to accept Blue would also accept the wrong spool.
// A bed that waits is recoverable; forty millimetres of lettering in the wrong
// colour is not.

import "strings"

// LoadedFilament is one AMS tray's contents, as machines.filaments records it
// from the fleet sync.
type LoadedFilament struct {
	// Colour is "#RRGGBB". Empty when the tray reported none, which counts as
	// matching nothing rather than as a wildcard.
	Colour string
	Type   string
}

// MissingColours is the colours a plate needs that a machine does not have
// loaded, in the order they were asked for.
//
// Compared on the hex rather than the name because the two vocabularies do not
// agree: the shop calls a colour "BLUE" and a tray reports "#46A8F9". Exact,
// because the alternative is guessing which blue.
//
// A machine with nothing loaded is missing everything, which is the honest
// answer: a printer whose AMS the sync has never populated is not proven to hold
// anything at all.
func MissingColours(need []string, loaded []LoadedFilament) []string {
	have := make(map[string]bool, len(loaded))
	for _, f := range loaded {
		if hex := normaliseColourHex(f.Colour); hex != "" {
			have[hex] = true
		}
	}

	var missing []string
	seen := map[string]bool{}
	for _, want := range need {
		hex := normaliseColourHex(want)
		// A colour that cannot be read as a hex cannot be confirmed loaded, so
		// it counts as missing rather than being skipped.
		if hex == "" {
			hex = strings.ToUpper(strings.TrimSpace(want))
		}
		if hex == "" || seen[hex] || have[hex] {
			continue
		}
		seen[hex] = true
		missing = append(missing, hex)
	}
	return missing
}

// HasColours reports whether every colour the plate needs is loaded.
func HasColours(need []string, loaded []LoadedFilament) bool {
	return len(MissingColours(need, loaded)) == 0
}

// normaliseColourHex accepts the forms both sides produce - "#RRGGBB",
// "RRGGBB", and the 8-digit RGBA a Bambu tray reports - and returns "#RRGGBB".
// Anything else returns "", which the caller treats as unmatchable.
func normaliseColourHex(raw string) string {
	h := strings.TrimSpace(raw)
	h = strings.TrimPrefix(h, "#")
	// Alpha is dropped: a tray reports it, a plate has no use for it, and
	// keeping it would make the same colour compare unequal across the two.
	if len(h) == 8 {
		h = h[:6]
	}
	if len(h) != 6 {
		return ""
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return ""
		}
	}
	return "#" + strings.ToUpper(h)
}
