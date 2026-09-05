package httpapi

// Syncs Tensor's physical fleet from BambuBuddy.
//
// BambuBuddy is the authority on which printers exist and what they are doing
// right now; Tensor is the authority on what they have been ASKED to do. This
// file keeps the first in step with the second without letting either overwrite
// the other: identity, liveness and loaded filament come from the printer,
// while current_batch_id and the scheduling state around it stay Tensor's.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// SyncFleetResult is what one sync pass did, for the caller to log or return.
type SyncFleetResult struct {
	Synced  int      `json:"synced"`
	Removed int      `json:"removed"`
	Names   []string `json:"names"`
}

// SyncFleetFromBambuBuddy reconciles the machines table with BambuBuddy.
//
// Every printer BambuBuddy reports is created or refreshed; anything else in
// the fleet is removed, which is what clears placeholder machines out of
// Machine Management. A unit mid-print is never removed - deleting it would
// orphan the batch it is running - so a printer that genuinely disappeared
// while printing is left for a human.
//
// This is the operator-initiated path (POST /machine-fleet/sync). The prune is
// safe here precisely because somebody asked for it.
func (s *Server) SyncFleetFromBambuBuddy(ctx context.Context) (SyncFleetResult, error) {
	return s.syncFleet(ctx, syncFleetOptions{Prune: true})
}

// RefreshFleetFromBambuBuddy updates the fleet WITHOUT removing anything.
//
// This is what the periodic job runs, and the distinction is the whole reason
// the split exists. Reconciliation ends in DeleteFleetMachinesNotIn, so putting
// the full sync on a timer would make a destructive operation automatic: one
// transiently partial ListPrinters - a restarting BambuBuddy, a Tailscale blip -
// and machines are deleted, with only those mid-print protected.
//
// Removing a decommissioned printer is a once-in-a-while act a human can ask
// for. Refreshing status is the thing that has to happen every minute. They do
// not belong on the same schedule.
func (s *Server) RefreshFleetFromBambuBuddy(ctx context.Context) (SyncFleetResult, error) {
	return s.syncFleet(ctx, syncFleetOptions{Prune: false})
}

// syncFleetOptions selects how far a sync goes.
type syncFleetOptions struct {
	// Prune removes fleet rows BambuBuddy no longer reports. Only ever true on
	// a path a human initiated.
	Prune bool
}

