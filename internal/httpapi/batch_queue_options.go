package httpapi

// Which machines can take this bed, and what the ones that cannot are missing.
//
// The shop's rule, stated plainly: a printer may run a bed only if it already
// holds every colour that bed needs. Not "close enough", not "load it later" -
// a bed whose colours nobody has loaded WAITS. Printing a plank in whatever was
// already in the machine is scrap, and scrap is worse than a bed sitting still.
//
// So this does not choose anything. It reports what is true - the bed's colours,
// each machine's loaded colours, and the difference - and a person chooses. That
// is deliberately the opposite of the automatic picker this replaces, which
// scored the fleet and sent the plate wherever it judged best; the shop wanted
// the decision back.
//
// Read from Tensor's own mirror of the fleet rather than from BambuBuddy: the
// sync writes every printer's AMS trays into machines.filaments (see
// filamentsJSON in bambubuddy_sync.go), so thirteen printers cost one query
// instead of thirteen calls. Up to a sync interval stale, which is the right
// trade for a list somebody reads before choosing.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// queueOptionsResponse is what the Queue dialog draws.
type queueOptionsResponse struct {
	BatchNumber string `json:"batch_number"`
	Status      string `json:"status"`
	// Colours is what the bed needs loaded, name and swatch together: an
	// operator thinks in "blue", the printer answers in "#1560BD", and the
	// dialog has to speak both.
	Colours  []queueColour  `json:"colours"`
	Machines []queueMachine `json:"machines"`
	// ColoursVerified is false when Tensor cannot compare the bed's colours
	// against what the printers hold - see colourMatchingPossible. Every machine
	// is then offered, and the dialog says why the list is unfiltered rather
	// than presenting a guess as a rule.
	ColoursVerified bool `json:"colours_verified"`
	// Note says why nothing is offered when nothing is, rather than leaving a
	// blank dropdown to be interpreted.
	Note string `json:"note"`
}

type queueColour struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

// queueMachine is one printer and whether it can take this bed.
//
// Ineligible machines are listed too, with what they are missing. A printer an
// operator can see standing idle, absent from the list with no explanation, is
// the kind of thing that gets worked around rather than fixed.
type queueMachine struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Status string `json:"status"`
	// Loaded is every colour in this printer's AMS right now, as hexes - the
	// dialog draws them as swatches beside the name.
	Loaded []string `json:"loaded"`
	// Missing names the bed's colours this printer does not hold, by name
	// rather than by hex: "does not hold BLUE" is something an operator can act
	// on, "#1560BD" is something they have to look up.
	Missing  []string `json:"missing"`
	Eligible bool     `json:"eligible"`
	// Suggested marks the one printer the dialog should offer first - the
	// closest colour match, idle before busy. A suggestion, never a decision:
	// the operator can pick any machine in the list.
	Suggested bool `json:"suggested"`
	// Reason explains an ineligible machine in one line.
	Reason string `json:"reason,omitempty"`
}

// batchQueueOptions lists the machines that could print this bed.
func (s *Server) batchQueueOptions(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(jobs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "This batch holds no jobs.")
		return
	}

	out := queueOptionsResponse{
		BatchNumber: batch.BatchNumber,
		Status:      batch.Status,
		Colours:     []queueColour{},
		Machines:    []queueMachine{},
	}

	out.Colours = s.queueColoursFor(ctx, jobs)

	rows, err := s.store.Q.ListFleetMachines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the fleet.")
		return
	}

	out.ColoursVerified = s.colourMatchingPossible(ctx)

	eligible := 0
	for _, m := range rows {
		machine := queueMachine{
			ID: m.ID.String(), Name: m.Name, Model: deref(m.Model), Status: m.Status,
			Loaded: loadedColours(m), Missing: []string{},
		}
		switch {
		case m.Status == production.FleetMachineOff:
			machine.Reason = "this printer is off"
		case !out.ColoursVerified:
			// Offered, because Tensor cannot honestly say otherwise - see
			// colourMatchingPossible. The swatches are still shown on both
			// sides, so the operator can do the comparison Tensor cannot.
			machine.Eligible = true
			eligible++
		default:
			machine.Missing = missingColours(out.Colours, machine.Loaded)
			if len(machine.Missing) == 0 {
				machine.Eligible = true
				eligible++
			} else {
				machine.Reason = "does not hold " + strings.Join(machine.Missing, ", ")
			}
		}
		out.Machines = append(out.Machines, machine)
	}

	if i := suggestMachine(out.Machines, out.Colours); i >= 0 {
		out.Machines[i].Suggested = true
	}

	switch {
	case len(out.Machines) == 0:
		out.Note = "No machines are known yet. Sync the fleet from BambuBuddy first."
	case !out.ColoursVerified:
		out.Note = "Tensor cannot check which colours these printers hold - the filament shelf " +
			"has never been synced, and an AMS reports a colour only as a hex code. " +
			"Compare the swatches yourself before sending."
	case eligible == 0:
		out.Note = "No printer currently holds every colour this bed needs. Load a spool, then try again."
	}
	c.JSON(http.StatusOK, out)
}

