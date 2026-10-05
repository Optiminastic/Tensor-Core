package httpapi

// Slicing a bed FOR the printer an operator chose, then queueing it there.
//
// The path this replaces uploaded the plate unsliced and ran a slicer pipeline,
// which produced two faults that looked unrelated and were the same thing: a
// pipeline run carries only a file id, so everything about the slice came from
// whatever that pipeline happened to be configured with.
//
//   - The pipeline holds ONE filament preset, so a two-colour bed was sliced to
//     one filament. Verified on this shop's own files: a plate declaring
//     #FFFFFF and #1560BD came back declaring #FFFFFF alone, 265g of it. The
//     bed printed monochrome and nothing reported a fault.
//   - A run targets a printer CLASS and answers 202, so the queue item that
//     carries printer_id does not exist yet. The operator's choice was a
//     suggestion BambuBuddy was free to ignore.
//
// Slicing the file directly fixes both, because Tensor gets to say what the
// slice is made of: one filament preset per plate slot, and the colours taken
// from the CHOSEN PRINTER'S OWN TRAYS rather than from the plate. BambuBuddy's
// filament check then compares a tray against itself, and because Tensor queues
// the resulting sliced file itself, printer_id is set at creation.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxPlateBytes bounds the plate read into memory to inspect its slots.
//
// The zip directory is at the END of a 3MF, so the slot declaration cannot be
// streamed - the whole file has to be addressable. A merged four-plank plate is
// a few megabytes; this is headroom, not a target, and a file past it is refused
// rather than being allowed to exhaust the process.
const maxPlateBytes = 256 << 20

// sendOrigin says who asked for this send, because two rules turn on it.
//
// An explicit origin rather than sniffing the actor string: systemActor is used
// by several automatic steps, and a rule that reads "not a person" by comparing
// a name is one refactor away from a new caller silently inheriting a
// permission nobody meant to give it.
type sendOrigin int

const (
	// operatorSend is a person pressing Queue. "Run that again" is theirs to
	// say, so this origin may clear a failed print outcome.
	operatorSend sendOrigin = iota
	// automaticSend is the dispatcher, on its own schedule with nobody
	// watching. It may never clear a failed print outcome - retrying a failed
	// plate into whatever went wrong the first time is a second wasted bed,
	// every seven minutes - and it re-checks the bed's jobs, which nothing
	// does for an already-locked bed otherwise.
	automaticSend
)

