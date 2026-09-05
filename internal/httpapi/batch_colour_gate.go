package httpapi

// The gate that stops a bed reaching a printer that cannot print it.
//
// BambuBuddy accepts a pipeline run on printer class and nozzle. It does not
// compare the plate's colours against the AMS trays - a blue plate was accepted,
// sliced and printed on a machine loaded with red and white. So Tensor checks
// before it sends, and holds the bed when nothing can print it.
//
// The colours compared are the ones written INTO the plate, not the shop's
// colour names: the merged 3MF declares "#FFFFFF" and "#1560BD" in its filament
// slots, and those are what a machine has to have loaded.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// colourGate is the answer to "can any machine print this bed right now?".
type colourGate struct {
	// Machine names that have every colour loaded. Empty means hold.
	Ready []string
	// Missing is what the closest machine lacked, for the hold reason.
	Missing []string
	// Checked is how many machines were eligible on family and status before
	// colour was considered - zero means the problem is not colour at all.
	Checked int
}

// canAnyMachinePrint reports whether some eligible machine holds every colour
// the plate needs.
//
// Eligible means powered on, not faulted, linked to an online profile, and of
// the family the bed's jobs want. Colour is checked last, so a bed that fails
// for a different reason says so rather than blaming a spool.
func (s *Server) canAnyMachinePrint(ctx context.Context, family string, need []string) colourGate {
	var gate colourGate
	if len(need) == 0 {
		// Nothing to match. Not a pass: a plate that declares no colours is a
		// plate we cannot reason about, and guessing is how the wrong one prints.
		gate.Missing = []string{"the plate declares no filament colours"}
		return gate
	}

	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the fleet to check colours", "error", err)
		gate.Missing = []string{"the fleet could not be read"}
		return gate
	}

	for _, m := range rows {
		if !machineCanPrint(m, family) {
			continue
		}
		gate.Checked++
		missing := production.MissingColours(need, loadedFilamentsOf(m.Filaments))
		if len(missing) == 0 {
			gate.Ready = append(gate.Ready, m.Name)
			continue
		}
		// Remember the closest miss so the hold reason names one spool to load
		// rather than every colour on every machine.
		if len(gate.Ready) == 0 && (gate.Missing == nil || len(missing) < len(gate.Missing)) {
			gate.Missing = missing
		}
	}
	return gate
}

// holdReason explains, in an operator's terms, why nothing can print this bed.
func (g colourGate) holdReason(need []string) string {
	switch {
	case g.Checked == 0:
		return "no machine of the required type is available, so this bed cannot be sent yet."
	case len(g.Missing) > 0:
		return fmt.Sprintf(
			"no machine has this bed's filament loaded. It needs %s; the closest machine is missing %s.",
			strings.Join(need, " and "), strings.Join(g.Missing, " and "))
	default:
		return "no machine can print this bed yet."
	}
}

// plateColoursFor is the colours the merged plate actually declares.
//
// Read from the jobs the same way the plate itself is built - the base plate's
// white plus each job's own resolved colour - so the gate compares exactly what
// the 3MF asks a printer for. Reading the shop's colour NAMES instead would
// compare a vocabulary the trays do not share.
func (s *Server) plateColoursFor(ctx context.Context, jobs []gen.ProductionJob) []string {
	seen := map[string]bool{}
	var out []string
	add := func(hex string) {
		if hex == "" || seen[hex] {
			return
		}
		seen[hex] = true
		out = append(out, hex)
	}

	for _, j := range jobs {
		if IsGeneratedProduct(deref(j.Sku), deref(j.ProductName)) {
			// Every generated plank has a white body, whatever its lettering.
			add(BasePlateColour)
		}
		add(s.jobColourHex(ctx, j))
	}
	return out
}

// batchFamilyFromRows is the one machine family a bed's jobs agree on, or "" if
// they record none.
//
// The same reading batchMachineFamily takes on planner jobs: an unset family
// means unknown rather than different, so it does not create a disagreement.
// Where jobs genuinely disagree the answer is "" too - which widens the gate to
// every machine, and is safe because colour is still checked on each one.
func batchFamilyFromRows(jobs []gen.ProductionJob) string {
	family := ""
	for _, j := range jobs {
		f := strings.TrimSpace(deref(j.MachineFamily))
		if f == "" {
			continue
		}
		if family == "" {
			family = f
			continue
		}
		if !strings.EqualFold(f, family) {
			return ""
		}
	}
	return family
}

// loadedFilamentsOf decodes the machines.filaments snapshot the fleet sync
// writes from each printer's AMS trays.
//
// An unreadable value decodes to nothing loaded, which the matcher treats as
// missing everything. That is the safe direction: a machine whose spools Tensor
// cannot read is not one to send a coloured plate to.
func loadedFilamentsOf(raw []byte) []production.LoadedFilament {
	if len(raw) == 0 {
		return nil
	}
	var rows []struct {
		Colour string `json:"colour"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}
	out := make([]production.LoadedFilament, 0, len(rows))
	for _, r := range rows {
		out = append(out, production.LoadedFilament{Colour: r.Colour, Type: r.Type})
	}
	return out
}

// machineCanPrint reports whether a fleet unit is eligible before colour is
// considered: powered on, not faulted, linked to an online profile, and of the
// family the bed needs.
//
// An unknown family matches any machine rather than none - the same reading
// batchMachineFamily takes, where unset means unknown rather than different.
func machineCanPrint(m gen.ListFleetMachinesWithFamilyRow, family string) bool {
	switch m.Status {
	case production.FleetMachineIdle, production.FleetMachineRunning:
	default:
		return false
	}
	if m.MachineProfileID == nil || m.ProfileStatus == nil || *m.ProfileStatus != production.MachineOnline {
		return false
	}
	if family == "" {
		return true
	}
	return m.ProfileFamily != nil && strings.EqualFold(*m.ProfileFamily, family)
}
