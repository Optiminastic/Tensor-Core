package httpapi

// Dispatch for a machine class Tensor slices itself (TENSOR_SLICE_FAMILIES).
//
// Everything up to this point is shared with the BambuBuddy path: the machine
// is chosen, every plate slot is bound to a live tray, and the nozzle map is
// computed for this machine. Only the last step differs. Instead of uploading
// the unsliced plate and asking BambuBuddy's slicer for a file, the bed is
// handed to Tensor's slice worker with the SKU's slicing profile; the worker
// uploads the finished .gcode.3mf and the same queueing step as today puts it
// on the printer.
//
// The bed is marked as on its way with bambu_slice_job_id = 0. Every guard
// against sending a bed twice, the in-flight count per printer and the
// "Slicing" stage already read that column as "a slice is running"; a second
// marker would have to be taught to all of them, and the one it was not taught
// to would print a bed twice. Zero is never a BambuBuddy job id, and nothing
// reads the value - the queueing step gets the file from its own arguments.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// tensorSliceMarker is the bambu_slice_job_id a Tensor slice sets.
const tensorSliceMarker int32 = 0

type tensorSend struct {
	Batch       gen.Batch
	Machine     gen.Machine
	Pipeline    bambubuddy.Pipeline
	Plate       gen.FileAsset
	Assignments []slotAssignment
	NozzleMap   map[string]any
	PrinterID   int
}

// slicesWithTensor reports whether this machine's class is switched to
// Tensor's own slicer.
func (s *Server) slicesWithTensor(machine gen.Machine) bool {
	family := strings.ToUpper(strings.TrimSpace(deref(machine.Model)))
	return family != "" && slices.ContainsFunc(s.cfg.TensorSliceFamilies, func(f string) bool {
		return strings.EqualFold(strings.TrimSpace(f), family)
	})
}

// sendForTensorSlice marks the bed and enqueues its print slice, together or
// not at all - the same rule the BambuBuddy path keeps for its marker and its
// queueing job.
func (s *Server) sendForTensorSlice(ctx context.Context, t tensorSend, out queueBatchResponse) (queueBatchResponse, error) {
	profile, err := s.store.Q.GetSlicingProfileForPipeline(ctx, int32(t.Pipeline.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		why := fmt.Sprintf("This bed slices with the %q pipeline, which has no slicing profile in Tensor. "+
			"Import the profiles under Registry, Slicing, then send it again.", t.Pipeline.Name)
		s.recordPrintError(ctx, t.Batch.ID, why)
		return out, statusErr(http.StatusConflict, why)
	}
	if err != nil {
		return out, statusErr(http.StatusInternalServerError, "Could not read this bed's slicing profile.")
	}
	if s.enqueuer == nil {
		return out, statusErr(http.StatusServiceUnavailable,
			"Tensor's slicer is not available in this process, so the bed was not sent.")
	}
	args := tensorSliceArgs(t, profile)
	marker := tensorSliceMarker
	err = s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		if err := q.SetBatchSliceJob(ctx, gen.SetBatchSliceJobParams{
			ID: t.Batch.ID, BambuSliceJobID: &marker, FleetMachineID: &t.Machine.ID,
		}); err != nil {
			return err
		}
		return s.enqueuer.EnqueuePrintTx(ctx, tx, args)
	})
	if err != nil {
		s.recordPrintError(ctx, t.Batch.ID, "Could not hand this bed to Tensor's slicer. It will be sent again.")
		return out, statusErr(http.StatusInternalServerError, "Could not hand this bed to Tensor's slicer.")
	}
	out.Queued, out.Pinned = true, true
	out.Note = fmt.Sprintf("Tensor is slicing for %s now, then it goes on that printer", t.Machine.Name)
	obs.FromContext(ctx).Info("bed sent to Tensor's slicer against a chosen printer",
		"batch", t.Batch.BatchNumber, "machine", t.Machine.Name, "printer", t.PrinterID,
		"profile", profile.Name, "profile_version", profile.Version,
		"colours", args.FilamentColours, "ams_mapping", args.AmsMapping)
	return out, nil
}

func tensorSliceArgs(t tensorSend, profile gen.SlicingProfile) slicing.SlicePrintArgs {
	return slicing.SlicePrintArgs{
		BatchID: t.Batch.ID, BatchNumber: t.Batch.BatchNumber,
		ProfileID: profile.ID, ProfileVersion: int(profile.Version),
		PlateKey: t.Plate.StorageKey, PlateFilename: t.Plate.Filename,
		FilamentColours: trayColoursOf(t.Assignments), NozzleOverrides: t.NozzleMap,
		PrinterID: t.PrinterID, MachineID: t.Machine.ID, MachineName: t.Machine.Name,
		AmsMapping: amsMappingOf(t.Assignments), TrayHexes: trayColoursOf(t.Assignments),
	}
}