// sendBatchToMachine slices a bed for one printer and schedules its queueing.
//
// Returns before the slice finishes - that takes minutes - so the response says
// the plate is on its way rather than claiming it is queued.
func (s *Server) sendBatchToMachine(
	ctx context.Context, batch gen.Batch, machine gen.Machine, slotTrays []int,
	actor string, origin sendOrigin,
) (queueBatchResponse, error) {
	log := obs.FromContext(ctx)
	out := queueBatchResponse{BatchNumber: batch.BatchNumber, MachineName: machine.Name}

	batch, locked, err := s.prepareBatchForQueue(ctx, batch, actor, origin)
	if err != nil {
		return out, err
	}
	out.Locked = locked

	// One physical print per bed. bambu_slice_job_id is here because a bed
	// mid-slice has neither a queue item nor a pipeline run, and without it a
	// second press would slice and print the same plate again.
	if batch.QueueItemID != nil || batch.PipelineRunID != nil || batch.BambuSliceJobID != nil {
		out.Note = "this batch is already on its way to a printer"
		return out, nil
	}

	// The plate has to be laid out for THIS machine's class, and usually is
	// not. The planner packs a bed when it locks it, before any machine is
	// chosen, using the unit-count rule - so a one-job bed is a P2S plate
	// whatever it is later sent to. The classes are not nested: an H2C's two
	// nozzles both reach only X 25..325, so a P2S plate starts 15mm inside the
	// strip its second nozzle cannot reach and the slice fails with "Found
	// G-code in unprintable area of multi-extruder printers".
	//
	// Re-plating here rather than refusing, because the bed is perfectly
	// printable - it was simply laid out for a different machine, and the
	// layout is the only thing that has to change.
	if batch, err = s.replateForMachine(ctx, batch, machine); err != nil {
		return out, err
	}

	plate, err := s.plateFileFor(ctx, batch)
	if err != nil {
		return out, err
	}

	// What the plate asks for, read from the plate. Not recomputed: the slot
	// ORDER is planObjects' rule (the plank body takes slot 1), which nothing
	// outside meshio knows, and planColoursFromJobs omits the body colour
	// entirely.
	slots, err := s.plateSlots(ctx, plate)
	if err != nil {
		return out, err
	}
	if len(slots) == 0 {
		return out, statusErr(http.StatusConflict,
			"This batch's plate declares no filament. Rebuild the bed, then send it again.")
	}

	// The machine's trays, read LIVE. The mirrored row is up to a sync interval
	// old and the whole send turns on it; a spool swapped since the last sync
	// would otherwise be mapped to the colour it used to hold.
	trays := s.liveTraysFor(ctx, machine)

	// The bed's own colours travel with it. Without them the live re-bind here
	// judges the plate purely on the hex baked into it, so a bed locked before
	// its colour was mapped is refused at the last step - after the machine was
	// chosen, on the same fleet that had just accepted it.
	bedJobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return out, statusErr(http.StatusInternalServerError, "Could not read the batch's jobs.")
	}
	assignments, err := s.bindSlots(ctx, slots, liveBinding{
		Trays: trays, Chosen: slotTrays, MachineName: machine.Name,
		Bed: bedColoursOf(s.queueColoursFor(ctx, bedJobs)),
	})
	if err != nil {
		return out, statusErr(http.StatusConflict, err.Error())
	}

	printerID, err := s.bambuCache.printerID(ctx, machine.MachineID, s.bambu.ListPrinters)
	if err != nil || printerID <= 0 {
		// Fatal rather than degraded: there is no printer_id to queue against,
		// and printing on an unknown machine is not a graceful fallback.
		log.Warn("could not resolve the chosen machine to a BambuBuddy printer",
			"machine", machine.Name, "serial", machine.MachineID, "error", err)
		return out, statusErr(http.StatusConflict, fmt.Sprintf(
			"BambuBuddy does not know a printer with %s's serial number. Sync the fleet and try again.",
			machine.Name))
	}

	pipeline, err := s.pipelineForBed(ctx, bedJobs, deref(machine.Model))
	if err != nil {
		s.recordPrintError(ctx, batch.ID, err.Error())
		return out, statusErr(http.StatusConflict, err.Error())
	}

	uploaded, err := s.uploadPlate(ctx, batch, plate)
	if err != nil {
		return out, err
	}
	out.Filename = uploaded.Filename

	// One filament preset PER SLOT. A single preset is what collapsed a
	// two-colour bed to one filament, and repeating the pipeline's own preset is
	// what keeps the second slot alive without duplicating slicer config here.
	presets := make([]bambubuddy.PresetVal, 0, len(assignments))
	for range assignments {
		presets = append(presets, filamentPresetOf(pipeline))
	}

	job, err := s.bambu.SliceFile(ctx, uploaded.ID, bambubuddy.SliceRequest{
		PrinterPreset:   pipeline.PrinterPreset,
		ProcessPreset:   pipeline.ProcessPreset,
		FilamentPresets: presets,
		FilamentColours: trayColoursOf(assignments),
		ExportThreeMF:   true,
		// bedpack already placed every part and meshio merged them at those
		// offsets; letting the slicer rearrange the plate would discard it.
		AutoOrient: false, AutoArrange: false, UseEmbeddedSettings: false,
		ProcessOverrides: nozzleMapOverrides(machine, assignments, s.physicalExtruderMap(ctx, pipeline)),
	})
	if err != nil {
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			s.recordPrintError(ctx, batch.ID, reason.Reason)
			return out, statusErr(http.StatusUnprocessableEntity, reason.Reason)
		}
		s.recordPrintError(ctx, batch.ID, "Could not start slicing on BambuBuddy.")
		return out, statusErr(http.StatusBadGateway, "Could not start slicing on BambuBuddy.")
	}

	// The marker and the queueing, together or not at all.
	//
	// They were two statements and each could fail alone, in its own bad way.
	// A failed SetBatchSliceJob left bambu_slice_job_id unset, so the
	// already-sent guard above could not fire and the next pass would slice and
	// PRINT the same plate again. A failed Enqueue left it set with nothing
	// coming to queue the result, so the bed was stuck: the guard refuses it
	// for ever and ClearBatchPrintOutcome does not clear that column.
	//
	// In one transaction the only outcome is "neither happened": a slice runs
	// on BambuBuddy that nothing collects, and the next pass sends the bed
	// cleanly. A wasted slice costs minutes of a slicer's time. A wasted bed
	// costs plastic and somebody's plank.
	sliceJobID := int32(job.JobID)
	err = s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		if err := q.SetBatchSliceJob(ctx, gen.SetBatchSliceJobParams{
			ID: batch.ID, BambuSliceJobID: &sliceJobID,
			// The physical printer, not the profile. Until a queue item exists
			// this row is the only record that this bed is on its way to this
			// unit - which is what keeps the next bed from being ranked
			// against a fleet that still looks idle.
			FleetMachineID: &machine.ID,
		}); err != nil {
			return err
		}
		return s.sliceQueueEnqueuer.EnqueueTx(ctx, tx, production.QueueSlicedPlateArgs{
			BatchID: batch.ID, SliceJobID: job.JobID, PrinterID: printerID,
			MachineID: machine.ID, MachineName: machine.Name,
			AmsMapping: amsMappingOf(assignments), TrayHexes: trayColoursOf(assignments),
		})
	})
	if err != nil {
		// The slice is running either way; what is lost is the queueing that
		// follows it. Say so rather than reporting a clean success.
		log.Error("could not record the slice or schedule its queueing",
			"batch", batch.BatchNumber, "error", err)
		s.recordPrintError(ctx, batch.ID,
			"Tensor started slicing this bed but could not schedule its queueing. It will be sent again.")
		out.Note = "slicing started, but Tensor could not schedule the queueing. Send it again once the slice finishes."
		return out, nil
	}

	out.Queued = true
	out.Pinned = true
	out.Note = fmt.Sprintf("slicing for %s now, then it goes on that printer", machine.Name)
	log.Info("bed sent for slicing against a chosen printer",
		"batch", batch.BatchNumber, "machine", machine.Name, "printer", printerID,
		"slice_job", job.JobID, "colours", trayColoursOf(assignments),
		"ams_mapping", amsMappingOf(assignments))
	return out, nil
}