// colourMatchingPossible reports whether Tensor can tell what a printer holds.
//
// Today it cannot, unless the filament shelf has been synced, and the reason is
// worth writing down because the answer looks like a bug either way.
//
// An order names a colour in words - "BLUE", "SKY BLUE". An AMS reports one as a
// bare hex and nothing else: tray_sub_brands is empty and there is no name field
// on any tray in this fleet. filament_inventory is the only table that holds
// both, because the sync records BambuBuddy's own swatch against the colour
// name. With it empty, resolveColourHex falls back to the built-in table in
// dnp_colour.go, whose hexes are NOT the ones these printers report - it calls
// blue #1560BD where the AMS says #2850E0.
//
// Comparing those would mark every printer ineligible for every coloured bed,
// which reads as "the fleet is full" rather than "Tensor does not know". So the
// check reports what it can prove, and the dialog says the rest.
//
// A failure counts as "cannot check" rather than "can": a lost connection must
// not silently turn into a colour rule nobody can see being applied.
func (s *Server) colourMatchingPossible(ctx context.Context) bool {
	keys, err := s.store.Q.ListFilamentKeys(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the filament shelf for the queue dialog", "error", err)
		return false
	}
	return len(keys) > 0
}

// queueColoursFor is the bed's colours, by name and by swatch.
//
// Shared with the send path deliberately: the dialog's list and the check that
// refuses a machine must be built the same way, or the dialog offers a printer
// the server then rejects. A name with no swatch is kept rather than dropped -
// it still has to be shown, and resolveColourHex already normalises what it
// returns to "#RRGGBB".
func (s *Server) queueColoursFor(ctx context.Context, jobs []gen.ProductionJob) []queueColour {
	out := make([]queueColour, 0, 4)
	for _, name := range planColoursFromJobs(jobs) {
		colour := queueColour{Name: name}
		if hex, err := s.resolveColourHex(ctx, name); err == nil {
			colour.Hex = hex
		}
		out = append(out, colour)
	}
	return out
}

// suggestMachine picks the printer to offer first, or -1 when none fits.
//
// Nearest colour, not exact, and that difference is deliberate. An exact match
// is the right rule for REFUSING a machine - see missingColours, where being
// approximately right means printing a plank in the wrong colour. It is the
// wrong rule for a default, because the hexes an order resolves to and the hexes
// an AMS reports come from different sources and rarely agree to the byte (the
// shop's blue is #1560BD, the spool in the tray says #2850E0). Refusing to
// suggest anything unless they match exactly would leave the dropdown on "Pick a
// printer" every single time.
//
// A wrong suggestion costs nothing: it is pre-selected, both sets of swatches
// are on screen beside it, and changing it is one click. A wrong refusal costs a
// bed nobody can send.
//
// Idle beats busy at equal colour distance, because the point of choosing a
// machine is getting the plate printed sooner.
func suggestMachine(machines []queueMachine, colours []queueColour) int {
	best, bestScore := -1, 0
	for i, m := range machines {
		if !m.Eligible || len(m.Loaded) == 0 {
			continue
		}
		score := 0
		matched := true
		for _, c := range colours {
			d, ok := nearestColourDistance(c.Hex, m.Loaded)
			if !ok {
				matched = false
				break
			}
			score += d
		}
		if !matched {
			continue
		}
		// Busy machines are ranked behind idle ones by a margin wider than any
		// colour distance can reach, so colour still decides between two idle
		// printers and never loses to a marginally closer busy one.
		if m.Status != production.FleetMachineIdle {
			score += busyPenalty
		}
		if best < 0 || score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// busyPenalty outranks any possible colour distance (3 * 255^2 per colour).
const busyPenalty = 1 << 24

// nearestColourDistance is how far the closest loaded spool is from want, as a
// squared RGB distance. Reports false when either side cannot be read.
func nearestColourDistance(want string, loaded []string) (int, bool) {
	wr, wg, wb, ok := rgbOf(want)
	if !ok {
		return 0, false
	}
	best, found := 0, false
	for _, hex := range loaded {
		lr, lg, lb, ok := rgbOf(hex)
		if !ok {
			continue
		}
		d := (wr-lr)*(wr-lr) + (wg-lg)*(wg-lg) + (wb-lb)*(wb-lb)
		if !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// rgbOf reads "#RRGGBB" into its channels.
func rgbOf(hex string) (r, g, b int, ok bool) {
	normalised, ok := normaliseHex(hex)
	if !ok {
		return 0, 0, 0, false
	}
	var rr, gg, bb int
	if _, err := fmt.Sscanf(normalised, "#%02X%02X%02X", &rr, &gg, &bb); err != nil {
		return 0, 0, 0, false
	}
	return rr, gg, bb, true
}

// loadedColours is the hexes in one machine's AMS, as the fleet sync last saw
// them. Shape written by filamentsJSON (bambubuddy_sync.go).
func loadedColours(m gen.Machine) []string {
	var trays []struct {
		Colour string `json:"colour"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(m.Filaments, &trays); err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(trays))
	for _, t := range trays {
		hex, ok := normaliseHex(t.Colour)
		if !ok || seen[hex] {
			continue
		}
		seen[hex] = true
		out = append(out, hex)
	}
	return out
}

// missingColours names the bed's colours this machine does not hold.
//
// Matched on hex, never on name: the spool in the tray reports a colour, not a
// word, and "Sky Blue" is printed from the BLUE spool (see CanonicalColourName)
// - so comparing names would call a machine that holds exactly the right
// filament ineligible.
//
// Exact, never nearest. Sky Blue (#87CEEB) sits closer to a loaded #46A8F9 than
// Blue (#1560BD) does, so any tolerance loose enough to accept the right spool
// also accepts the wrong one - which is how a plank prints in a colour nobody
// ordered.
//
// A colour with no hex cannot be matched either way. It is reported missing:
// Tensor cannot say the machine holds it, and guessing yes is the answer that
// prints scrap.
func missingColours(needed []queueColour, loaded []string) []string {
	have := map[string]bool{}
	for _, hex := range loaded {
		have[hex] = true
	}
	missing := make([]string, 0, len(needed))
	for _, colour := range needed {
		if colour.Hex == "" || !have[colour.Hex] {
			missing = append(missing, colour.Name)
		}
	}
	return missing
}
