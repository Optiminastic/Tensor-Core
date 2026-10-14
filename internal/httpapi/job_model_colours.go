package httpapi

// Reading the filament colours out of a 3MF somebody uploaded.
//
// A design file arrives for a product Tensor does not render, and the upload
// records its geometry: bounding box in, print file attached, job released.
// What it did NOT record was colour - and the planner groups a bed by colour
// alone (production.GroupByColour reads production_jobs.colours and nothing
// else). So an uploaded model came out dimensionally packable and completely
// invisible to batching, sitting in the queue under "No filament colour
// recorded" with no way for the uploader to know that was coming.
//
// EXACT MATCHES ONLY, and this is the whole care in the file. There is a
// nearestColourName in colour_map.go and it must not be used here: its own
// comment says nearest "is right HERE and wrong at send time", because it
// pre-fills a field a human confirms while looking at the spool. This is not
// that field. production_jobs.colours decides which bed a job joins and which
// filament is reserved for it, with nobody looking - a near miss prints a
// customer's order in the wrong colour.
//
// So a hex that is not exactly a colour the shop has recorded comes back
// unmapped, the upload still succeeds, and the job keeps saying it has no
// colour. That is a worse queue entry and a correct one.

import (
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// coloursFromParts resolves a model's declared hexes to the shop's own filament
// names.
//
// Returns the names in a stable order and, separately, every hex it could not
// place. An empty names list with a non-empty unmapped list is the interesting
// case: the file says what it wants and the shop has never recorded that
// colour.
func coloursFromParts(parts []meshio.Part, byHex map[string]string) (names, unmapped []string) {
	seenHex := make(map[string]bool, len(parts))
	seenName := make(map[string]bool, len(parts))

	for _, p := range parts {
		hex, ok := normaliseHex(p.Colour)
		if !ok {
			// A part with no colour at all. Not an error: a single-colour
			// model may carry none, and the caller decides what that means.
			continue
		}
		if seenHex[hex] {
			continue
		}
		seenHex[hex] = true

		name, found := byHex[hex]
		if !found {
			unmapped = append(unmapped, hex)
			continue
		}
		// Folded to the one spelling the planner groups by, so a file drawn in
		// "SKY BLUE" lands on the same bed as a job recorded as "BLUE" - see
		// production.NormalisedColourKey, which does exactly this on the way
		// in and would otherwise key them apart.
		name = production.CanonicalColourName(name)
		if name == "" || seenName[name] {
			continue
		}
		seenName[name] = true
		names = append(names, name)
	}

	// Sorted so two files declaring the same colours in different orders
	// produce the same stored value, and therefore the same grouping key.
	sort.Strings(names)
	sort.Strings(unmapped)
	return names, unmapped
}

// colourNamesByHex is the reverse of the shop's colour map: given a hex, which
// filament is it.
//
// A primary row wins, which is why the rows are walked in the order the query
// returns them (is_primary DESC within a name) and an entry already present is
// not overwritten. The built-in table fills in underneath, so a shop that has
// never confirmed a spool still resolves the colours Tensor knows about.
func colourNamesFromMap(rows []colourMapRow) map[string]string {
	out := make(map[string]string, len(rows)+len(fallbackColours))

	for name, hex := range fallbackColours {
		if h, ok := normaliseHex(hex); ok {
			out[h] = name
		}
	}
	// After the fallbacks, so a confirmed spool overrides the built-in idea of
	// what that hex is called.
	for _, r := range rows {
		h, ok := normaliseHex(r.Hex)
		if !ok {
			continue
		}
		name := strings.TrimSpace(r.ColourName)
		if name == "" {
			continue
		}
		if _, taken := out[h]; taken && !r.IsPrimary {
			continue
		}
		out[h] = name
	}
	return out
}

// colourMapRow is the part of a colour_map row this needs, so the resolver
// stays testable without a database.
type colourMapRow struct {
	ColourName string
	Hex        string
	IsPrimary  bool
}