func (s *Server) syncFleet(ctx context.Context, opts syncFleetOptions) (SyncFleetResult, error) {
	log := obs.FromContext(ctx)
	if !s.bambu.Configured() {
		return SyncFleetResult{}, fmt.Errorf("BambuBuddy is not configured (set BAMBUBUDDY_URL and BAMBUBUDDY_API_KEY)")
	}

	printers, err := s.bambu.ListPrinters(ctx)
	if err != nil {
		return SyncFleetResult{}, fmt.Errorf("list printers: %w", err)
	}

	// The sync reads the client directly - it is the writer of truth and must
	// never read through a cache it fills - but sharing what it just fetched
	// costs nothing and means a manual Sync makes a newly added printer
	// resolvable immediately rather than after the index TTL.
	s.bambuCache.putIndex(printers)

	out := SyncFleetResult{Names: make([]string, 0, len(printers))}
	keep := make([]string, 0, len(printers))

	for _, p := range printers {
		code := fleetCode(p)
		keep = append(keep, code)

		// Live state is best-effort. A printer BambuBuddy knows about but
		// cannot currently reach is still part of the fleet - it is simply
		// off, which is exactly what the status column is for.
		status, statusErr := s.bambu.GetStatus(ctx, p.ID)
		if statusErr != nil {
			log.Warn("bambubuddy status unavailable, recording the printer as off",
				"printer", p.Name, "error", statusErr)
		} else {
			// Only a good status is shared. Seeding a failure would make the
			// sync's bad luck everyone's for the whole error TTL.
			s.bambuCache.putStatus(p.ID, status)
		}

		profileID, err := s.profileForPrinter(ctx, p, status)
		if err != nil {
			log.Warn("could not resolve a machine profile for printer", "printer", p.Name, "error", err)
		}

		machine, err := s.store.Q.UpsertFleetMachineFromSource(ctx, gen.UpsertFleetMachineFromSourceParams{
			ID: uuid.New(), MachineID: code, Name: fleetName(p),
			Status:       fleetStatus(p, status, statusErr == nil),
			StatusReason: statusReason(status),
			Filaments:    filamentsJSON(status),
			// What the unit is, so the fleet list can be read without opening
			// BambuBuddy to find out which of thirteen printers is an H2C.
			Model:       nonEmptyPtr(p.Model),
			Location:    nonEmptyPtr(p.Location),
			IpAddress:   nonEmptyPtr(p.IPAddress),
			NozzleCount: int32Ptr(p.NozzleCount),
			// Layer progress only means something mid-print. A finished plate
			// still reporting 25/25 would render as a machine permanently at
			// 100%, which reads as stuck rather than done.
			CurrentLayer: layerPtr(status, status.Printing()),
			TotalLayers:  totalLayerPtr(status, status.Printing()),
		})
		if err != nil {
			return out, fmt.Errorf("upsert machine %s: %w", code, err)
		}

		if profileID != nil && (machine.MachineProfileID == nil || *machine.MachineProfileID != *profileID) {
			if _, err := s.store.Q.SetFleetMachineProfile(ctx, gen.SetFleetMachineProfileParams{
				ID: machine.ID, MachineProfileID: profileID,
			}); err != nil {
				log.Warn("could not link printer to its machine profile", "printer", p.Name, "error", err)
			}
		}

		out.Synced++
		out.Names = append(out.Names, p.Name)
	}

	if !opts.Prune {
		log.Info("fleet refreshed from bambubuddy", "printers", out.Synced)
		return out, nil
	}

	before, err := s.store.Q.ListFleetMachineCodes(ctx)
	if err != nil {
		return out, fmt.Errorf("list fleet codes: %w", err)
	}
	if err := s.store.Q.DeleteFleetMachinesNotIn(ctx, keep); err != nil {
		return out, fmt.Errorf("remove machines BambuBuddy no longer reports: %w", err)
	}
	after, err := s.store.Q.ListFleetMachineCodes(ctx)
	if err != nil {
		return out, fmt.Errorf("list fleet codes after prune: %w", err)
	}
	out.Removed = len(before) - len(after)

	log.Info("fleet synced from bambubuddy",
		"printers", out.Synced, "removed", out.Removed)
	return out, nil
}

// fleetCode is the stable identity for a physical unit.
//
// The serial number, not the display name: BambuBuddy lets an operator rename a
// printer, and keying on the name would make a rename look like one machine
// disappearing and a different one arriving - taking its history with it.
func fleetCode(p bambubuddy.Printer) string {
	if strings.TrimSpace(p.SerialNumber) != "" {
		return p.SerialNumber
	}
	return fmt.Sprintf("bambubuddy-%d", p.ID)
}

// fleetName is what an operator sees: the printer's own name, exactly as they
// named it in BambuBuddy.
//
// The model used to be appended - "A1 (A2L)" - because there was nowhere else
// to put it. It has its own column now, so appending it would print the same
// fact twice in the same row and stop the two systems agreeing on what a
// printer is called.
func fleetName(p bambubuddy.Printer) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return fmt.Sprintf("Printer %d", p.ID)
}

