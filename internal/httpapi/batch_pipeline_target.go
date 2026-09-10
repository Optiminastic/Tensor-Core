package httpapi

// Choosing which of BambuBuddy's slicer pipelines may take a plate.
//
// A pipeline is a printer binding: it carries either a model class ("A2L",
// "P2S", "H2C") or one specific printer, and slicing through it is what decides
// the machine a plate lands on. Tensor was ignoring that entirely - it ran every
// pipeline BambuBuddy returned, in whatever order it returned them, and took the
// first that did not refuse.
//
// That was survivable only because every bed holds the same four planks. It
// stops being survivable the moment a bed is sized for the machine that will
// print it: a 7-up H2C plate handed to the P2S pipeline is a plate that cannot
// fit the bed it was sliced for. The eligibility check on BambuBuddy's side
// looks at printer class and filament, not at whether the parts fit, so nothing
// downstream would catch it.
//
// So a plate is offered to the pipeline for the class Tensor planned it for
// FIRST - and then, if that class cannot take it, to the others. The planned
// class is a preference rather than a restriction, because today nothing about
// the plate is class-specific: bedpack packs everything onto one 330mm bed and
// holds no per-family dimensions, so the "7-up H2C plate" above does not yet
// exist. See pipelinesFor for what would have to change if it ever does.
//
// Which physical printer runs it stays BambuBuddy's call, which is what the
// floor wants - it is the only side that can compare the plate's declared
// filament slots against what is really in each AMS.

import (
	"context"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// batchTargetFamily is the printer class a bed was planned for.
//
// The batch's assigned machine profile is the authority: it is what the machine
// scheduler picked at plan time, weighing filament match and how busy each
// printer was, and it is what the bed's capacity will be derived from. The jobs'
// own recorded family is the fallback for a bed with no machine yet.
//
// An empty answer means "unknown", and the caller widens to every pipeline
// rather than refusing - the same way canAnyMachinePrint treats an unknown
// family as matching any machine.
func (s *Server) batchTargetFamily(ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob) string {
	if batch.MachineID != nil {
		if profile, err := s.store.Q.GetMachineProfileFull(ctx, *batch.MachineID); err == nil {
			if family := strings.TrimSpace(profile.Family); family != "" {
				return family
			}
		} else {
			obs.FromContext(ctx).Warn("could not read a batch's machine profile for its family",
				"batch", batch.BatchNumber, "error", err)
		}
	}
	return batchFamilyFromRows(jobs)
}

// pipelinesFor ORDERS BambuBuddy's pipelines by how well each suits this plate,
// best first. It no longer drops any.
//
// Ordering is on the model class, because that is the granularity Tensor plans
// at: a batch's machine_id is a machine_profiles row - a printer class with a
// nozzle and flow - not one physical printer. Which of the three H2Cs actually
// runs the plate stays BambuBuddy's decision, which is what the floor wants.
//
// A pipeline pinned to one specific printer is skipped when the family is known:
// its printer may be any model, and Tensor cannot tell which from the pipeline
// alone. A pipeline that names no target at all is kept as a last resort - an
// install with one general-purpose pipeline is a valid setup, and dropping it
// would mean nothing prints, which is worse than slicing through a pipeline that
// never said what it was for.
func pipelinesFor(all []bambubuddy.Pipeline, family string) []bambubuddy.Pipeline {
	family = strings.TrimSpace(family)
	if family == "" {
		return all // unknown class: widen, as the colour gate does
	}

	var byFamily, untargeted, otherFamilies []bambubuddy.Pipeline
	for _, p := range all {
		switch {
		case p.TargetModelClass != nil && strings.TrimSpace(*p.TargetModelClass) != "":
			if strings.EqualFold(strings.TrimSpace(*p.TargetModelClass), family) {
				byFamily = append(byFamily, p)
			} else {
				otherFamilies = append(otherFamilies, p)
			}
		case p.TargetPrinterID != nil:
			// Pinned to one printer of an unknown model - not safe to assume.
		default:
			untargeted = append(untargeted, p)
		}
	}
	// Preferred class first, then anything else that could take it.
	//
	// The planned class is a PREFERENCE, not a restriction, because nothing
	// about the plate is class-specific: bedpack packs every plate onto one
	// bed - bedpack.BedXMM is a single global 330mm constant, and Tensor holds
	// no per-family bed dimensions at all - so an A2L plate and an H2C plate of
	// the same four planks are byte-identical. Narrowing to the planned class
	// therefore bought no safety and cost the fleet: with
	// GENERATED_MACHINE_FAMILY stamping every plank A2L, five of thirteen
	// printers did all the plank work while eight sat idle.
	//
	// Whether a given printer can actually run it stays BambuBuddy's call - it
	// compares the plate's declared filament slots against real AMS trays, which
	// is the colour matching the shop wants and which Tensor cannot do as well.
	// sliceAndQueue takes the first pipeline that accepts, so this ordering means
	// "the class we planned for if it can, otherwise whoever can".
	//
	// If plates ever become class-specific - a 7-up H2C plate that cannot fit a
	// P2S bed - this ordering must become a restriction again, and bedpack
	// growing per-family dimensions is the signal that day has come.
	out := append(byFamily, untargeted...)
	return append(out, otherFamilies...)
}

// pipelineTargetNote describes what was looked for, for a hold reason a person
// can act on. "no pipeline targets H2C" is a setup problem with an obvious fix;
// "no printer can print this plate" is not.
func pipelineTargetNote(family string) string {
	if strings.TrimSpace(family) == "" {
		return "BambuBuddy has no slicer pipeline this plate can use."
	}
	return "BambuBuddy has no slicer pipeline for " + family +
		" printers, so this plate cannot be sliced for the machine it was planned for. " +
		"Add a pipeline whose target model class is " + family + "."
}