// liveBinding is what bindSlots weighs: the printer's trays as they are right
// now, whatever the operator chose, and the machine's name for the refusal.
type liveBinding struct {
	Trays []loadedTray
	// Chosen is the operator's own slot-to-tray answer, empty when Tensor is
	// deciding.
	Chosen      []int
	MachineName string
	// Bed is what the ORDER asked for, which rescues a plate whose hex was
	// baked in before its colour was mapped.
	Bed bedColours
}

// bindSlots decides which spool prints each slot, against the trays the printer
// is holding RIGHT NOW.
//
// Two callers, two rules, and the difference matters.
//
// An operator who named a machine also named the spools, and their answer is
// checked rather than recomputed: they are standing at the printer, and if they
// say the third tray is the blue this order meant, that beats anything Tensor
// can read.
//
// Nobody named anything, so Tensor chose the printer - and it must bind here,
// through the colour map, against these live trays. Not reuse the binding that
// picked the machine: THAT was computed from machines.filaments, a mirror up to
// a sync interval old, and a mapping is a list of tray POSITIONS. A spool
// swapped in the last minute leaves those positions valid and their contents
// wrong, so the plate would be sliced declaring whatever now sits in slot 3 and
// print the lettering in it. Re-binding here asks the colour map whether what
// is actually loaded is still this bed's colour, and refuses when it is not.
func (s *Server) bindSlots(
	ctx context.Context, slots []meshio.Slot, in liveBinding,
) ([]slotAssignment, error) {
	trays, chosen, name := in.Trays, in.Chosen, in.MachineName
	if len(chosen) > 0 {
		return assignmentsFromChoice(slots, trays, chosen)
	}

	identities, err := s.colourIdentities(ctx)
	if err != nil {
		// Only exact hex matches will bind without it, which is a refusal for
		// most beds rather than a wrong print. Say so rather than proceeding
		// with a weaker rule nobody asked for.
		return nil, fmt.Errorf("could not read the colour map, so this bed cannot be matched to a printer")
	}
	bound, err := bindPlateToTrays(slots, trays, identities, in.Bed)
	if err != nil {
		return nil, fmt.Errorf("%s no longer holds this bed's colours: %w", name, err)
	}
	return assignmentsFromChoice(slots, trays, bound)
}

