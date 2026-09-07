package httpapi

// The machine family a bed's jobs agree on.
//
// This is all that survives of the colour gate. That gate held a bed back from
// a printer unless some machine already had every colour of its plate loaded,
// and wrote the refusal to print_error - which is what put a red "no machine
// has this bed's filament loaded" note on most rows of the Batches page. It was
// the dispatch-time twin of the fleet scoring removed from planning: both made
// the fate of a bed depend on which spools happened to be in which printer at
// that instant. The shop's workflow is the other way round - the plate goes to
// the queue and the operator loads the spool it asks for.

import (
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// batchFamilyFromRows is the one machine family a bed's jobs agree on, or "" if
// they record none.
//
// The same reading batchMachineFamily takes on planner jobs: an unset family
// means unknown rather than different, so it does not create a disagreement.
// Where jobs genuinely disagree the answer is "" too, which leaves the choice
// of machine open rather than naming one no plank on the bed can use.
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
