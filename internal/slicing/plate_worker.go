package slicing

// The River worker that consumes PlateSliceArgs: download a batch's merged plate
// STL, slice it headless with Bambu Studio, and upload the printable G-code.
//
// Deliberately much thinner than SliceWorker. A design slice exists to produce
// COST (per-unit metrics that feed pricing); a plate slice exists to produce an
// ARTEFACT the printer can consume. There is no Design CP for a plate, so nothing
// here parses metrics or prices anything - the batch's cost snapshot was already
// computed from its jobs at approval time.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const plateStlName = "plate.stl"

// PlateGcodeKey is the object key a batch's sliced plate is stored under. Kept
// beside the design G-code convention (gcode/<id>.gcode.3mf) but namespaced, so
// design and batch artefacts never collide.
func PlateGcodeKey(batchID uuid.UUID) string {
	return fmt.Sprintf("gcode/batches/%s.gcode.3mf", batchID)
}

// PlateSliceWorker slices one batch plate per job.
type PlateSliceWorker struct {
	river.WorkerDefaults[PlateSliceArgs]

	store        *db.Store
	objects      *storage.Client
	bambuRoot    string
	sliceTimeout time.Duration
	logger       *slog.Logger
	// dispatcher schedules the hand-off to the printer once the plate exists. Nil
	// leaves the plate sliced but unsent, which is the correct behaviour when
	// printing is not configured.
	dispatcher *production.DispatchEnqueuer
}

// EnableDispatch makes a finished plate schedule its own print. Kept separate
// from the constructor so the worker still builds when printing is not wired up.
func (w *PlateSliceWorker) EnableDispatch(d *production.DispatchEnqueuer) {
	w.dispatcher = d
}

// NewPlateSliceWorker builds the worker. logger may be nil (falls back to slog's
// default).
func NewPlateSliceWorker(
	store *db.Store, objects *storage.Client, bambuRoot string,
	sliceTimeout time.Duration, logger *slog.Logger,
) *PlateSliceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &PlateSliceWorker{
		store: store, objects: objects, bambuRoot: bambuRoot,
		sliceTimeout: sliceTimeout, logger: logger,
	}
}

// Work slices one batch plate. Any returned error tells River to retry with
// backoff; on the final attempt the batch is marked failed with the reason, so a
// batch never sits silently without a printable file.
func (w *PlateSliceWorker) Work(ctx context.Context, job *river.Job[PlateSliceArgs]) error {
	args := job.Args
	w.logger.Info("plate slice start", "batch", args.BatchID, "attempt", job.Attempt)

	if err := w.store.Q.MarkBatchSlicing(ctx, args.BatchID); err != nil {
		return fmt.Errorf("mark batch slicing: %w", err)
	}

	key, err := w.slice(ctx, args)
	if err != nil {
		w.logger.Error("plate slice failed", "batch", args.BatchID, "attempt", job.Attempt, "error", err)
		if job.Attempt >= job.MaxAttempts {
			reason := err.Error()
			if failErr := w.store.Q.FailBatchSlice(ctx, gen.FailBatchSliceParams{
				ID: args.BatchID, SliceError: &reason,
			}); failErr != nil {
				w.logger.Error("could not record plate slice failure", "batch", args.BatchID, "error", failErr)
			}
		}
		return err
	}

	// Record the plate and schedule its print in ONE transaction, so a batch can
	// never end up with a printable file and nothing scheduled to send it.
	if err := w.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		if _, err := q.SetBatchGcode(ctx, gen.SetBatchGcodeParams{
			ID: args.BatchID, GcodeKey: &key,
		}); err != nil {
			return err
		}
		if w.dispatcher == nil {
			return nil // Printing not configured; the plate is still recorded.
		}
		return w.dispatcher.EnqueueTx(ctx, tx, production.DispatchPrintArgs{BatchID: args.BatchID})
	}); err != nil {
		return fmt.Errorf("record plate gcode: %w", err)
	}
	w.logger.Info("plate slice done", "batch", args.BatchID, "gcode_key", key)
	return nil
}

// slice downloads the plate, slices it, and uploads the G-code, returning its
// object key. Everything happens inside a temp directory that is removed on exit.
func (w *PlateSliceWorker) slice(ctx context.Context, args PlateSliceArgs) (string, error) {
	if w.objects == nil {
		return "", fmt.Errorf("object storage is not configured")
	}
	workdir, err := os.MkdirTemp("", "plate-slice-*")
	if err != nil {
		return "", fmt.Errorf("create workdir: %w", err)
	}
	defer os.RemoveAll(workdir)

	stlPath := filepath.Join(workdir, plateStlName)
	if err := w.objects.Download(ctx, args.StlKey, stlPath); err != nil {
		return "", fmt.Errorf("download plate %s: %w", args.StlKey, err)
	}

	profiles, err := w.resolveProfiles(ctx, args)
	if err != nil {
		return "", err
	}

	infill := args.InfillPct
	if infill <= 0 {
		infill = defaultInfillPct
	}
	out, err := RunPlateSlice(ctx, w.bambuRoot, profiles, stlPath, infill, args.Settings, workdir, w.sliceTimeout)
	if err != nil {
		return "", err
	}

	key := PlateGcodeKey(args.BatchID)
	if err := w.objects.Upload(ctx, key, out.Gcode3mfPath, gcodeMimeType); err != nil {
		return "", fmt.Errorf("upload plate gcode: %w", err)
	}
	return key, nil
}

// resolveProfiles mirrors SliceWorker.resolveProfiles: machine-driven when the
// batch has a machine, otherwise the legacy fixed profile from material+quality.
func (w *PlateSliceWorker) resolveProfiles(ctx context.Context, args PlateSliceArgs) (ResolvedProfiles, error) {
	if args.MachineID == nil {
		return ResolveProfiles(w.bambuRoot, args.Material, args.Quality)
	}
	cfg, err := w.store.Q.GetMachineConfig(ctx, *args.MachineID)
	if err != nil {
		return ResolvedProfiles{}, fmt.Errorf("load machine config: %w", err)
	}
	preset, density, err := pickFilament(cfg.SupportedFilaments, args.FilamentPreset, args.Material)
	if err != nil {
		return ResolvedProfiles{}, err
	}
	layerHeight := 0.20
	if args.Settings.LayerHeightMM != nil {
		layerHeight = *args.Settings.LayerHeightMM
	}
	return ResolveMachineProfiles(w.bambuRoot, cfg.Family, cfg.NozzleMm, preset, density, layerHeight)
}