// prepareBatchForQueue locks a Draft and reports whether it did.
//
// Shared with the automatic path so the status rules live in one place: a Draft
// is locked, never sent as a Draft, because the next planning pass can dissolve
// one and rebuild it from different jobs.
//
// actor is load-bearing, not just recorded: systemActor is refused the one
// thing on this path that only a person may do - clearing a failed print so the
// bed goes round again.
func (s *Server) prepareBatchForQueue(
	ctx context.Context, batch gen.Batch, actor string, origin sendOrigin,
) (gen.Batch, bool, error) {
	switch batch.Status {
	case production.BatchPendingApproval:
		approved, err := s.ApproveBatchFor(ctx, batch.ID, nil, actor)
		if err != nil {
			return batch, false, err
		}
		return approved, true, nil

	case production.BatchOpen:
		// A bed whose last print failed keeps its 'open' status and its
		// outcome. Clearing it is the deliberate human "run that again".
		if batch.PrintOutcome != nil {
			// And only a human's. The dispatcher must never retry a failed
			// plate into whatever went wrong the first time: clearing the
			// outcome here would do exactly that, one query change away from
			// ListBatchesToDispatch stopping filtering them out. It refuses
			// instead, so the bed waits for somebody to decide.
			if origin == automaticSend {
				return batch, false, statusErr(http.StatusConflict,
					"This bed's last print failed. Queue it by hand to run it again.")
			}
			if err := s.store.Q.ClearBatchPrintOutcome(ctx, batch.ID); err != nil {
				return batch, false, statusErrf(http.StatusInternalServerError,
					"Could not clear the previous print result.", err)
			}
			batch.PrintOutcome = nil
			batch.QueueItemID = nil
			batch.PipelineRunID = nil
		}
		// The check ApproveBatchFor runs at the moment of commitment, which an
		// already-locked bed never reaches again. It used not to matter: a
		// person locked a bed and sent it in the same breath. The dispatcher
		// sends on its own schedule, so a job held in the hours between is a
		// job that prints anyway unless it is caught here.
		if origin == automaticSend {
			jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
			if err != nil {
				return batch, false, statusErrf(http.StatusInternalServerError,
					"Could not read the batch's jobs.", err)
			}
			if why := unprintableJob(jobs); why != "" {
				return batch, false, statusErr(http.StatusConflict, why)
			}
		}
		return batch, false, nil

	case production.BatchInProgress:
		return batch, false, statusErr(http.StatusConflict, "This batch is already printing.")

	default:
		return batch, false, statusErr(http.StatusConflict, fmt.Sprintf(
			"A %s batch cannot be queued.", batch.Status))
	}
}

// plateSlots reads what the merged plate declares.
//
// Its own storage read rather than buffering the bytes for the upload too: the
// upload streams deliberately, and holding a plate per concurrent send is how
// the service falls over on a busy afternoon.
func (s *Server) plateSlots(ctx context.Context, plate gen.FileAsset) ([]meshio.Slot, error) {
	obj, err := s.storage.Get(ctx, plate.StorageKey)
	if err != nil {
		return nil, statusErr(http.StatusConflict,
			"This batch's plate is missing from storage. Re-approve the batch to rebuild it.")
	}
	defer func() { _ = obj.Body.Close() }()

	if obj.Size > maxPlateBytes {
		return nil, statusErr(http.StatusConflict, fmt.Sprintf(
			"This batch's plate is %d MB, which is too large to inspect.", obj.Size>>20))
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, maxPlateBytes))
	if err != nil {
		return nil, statusErrf(http.StatusInternalServerError, "Could not read this batch's plate.", err)
	}

	slots, err := meshio.ReadPlateSlots(data)
	if err != nil {
		return nil, statusErrf(http.StatusConflict,
			"This batch's plate could not be read. Rebuild the bed, then send it again.", err)
	}
	return slots, nil
}

// uploadPlate puts the unsliced plate in BambuBuddy's library.
func (s *Server) uploadPlate(
	ctx context.Context, batch gen.Batch, plate gen.FileAsset,
) (bambubuddy.UploadedFile, error) {
	obj, err := s.storage.Get(ctx, plate.StorageKey)
	if err != nil {
		const reason = "This batch's plate is missing from storage. Re-approve the batch to rebuild it."
		s.recordPrintError(ctx, batch.ID, reason)
		return bambubuddy.UploadedFile{}, statusErr(http.StatusConflict, reason)
	}
	defer func() { _ = obj.Body.Close() }()

	uploaded, err := s.bambu.UploadFile(ctx, plate.Filename, obj.Body)
	if err != nil {
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			s.recordPrintError(ctx, batch.ID, reason.Reason)
			return bambubuddy.UploadedFile{}, statusErr(http.StatusUnprocessableEntity, reason.Reason)
		}
		s.recordPrintError(ctx, batch.ID, "Could not send the plate to BambuBuddy.")
		return bambubuddy.UploadedFile{}, statusErr(http.StatusBadGateway,
			"Could not send the plate to BambuBuddy.")
	}
	return uploaded, nil
}

