package httpapi

// Re-sizing the ams_mapping to the plate the slicer actually produced.
//
// Tensor binds spools BEFORE slicing, because the colours it binds are what it
// asks the slicer to use. The mapping it builds there has one entry per slot of
// TENSOR'S plate. The file that comes back is Bambu Studio's, and Bambu Studio
// drops filaments no object prints in - so a bed bound across four slots can
// come back declaring two.
//
// Sending the four-entry mapping anyway is not a rounding error. The printer
// halts at the first tool change with "[0700-8012] Failed to get AMS mapping
// table", reports the queue item as "printing" throughout, and refuses to
// cancel it because it believes it is printing. Observed on H2 and H3; the same
// plate with a correctly sized mapping printed.
//
// Pure: it takes the two arrays the slice job already carries and returns the
// one to send, so it is testable without a printer.

import (
	"fmt"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

// mappingForSlicedPlate is the ams_mapping for the file BambuBuddy produced.
//
// Matched by COLOUR rather than by position, because the slicer both drops
// filaments and renumbers what is left: the plate's filament 2 may be the bed's
// slot 4. Colour is what survives that, and it is the same value on both sides
// - trayHexes holds what the printer reports for the chosen tray, which is
// exactly what was sent as filament_colours and therefore what the slicer wrote
// into the file.
//
// mapping and trayHexes are parallel: mapping[i] is the tray serving the bed's
// slot i, trayHexes[i] the colour physically in it.
func mappingForSlicedPlate(
	sliced []bambubuddy.SlicedFilament, mapping []int, trayHexes []string,
) ([]int, error) {
	if len(mapping) != len(trayHexes) {
		return nil, fmt.Errorf(
			"this bed was sliced with %d spools but %d colours; send it again",
			len(mapping), len(trayHexes))
	}
	if len(sliced) == 0 {
		return nil, fmt.Errorf("the sliced plate declares no filament; send this bed again")
	}

	used := make([]bool, len(mapping))
	out := make([]int, 0, len(sliced))
	for i, f := range sliced {
		hex, ok := normaliseHex(f.Colour)
		if !ok {
			return nil, fmt.Errorf(
				"the sliced plate's filament %d declares no readable colour; send this bed again", i+1)
		}
		found := -1
		for j, tray := range trayHexes {
			if used[j] {
				continue
			}
			if h, ok := normaliseHex(tray); ok && h == hex {
				found = j
				break
			}
		}
		if found < 0 {
			// Same length and no match means the colours were never going to
			// line up by name - a slicer that rewrote a hex, say. The planned
			// mapping is then the best answer available and is what every
			// single-colour bed has always used, so it goes as planned rather
			// than failing a bed that would have printed.
			if len(sliced) == len(mapping) {
				return mapping, nil
			}
			return nil, fmt.Errorf(
				"the sliced plate needs %s, which no spool this bed was sliced for holds; send it again", hex)
		}
		used[found] = true
		out = append(out, mapping[found])
	}
	return out, nil
}