// fleetStatus maps Bambu's state vocabulary onto Tensor's four.
//
// Bambu distinguishes RUNNING, FINISH, IDLE, PREPARE, PAUSE, FAILED and more;
// Tensor's machines column allows idle/running/off/error. Only RUNNING is
// "running" - a finished plate sitting on the bed is not consuming machine
// time, it is waiting for a human, and counting it as running would keep the
// scheduler from ever assigning that printer more work.
//
// Derived from STATE, never from the printer's HMS list.
//
// This used to treat any health message above severity 1 as a fault, assuming
// Bambu's severity ran from 1 (informational) upward. It does not, and the
// live fleet disproved it outright: severity 6 appears on printers that had
// just FINISHED a plate at 143/143, on printers midway through RUNNING one,
// and on printers that had FAILED - the same value across success and failure
// alike. Two printers reported an identical code with different severities.
//
// So the whole fleet rendered red, including machines printing normally at
// that moment. An invented fault is worse than a missed one: it teaches an
// operator that red rows mean nothing, which is the habit that makes a real
// fault invisible. BambuBuddy reads these codes in its own UI with knowledge
// Tensor does not have; until Tensor has it too, HMS is information to show,
// not a status to derive.
func fleetStatus(p bambubuddy.Printer, s bambubuddy.Status, reachable bool) string {
	if !p.IsActive || !reachable || !s.Connected {
		return production.FleetMachineOff
	}
	if s.Printing() {
		return production.FleetMachineRunning
	}
	// FAILED lands here, as idle, and that is deliberate.
	//
	// BambuBuddy reports FAILED as a fact about the LAST PRINT, not about the
	// printer: such a machine is connected, sitting at 0% and available - its
	// own UI shows it as ready, and it keeps assigning work to it. Tensor used
	// to turn that into an error status, which painted five of thirteen
	// machines red and, far worse, took them out of scheduling entirely:
	// machineCanPrint and plannableMachine both accept only idle or running, so
	// five free printers were being withheld from batching because of something
	// that happened on a previous job.
	//
	// The information is not lost - statusReason still says the last print
	// failed and the plate needs clearing. It is reported as a note about a
	// working machine rather than as a fault that stops it being used.
	return production.FleetMachineIdle
}

// statusReason is what a person still needs to do to this machine, or nil.
//
// Advisory, not a status: a printer whose last print failed is available and
// scheduled like any other (see fleetStatus), but somebody has to clear the
// plate before the next plate lands on top of the last one.
//
// HMS codes are deliberately not reported here, for the reason fleetStatus
// gives: a line reading "Printer fault 0500060000020070" beside a machine that
// is printing perfectly well is worse than silence - a fault report the
// operator cannot act on and that turns out not to be true.
func statusReason(s bambubuddy.Status) *string {
	if !s.Failed() {
		return nil
	}
	text := "The last print failed - check the plate is clear before the next one."
	return &text
}