// pipelineForModel picks the slicer configuration for one printer model.
//
// The FIRST match only. sliceAndQueue's try-each-until-one-accepts is right when
// nobody named a machine; here the operator did, and falling through to another
// class's pipeline would slice the plate for the wrong printer.
func (s *Server) pipelineForModel(ctx context.Context, model string) (bambubuddy.Pipeline, error) {
	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		return bambubuddy.Pipeline{}, fmt.Errorf("could not read BambuBuddy's slicer pipelines")
	}
	eligible := pipelinesFor(pipelines, model)
	if len(eligible) == 0 {
		return bambubuddy.Pipeline{}, fmt.Errorf("%s", pipelineTargetNote(model))
	}
	chosen := eligible[0]
	if chosen.PrinterPreset == nil || chosen.ProcessPreset == nil {
		return bambubuddy.Pipeline{}, fmt.Errorf(
			"BambuBuddy's %q pipeline has no printer or process preset, so Tensor cannot slice with it",
			chosen.Name)
	}
	return chosen, nil
}

// filamentPresetOf is the filament preset to repeat per plate slot.
func filamentPresetOf(p bambubuddy.Pipeline) bambubuddy.PresetVal {
	if len(p.FilamentPresets) > 0 {
		return p.FilamentPresets[0]
	}
	return bambubuddy.PresetVal{}
}

// liveTraysFor reads one printer's AMS now, falling back to the mirrored row.
//
// One call for one printer, not a fleet sync: the send turns on which spools are
// in THIS machine at this moment, and the stored row is as old as the last sync.
func (s *Server) liveTraysFor(ctx context.Context, machine gen.Machine) []loadedTray {
	log := obs.FromContext(ctx)

	printerID, err := s.bambuCache.printerID(ctx, machine.MachineID, s.bambu.ListPrinters)
	if err == nil && printerID > 0 {
		status, err := s.bambu.GetStatus(ctx, printerID)
		if err == nil {
			// The fixed nozzle's spool rides along. It is not in the AMS
			// status - it is an external feed with no RFID - so a machine
			// rebuilt from the live read alone loses it, and the send then
			// refuses a bed the ranking had just accepted: "H2 no longer holds
			// this bed's colours: no spool has been confirmed as #FFFFFF",
			// for white that was physically loaded and declared.
			trays := decodeTrays(gen.Machine{
				Filaments:         filamentsJSON(status),
				FixedNozzleColour: machine.FixedNozzleColour,
				FixedNozzleIndex:  machine.FixedNozzleIndex,
			})
			if len(trays) > 0 {
				return trays
			}
		} else {
			log.Info("could not read the printer's trays live; using the last sync",
				"machine", machine.Name, "error", err)
		}
	}
	trays := decodeTrays(machine)
	sortTrays(trays)
	return trays
}

// colourIdentities is every colour the shop has confirmed, with the hexes its
// printers report for it.
func (s *Server) colourIdentities(ctx context.Context) ([]colourIdentity, error) {
	rows, err := s.store.Q.ListColourMap(ctx)
	if err != nil {
		return nil, err
	}
	// ListColourMap is ordered by name with the primary first, so appending in
	// order keeps each identity's primary hex at the front.
	byName := map[string]*colourIdentity{}
	out := make([]colourIdentity, 0, len(rows))
	for _, r := range rows {
		hex, ok := normaliseHex(r.Hex)
		if !ok {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(r.ColourName))
		if existing, seen := byName[key]; seen {
			existing.Hexes = append(existing.Hexes, hex)
			continue
		}
		out = append(out, colourIdentity{Name: key, Hexes: []string{hex}})
		byName[key] = &out[len(out)-1]
	}
	return out, nil
}

