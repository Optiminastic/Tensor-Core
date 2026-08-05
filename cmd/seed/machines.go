package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// fleetSize is how many physical printers this seed creates when the fleet is
// empty - matches the three-machine fleet the production pipeline was
// designed around.
const fleetSize = 3

// seedFleetMachines ensures every fleet machine (table: machines) is linked to
// its own machine_profiles row - Stage 9's scheduler resolves a batch's
// machine_id (which points at machine_profiles) to a specific physical unit
// via that 1:1 link, so a machine can never be scheduled onto without one.
// Creates a default 3-machine fleet if none exists yet; either way, links
// whatever is still unlinked. Safe to run repeatedly: existing machines and
// profiles are reused, never duplicated.
func seedFleetMachines(ctx context.Context, store *db.Store) (int, error) {
	machines, err := store.Q.ListFleetMachines(ctx)
	if err != nil {
		return 0, fmt.Errorf("list fleet machines: %w", err)
	}
	if len(machines) == 0 {
		machines, err = createDefaultFleet(ctx, store)
		if err != nil {
			return 0, fmt.Errorf("create default fleet: %w", err)
		}
	}

	linkedProfiles := make(map[uuid.UUID]bool, len(machines))
	for _, m := range machines {
		if m.MachineProfileID != nil {
			linkedProfiles[*m.MachineProfileID] = true
		}
	}
	profiles, err := store.Q.ListMachineProfilesFull(ctx)
	if err != nil {
		return 0, fmt.Errorf("list machine profiles: %w", err)
	}

	linked := 0
	for _, m := range machines {
		if m.MachineProfileID != nil {
			continue
		}
		profileID, err := ensureFleetMachineProfile(ctx, store, m, profiles, linkedProfiles)
		if err != nil {
			return linked, fmt.Errorf("resolve profile for %s: %w", m.MachineID, err)
		}
		if _, err := store.Q.SetFleetMachineProfile(ctx, gen.SetFleetMachineProfileParams{
			ID: m.ID, MachineProfileID: &profileID,
		}); err != nil {
			return linked, fmt.Errorf("link %s: %w", m.MachineID, err)
		}
		linkedProfiles[profileID] = true
		linked++
	}
	return linked, nil
}

func createDefaultFleet(ctx context.Context, store *db.Store) ([]gen.Machine, error) {
	created := make([]gen.Machine, 0, fleetSize)
	for i := 1; i <= fleetSize; i++ {
		m, err := store.Q.InsertFleetMachine(ctx, gen.InsertFleetMachineParams{
			ID: uuid.New(), MachineID: fmt.Sprintf("H2C-%02d", i),
			Name: fmt.Sprintf("Bambu H2C Unit %d", i), Status: "idle", Filaments: []byte("[]"),
		})
		if err != nil {
			return nil, err
		}
		created = append(created, m)
	}
	return created, nil
}

// familyFromMachineCode derives a printer family from a fleet code like
// "H2C-01" (the part before the first hyphen), falling back to "H2S" for
// anything that doesn't follow that convention.
func familyFromMachineCode(code string) string {
	if i := strings.IndexByte(code, '-'); i > 0 {
		return code[:i]
	}
	return "H2S"
}

// ensureFleetMachineProfile finds an existing machine_profiles row of the
// right family that isn't already linked to a different fleet machine, or
// creates a new one with sane defaults.
func ensureFleetMachineProfile(
	ctx context.Context, store *db.Store, m gen.Machine,
	profiles []gen.ListMachineProfilesFullRow, linkedProfiles map[uuid.UUID]bool,
) (uuid.UUID, error) {
	family := familyFromMachineCode(m.MachineID)
	for _, p := range profiles {
		if p.Family == family && !linkedProfiles[p.ID] {
			return p.ID, nil
		}
	}
	// H2C is dual-nozzle hardware, and the design upload form always submits
	// both nozzles (defaulting to 0.4mm/standard each - see
	// components/designs/design-upload-form.tsx) - a symmetric default here is
	// what a design's auto-linked profile actually looks like out of the box,
	// so a default-answers design matches a fleet machine with no extra setup.
	rightNozzle := 0.4
	rightFlow := "standard"
	p, err := store.Q.InsertMachineProfileFull(ctx, gen.InsertMachineProfileFullParams{
		ID: uuid.New(), Name: fmt.Sprintf("Bambu %s (%s)", family, m.MachineID), IsActive: true,
		Family: family, NozzleMm: 0.4, RightNozzleMm: &rightNozzle, Flow: "standard", RightFlow: &rightFlow,
		LayerHeightMinMm: 0.08, LayerHeightMaxMm: 0.28,
		SupportedFilaments: []byte("[]"), Status: "offline", IsDefault: false,
	})
	if err != nil {
		return uuid.UUID{}, err
	}
	return p.ID, nil
}
