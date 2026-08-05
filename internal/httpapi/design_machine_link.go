package httpapi

// Bridges the design-upload form's own nozzle/flow answers to a
// machine_profiles row, per the confirmed decision to keep designs.machine_id
// as the single link Stage 9's scheduler and the SKU-match pipeline already
// depend on (see design_match.go) rather than duplicating nozzle/flow onto
// designs directly.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// designMachineFamily is the printer family every design-upload-created
// machine profile is linked to - the shop's fleet is BBL H2C dual-nozzle
// (see cmd/seed/machines.go's default fleet).
const designMachineFamily = "H2C"

// designLayerHeightMinMM/MaxMM bound every auto-created profile's layer-height
// range - the full span the design form's quality taxonomy covers
// (internal/slicing/profiles.go's qualityLayerHeightMM), not a per-design value
// (quality/layer height is chosen per design, not per machine profile).
const (
	designLayerHeightMinMM = 0.08
	designLayerHeightMaxMM = 0.24
)

// nozzleSpec is one design upload's requested dual-nozzle slicing
// configuration, as picked on the design form.
type nozzleSpec struct {
	LeftNozzleMM  float64
	RightNozzleMM float64
	LeftFlow      string
	RightFlow     string
}

// findOrCreateMachineProfile resolves spec to a machine_profiles row, reusing
// an exact match if one already exists - so every design with the same
// nozzle/flow combination shares one profile, keeping the set of profiles
// Stage 9's scheduler links fleet machines against stable - or creating one.
func (s *Server) findOrCreateMachineProfile(ctx context.Context, q *gen.Queries, spec nozzleSpec) (uuid.UUID, error) {
	rightNozzle := spec.RightNozzleMM
	rightFlow := spec.RightFlow
	existing, err := q.FindMachineProfileByNozzleFlow(ctx, gen.FindMachineProfileByNozzleFlowParams{
		Family: designMachineFamily, NozzleMm: spec.LeftNozzleMM, RightNozzleMm: &rightNozzle,
		Flow: spec.LeftFlow, RightFlow: &rightFlow,
	})
	if err == nil {
		return existing.ID, nil
	}
	if !isNoRows(err) {
		return uuid.UUID{}, fmt.Errorf("find machine profile: %w", err)
	}

	filaments, err := json.Marshal(slicing.AvailableFilaments())
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("encode filaments: %w", err)
	}
	name := fmt.Sprintf("Bambu %s %.1f/%.1fmm %s/%s", designMachineFamily,
		spec.LeftNozzleMM, spec.RightNozzleMM, spec.LeftFlow, spec.RightFlow)
	created, err := q.InsertMachineProfileFull(ctx, gen.InsertMachineProfileFullParams{
		ID: uuid.New(), Name: name, IsActive: true, Family: designMachineFamily,
		NozzleMm: spec.LeftNozzleMM, RightNozzleMm: &rightNozzle,
		Flow: spec.LeftFlow, RightFlow: &rightFlow,
		LayerHeightMinMm: designLayerHeightMinMM, LayerHeightMaxMm: designLayerHeightMaxMM,
		SupportedFilaments: filaments, Status: production.MachineOffline, IsDefault: false,
	})
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("create machine profile: %w", err)
	}
	return created.ID, nil
}