// nozzleMapOverrides pins each plate slot to a nozzle on a two-nozzle machine.
//
// Without it Bambu Studio decides for itself, in "Auto For Flush" mode, and on
// this fleet it decided to put everything on one nozzle: the H2C plate read
// filament_map ["1","1","1"] however the machine was loaded. That is a
// single-nozzle print on a two-nozzle printer - the second extruder, and the
// spool on it, simply unused.
//
// The map is 1-based over EXTRUDERS, not slots: entry i is the nozzle that
// prints the plate's filament i. A slot bound to the external spool prints on
// the fixed nozzle; everything else prints on the other one.
//
// Nil for a single-nozzle machine, which is every A2L and P2S here. Sending a
// map to a printer with one extruder would be describing a machine that does
// not exist.
func nozzleMapOverrides(
	machine gen.Machine, assignments []slotAssignment, physicalMap []int,
) map[string]any {
	if machine.FixedNozzleIndex == nil || len(assignments) == 0 {
		return nil
	}
	// fixed_nozzle_index is a PHYSICAL index - it is synced from the feed the
	// printer reports as ams_id 254. filament_map wants a LOGICAL one, and on
	// an H2C physical_extruder_map is [1,0], so the two are swapped rather than
	// offset by one. Adding one, which this used to do, named the opposite
	// nozzle: every plate told the slicer to print the external spool's colour
	// from the AMS nozzle while ams_mapping said it came off the external
	// spool. Bambu Studio's own output for these machines puts the external
	// colour on 1 and the AMS colour on 2.
	fixed, ok := bambubuddy.LogicalExtruder(int(*machine.FixedNozzleIndex), physicalMap)
	if !ok {
		// The preset does not place that nozzle. Pinning a guess would print
		// every colour from the wrong one, so leave the map alone and let the
		// slicer arrange it - which is what an unmapped bed already does.
		return nil
	}
	// The other nozzle of the two. Two is all an H2C has, so "not the fixed
	// one" names it without needing to be told how many there are.
	other := 1
	if fixed == 1 {
		other = 2
	}

	mapping := make([]string, 0, len(assignments))
	external := false
	for _, a := range assignments {
		// The external feed, which is 254 - its own vt_tray id - not -1. A
		// slot carrying -1 is one no tray serves at all, and must not be
		// pinned to the fixed nozzle as though it were the spool on the back.
		if a.AmsIndex == amsExternalSpool {
			mapping = append(mapping, strconv.Itoa(fixed))
			external = true
			continue
		}
		mapping = append(mapping, strconv.Itoa(other))
	}
	// Nothing on this plate comes off the fixed spool, so there is nothing to
	// pin: let the slicer arrange the AMS colours as it likes rather than
	// forcing them all onto one nozzle and telling it that was deliberate.
	if !external {
		return nil
	}
	out := map[string]any{
		"filament_map_mode": "Manual",
		"filament_map":      mapping,
	}
	if topology, ok := amsTopologyFor(fixed, physicalMap); ok {
		out["extruder_ams_count"] = topology
	}
	for k, v := range primeTowerOverrides(assignments) {
		out[k] = v
	}
	return out
}

// amsTopologyFor is extruder_ams_count: what feeds each nozzle.
//
// BambuBuddy's H2C printer preset carries NO ams keys at all - 123 settings and
// not one of them - so its slices declare ["1#0|4#0","1#0|4#0"]: no AMS on
// either extruder. Bambu Studio, synced to the same printer, writes
// ["1#1|4#0","1#0|4#1"]. A file that says the machine has no AMS leaves the
// printer nothing to build a mapping table from, which is what
// "[0700-8012] Failed to get AMS mapping table" reports.
//
// Indexed by LOGICAL extruder, the same numbering filament_map uses, so the
// fixed nozzle's own index takes the single-spool holder and the other takes
// the four-slot AMS. On an H2C that reproduces Studio's value exactly.
//
// Two nozzles only. A single-extruder preset states a one-entry map, its AMS
// topology is whatever its own preset already says, and inventing a second
// extruder for it would describe a machine that does not exist.
func amsTopologyFor(fixedLogical int, physicalMap []int) ([]string, bool) {
	if len(physicalMap) != 2 || fixedLogical < 1 || fixedLogical > 2 {
		return nil, false
	}
	const (
		spoolHolder = "1#1|4#0" // the external feed: one single-spool unit
		fourSlotAMS = "1#0|4#1" // the AMS proper
	)
	out := make([]string, 2)
	out[fixedLogical-1] = spoolHolder
	out[2-fixedLogical] = fourSlotAMS
	return out, true
}