// filamentsJSON renders the AMS trays that actually hold filament, in the shape
// the fleet DTO already uses.
//
// remain is a percentage (0-100) and is -1 when the spool carries no RFID tag,
// so it is only reported when the printer genuinely knows.
func filamentsJSON(s bambubuddy.Status) []byte {
	type loaded struct {
		Colour         string  `json:"colour"`
		Type           string  `json:"type"`
		RemainingGrams float64 `json:"remaining_grams"`
	}
	out := make([]loaded, 0, 4)
	for _, ams := range s.AMS {
		for _, t := range ams.Trays {
			if !t.Exists || strings.TrimSpace(t.Type) == "" {
				continue
			}
			var grams float64
			if t.Remain >= 0 {
				// A full Bambu spool is 1kg; remain is the percentage left.
				grams = float64(t.Remain) * 10
			}
			out = append(out, loaded{
				Colour: hexColour(t.Colour), Type: t.Type, RemainingGrams: grams,
			})
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

// hexColour turns Bambu's 8-digit RGBA ("FFF144FF") into a CSS hex colour.
func hexColour(c string) string {
	c = strings.TrimSpace(c)
	if c == "" {
		return ""
	}
	if len(c) == 8 {
		c = c[:6] // drop the alpha byte
	}
	return "#" + strings.ToUpper(c)
}

func layerPtr(s bambubuddy.Status, printing bool) *int32 {
	if !printing {
		return nil
	}
	v := int32(s.LayerNum)
	return &v
}

func totalLayerPtr(s bambubuddy.Status, printing bool) *int32 {
	if !printing || s.TotalLayers <= 0 {
		return nil
	}
	v := int32(s.TotalLayers)
	return &v
}

// profileForPrinter resolves the machine_profiles row describing this
// printer's slicing configuration, creating it if the fleet has not seen this
// combination before.
//
// The profile is the SLICING config (family, nozzles, flow) that batches are
// grouped and assigned against; the machines row is the physical unit. Two
// identical printers share one profile, which is what lets a batch planned for
// "H2C 0.4/0.4 high-flow" run on whichever of them frees up first.
//
// Nozzles come from the live status rather than the printer record: nozzle_count
// says how many there are, but only the status reports what is actually
// installed, and an operator can swap a hot end without telling anyone.
func (s *Server) profileForPrinter(
	ctx context.Context, p bambubuddy.Printer, st bambubuddy.Status,
) (*uuid.UUID, error) {
	family := strings.TrimSpace(p.Model)
	if family == "" {
		return nil, fmt.Errorf("printer %s reports no model", p.Name)
	}

	left, right, flow, rightFlow := nozzleConfig(st, p.NozzleCount)
	if left <= 0 {
		return nil, fmt.Errorf("printer %s reports no nozzle diameter", p.Name)
	}

	existing, err := s.store.Q.FindMachineProfileByNozzleFlow(ctx, gen.FindMachineProfileByNozzleFlowParams{
		Family: family, NozzleMm: left, RightNozzleMm: right, Flow: flow, RightFlow: rightFlow,
	})
	if err == nil {
		return &existing.ID, nil
	}
	if !isNoRows(err) {
		return nil, err
	}

	name := profileName(family, left, right, flow)
	created, err := s.store.Q.InsertMachineProfileFull(ctx, gen.InsertMachineProfileFullParams{
		ID: uuid.New(), Name: name, IsActive: true, Family: family,
		NozzleMm: left, RightNozzleMm: right, Flow: flow, RightFlow: rightFlow,
		LayerHeightMinMm: 0.08, LayerHeightMaxMm: 0.28,
		SupportedFilaments: []byte("[]"),
		Status:             production.MachineOnline, IsDefault: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create machine profile %q: %w", name, err)
	}
	return &created.ID, nil
}

// nozzleConfig reads the installed hot ends. Falls back to the printer record's
// nozzle_count with a 0.4 default when the live status is unavailable, so a
// printer that is merely unreachable still gets a sensible profile rather than
// none at all.
func nozzleConfig(st bambubuddy.Status, nozzleCount int) (left float64, right *float64, flow string, rightFlow *string) {
	flow = "standard"
	if len(st.Nozzles) == 0 {
		left = 0.4
		if nozzleCount > 1 {
			r := 0.4
			right, rightFlow = &r, &flow
		}
		return left, right, flow, rightFlow
	}

	left = st.Nozzles[0].DiameterMM()
	flow = nozzleFlow(st.Nozzles[0].Type)
	if len(st.Nozzles) > 1 {
		r := st.Nozzles[1].DiameterMM()
		rf := nozzleFlow(st.Nozzles[1].Type)
		right, rightFlow = &r, &rf
	}
	return left, right, flow, rightFlow
}

// nozzleFlow maps Bambu's hot-end type code onto the flow vocabulary the
// pricing and slicing config use. HS01 is the high-flow hardened hot end; the
// stainless/standard types are not.
func nozzleFlow(nozzleType string) string {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(nozzleType)), "HS") {
		return "high_flow"
	}
	return "standard"
}

// profileName is the human-readable slicing-config name, e.g.
// "Bambu H2C 0.4/0.4 high_flow". machine_profiles.name is UNIQUE, so it has to
// describe the configuration rather than the printer - two identical machines
// share one row.
func profileName(family string, left float64, right *float64, flow string) string {
	nozzles := trimFloat(left)
	if right != nil {
		nozzles += "/" + trimFloat(*right)
	}
	return fmt.Sprintf("Bambu %s %s %s", family, nozzles, flow)
}

func trimFloat(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}
