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
	"strings"

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

// sendBatchToMachine slices a bed for one printer and schedules its queueing.
//
// Returns before the slice finishes - that takes minutes - so the response says
// the plate is on its way rather than claiming it is queued.
func (s *Server) sendBatchToMachine(
	ctx context.Context, batch gen.Batch, machine gen.Machine, slotTrays []int, actor string,
) (queueBatchResponse, error) {
	log := obs.FromContext(ctx)
	out := queueBatchResponse{BatchNumber: batch.BatchNumber, MachineName: machine.Name}

	batch, locked, err := s.prepareBatchForQueue(ctx, batch, actor)
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

	// The operator's own answer, checked rather than recomputed. Tensor knows
	// which tray holds which hex; only the person at the machine knows which of
	// those hexes is the blue this order meant.
	assignments, err := assignmentsFromChoice(slots, trays, slotTrays)
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

	pipeline, err := s.pipelineForModel(ctx, deref(machine.Model))
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

	sliceJobID := int32(job.JobID)
	if err := s.store.Q.SetBatchSliceJob(ctx, gen.SetBatchSliceJobParams{
		ID: batch.ID, BambuSliceJobID: &sliceJobID,
		// The physical printer, not the profile. Until a queue item exists this
		// row is the only record that this bed is on its way to this unit -
		// which is what keeps the next bed from being ranked against a fleet
		// that still looks idle.
		FleetMachineID: &machine.ID,
	}); err != nil {
		log.Warn("could not record the slice job", "batch", batch.BatchNumber, "error", err)
	}

	if err := s.sliceQueueEnqueuer.Enqueue(ctx, production.QueueSlicedPlateArgs{
		BatchID: batch.ID, SliceJobID: job.JobID, PrinterID: printerID,
		MachineID: machine.ID, MachineName: machine.Name,
		AmsMapping: amsMappingOf(assignments), TrayHexes: trayColoursOf(assignments),
	}); err != nil {
		// The slice is running either way; what is lost is the queueing that
		// follows it. Say so rather than reporting a clean success.
		log.Error("could not schedule the queueing that follows the slice",
			"batch", batch.BatchNumber, "error", err)
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

// prepareBatchForQueue locks a Draft and reports whether it did.
//
// Shared with the automatic path so the status rules live in one place: a Draft
// is locked, never sent as a Draft, because the next planning pass can dissolve
// one and rebuild it from different jobs.
func (s *Server) prepareBatchForQueue(
	ctx context.Context, batch gen.Batch, actor string,
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
			if err := s.store.Q.ClearBatchPrintOutcome(ctx, batch.ID); err != nil {
				return batch, false, statusErrf(http.StatusInternalServerError,
					"Could not clear the previous print result.", err)
			}
			batch.PrintOutcome = nil
			batch.QueueItemID = nil
			batch.PipelineRunID = nil
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
			trays := decodeTrays(gen.Machine{Filaments: filamentsJSON(status)})
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