// physicalExtruderMap reads how this pipeline's printer numbers its extruders.
//
// Read from the preset rather than assumed, because the answer differs per
// machine: an H2C reports [1,0] and a single-nozzle A2L or P2S reports nothing
// at all. Nil on any failure, which LogicalExtruder treats as "one nozzle, the
// numberings agree" - the behaviour every single-nozzle bed already relies on.
func (s *Server) physicalExtruderMap(ctx context.Context, pipeline bambubuddy.Pipeline) []int {
	if pipeline.PrinterPreset == nil {
		return nil
	}
	preset, err := s.bambu.GetPrinterPreset(ctx, *pipeline.PrinterPreset)
	if err != nil {
		obs.FromContext(ctx).Info("could not read the printer preset's extruder map",
			"pipeline", pipeline.Name, "error", err)
		return nil
	}
	return preset.PhysicalExtruderMap()
}

// primeTowerOverrides asks for the prime tower on a plate that changes colour.
//
// The tower is where a nozzle purges the colour before it, so without one the
// first millimetres after every change print in the previous colour. Bambu
// Studio's own slice of this plate carries "FEATURE: Prime tower"; BambuBuddy's
// carries none.
//
// BAMBUBUDDY CURRENTLY IGNORES THIS, and the override is sent anyway so the
// intent is recorded and starts working the day that changes. Measured against
// the live service: slicing one file with
// process_overrides {"enable_prime_tower":"1","sparse_infill_density":"42%"}
// came back with sparse_infill_density "42%" applied and enable_prime_tower
// "0" - then listed enable_prime_tower in different_settings_to_system as a
// "designer change", though neither the source 3MF nor the process preset says
// 0. Both say 1. So it is BambuBuddy forcing it off, over its own preset and
// over an explicit override; nothing Tensor sends can turn it on today.
//
// Only for a plate that actually changes colour. A single-colour bed never
// purges, so a tower there is plastic and minutes for nothing.
//
// The space is already reserved: bedpack holds back WipeTowerMM on every plate
// it packs, and the process preset's own prime_tower_width is 60 - the same
// number - so this costs no plate area that is not already set aside.
func primeTowerOverrides(assignments []slotAssignment) map[string]any {
	seen := map[string]bool{}
	for _, a := range assignments {
		seen[a.TrayHex] = true
	}
	if len(seen) < 2 {
		return nil
	}
	return map[string]any{"enable_prime_tower": "1"}
}

// pipelineForBed picks the slicer configuration for THIS bed on this model.
//
// The settings used to come from the printer model alone, so a Dual Name Plank
// and a heart keychain sent to the same H2C were sliced identically - and with
// two H2C pipelines on the floor, which one they got was whichever BambuBuddy
// listed first. A SKU can now name its own, per class.
//
// An unmapped SKU falls back to exactly that model-based pick, so nothing
// changes for a product nobody has configured.
func (s *Server) pipelineForBed(
	ctx context.Context, jobs []gen.ProductionJob, model string,
) (bambubuddy.Pipeline, error) {
	mapped, err := s.mappedPipelineFor(ctx, jobs, model)
	if err != nil {
		return bambubuddy.Pipeline{}, err
	}
	if mapped == nil {
		return s.pipelineForModel(ctx, model)
	}

	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		return bambubuddy.Pipeline{}, fmt.Errorf("could not read BambuBuddy's slicer pipelines")
	}
	for _, p := range pipelines {
		if int32(p.ID) != mapped.PipelineID {
			continue
		}
		if p.PrinterPreset == nil || p.ProcessPreset == nil {
			return bambubuddy.Pipeline{}, fmt.Errorf(
				"BambuBuddy's %q pipeline has no printer or process preset, so Tensor cannot slice with it",
				p.Name)
		}
		return p, nil
	}
	// Refused, not fallen back. Falling back would slice this bed with another
	// product's settings and say nothing - the plank would print as a keychain
	// and the only evidence would be the plate.
	return bambubuddy.Pipeline{}, fmt.Errorf(
		"this bed's SKU is set to slice with the %q pipeline, which BambuBuddy no longer has. "+
			"Re-map the SKU under Registry, or add the pipeline back",
		mapped.PipelineName)
}

// mappedPipelineFor is the pipeline this bed slices with, or nil when no job
// on it has a mapping.
//
// A bed can now legitimately hold SKUs mapped to different pipelines. Batching
// is by COLOUR - the SKU stopped deciding what shares a plate, because two gold
// Dual Name Planks differing only by whether an LED ships in the box were going
// to separate beds and neither reached the three-unit floor.
//
// This used to refuse such a bed outright, on the reasoning that picking one of
// two answers would slice half the plate wrong. That reasoning was sound while
// the planner guaranteed the case could not arise; with the SKU out of the
// grouping key it arises by design, and refusing would be worse than choosing -
// the bed would be planned, locked, have its filament reserved and its plate
// built, then fail at the last step with nothing an operator could do but take
// it apart by hand.
//
// So it chooses, by UNITS rather than by jobs: the pipeline most of the plate
// actually asks for wins, and a single odd plank rides along on its neighbours'
// settings rather than dictating to them. Ties go to the earliest job, which is
// oldest-order-first, so the answer is stable across passes and does not depend
// on map iteration. Disagreement is logged with both names and recorded on the
// bed, because "this plate was sliced with the other product's process" is
// exactly the kind of thing that must not be silent.
func (s *Server) mappedPipelineFor(
	ctx context.Context, jobs []gen.ProductionJob, model string,
) (*gen.SkuSlicerPipeline, error) {
	type tally struct {
		row   gen.SkuSlicerPipeline
		units int
		first int
	}
	byPipeline := map[int32]*tally{}

	for i, j := range jobs {
		sku := strings.TrimSpace(deref(j.Sku))
		if sku == "" {
			continue
		}
		row, err := s.store.Q.GetPipelineForSKU(ctx, gen.GetPipelineForSKUParams{
			Sku: sku, MachineFamily: model,
		})
		if err != nil {
			continue // no mapping for this SKU on this class
		}
		t, seen := byPipeline[row.PipelineID]
		if !seen {
			byPipeline[row.PipelineID] = &tally{row: row, units: int(jobQuantity(j.Quantity)), first: i}
			continue
		}
		t.units += int(jobQuantity(j.Quantity))
	}

	if len(byPipeline) == 0 {
		return nil, nil
	}

	var best *tally
	for _, t := range byPipeline {
		switch {
		case best == nil:
		case t.units > best.units:
		case t.units == best.units && t.first < best.first:
		default:
			continue
		}
		best = t
	}

	if len(byPipeline) > 1 {
		others := make([]string, 0, len(byPipeline)-1)
		for id, t := range byPipeline {
			if id != best.row.PipelineID {
				others = append(others, fmt.Sprintf("%s (%d units)", t.row.PipelineName, t.units))
			}
		}
		sort.Strings(others)
		obs.FromContext(ctx).Warn("this bed's SKUs are mapped to different pipelines; slicing it with the one most of the plate asks for",
			"chosen", best.row.PipelineName, "chosen_units", best.units, "others", others)
	}

	return &best.row, nil
}

// replateForMachine lays a bed's plate out for the class it is being sent to.
//
// A no-op when they already agree, which is every bed whose class the planner
// happened to guess right and every single-nozzle machine. Only the H2C can
// disagree in a way that matters, because only it has a usable area that does
// not start at the bed's origin.
//
// A failure here is returned rather than swallowed: sending the old plate would
// slice, upload, and fail on the printer minutes later with an error naming
// geometry rather than the bed it came from.
func (s *Server) replateForMachine(
	ctx context.Context, batch gen.Batch, machine gen.Machine,
) (gen.Batch, error) {
	target := strings.TrimSpace(deref(machine.Model))
	if target == "" {
		return batch, nil
	}
	if _, known := bedpack.BedForKnownFamily(target); !known {
		return batch, nil
	}
	if strings.EqualFold(strings.TrimSpace(deref(batch.MachineFamily)), target) {
		return batch, nil
	}

	log := obs.FromContext(ctx)
	log.Info("re-plating a bed for the machine it is going to",
		"batch", batch.BatchNumber, "packed_for", deref(batch.MachineFamily),
		"machine", machine.Name, "class", target)

	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return batch, statusErr(http.StatusInternalServerError, "Could not read the batch's jobs.")
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber,
		bedpack.BedForFamily(target))
	if herr != nil {
		return batch, statusErr(herr.status, herr.msg)
	}
	fileID, err := s.storePlateSystem(ctx, batch.ID, "preview", plate)
	if err != nil {
		return batch, err
	}

	units := int32(unitsOf(jobs))
	if _, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{
		ID: batch.ID, UnitsPerBed: &units,
		PreviewFileID:         &fileID,
		BedUtilizationPercent: &plate.utilisation,
		MachineFamily:         &target,
	}); err != nil {
		return batch, statusErr(http.StatusInternalServerError,
			"Could not record the re-laid plate.")
	}
	refreshed, err := s.store.Q.GetBatchByID(ctx, batch.ID)
	if err != nil {
		return batch, statusErr(http.StatusInternalServerError, "Could not reload the batch.")
	}
	return refreshed, nil
}
