package slicing

// The River worker that consumes SliceArgs jobs: download the STL, slice it
// headless with Bambu Studio, parse the output into per-unit metrics, upload the
// G-code, and price the design. This is the in-process Go replacement for the
// Python Celery task (slicer-worker/app/tasks.py) - same flow, no HTTP callback.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const (
	stlName          = "model.stl"
	gcodeMimeType    = "application/octet-stream"
	defaultInfillPct = 15.0
)

// SliceWorker slices one design per job. It holds only immutable dependencies, so
// River can run SliceConcurrency copies of Work concurrently.
type SliceWorker struct {
	river.WorkerDefaults[SliceArgs]

	store             *db.Store
	objects           *storage.Client
	bambuRoot         string
	sliceTimeout      time.Duration
	printerAvgPowerKW float64
	logger            *slog.Logger
}

// NewSliceWorker builds the worker. logger may be nil (falls back to slog default).
func NewSliceWorker(
	store *db.Store, objects *storage.Client, bambuRoot string,
	sliceTimeout time.Duration, printerAvgPowerKW float64, logger *slog.Logger,
) *SliceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SliceWorker{
		store: store, objects: objects, bambuRoot: bambuRoot,
		sliceTimeout: sliceTimeout, printerAvgPowerKW: printerAvgPowerKW, logger: logger,
	}
}

// Work runs one slice job. Any returned error tells River to retry with backoff;
// on the final attempt we also mark the domain job and design failed so a design
// never sits stuck in "slicing". A nil return closes the job and prices the design.
func (w *SliceWorker) Work(ctx context.Context, job *river.Job[SliceArgs]) error {
	args := job.Args
	w.logger.Info("slice start", "job", args.JobID, "design", args.DesignID, "attempt", job.Attempt)

	if err := MarkSlicing(ctx, w.store, args.DesignID); err != nil {
		return fmt.Errorf("mark slicing: %w", err)
	}

	metrics, err := w.slice(ctx, args)
	if err != nil {
		w.logger.Error("slice failed", "job", args.JobID, "attempt", job.Attempt, "error", err)
		// River discards the job after the final attempt; mirror that in our own
		// records so the design shows as failed to the user.
		if job.Attempt >= job.MaxAttempts {
			FailJob(ctx, w.store, args.JobID, err.Error())
		}
		return err
	}

	if err := ProcessSliceResult(ctx, w.store, args.JobID, args.DesignID, args.BrandSlug, metrics); err != nil {
		return fmt.Errorf("process slice result: %w", err)
	}
	w.logger.Info("slice done", "job", args.JobID, "design", args.DesignID)
	return nil
}

// slice performs the STL-in, per-unit-metrics-out pipeline for one job. It works
// entirely within a temp directory that is removed when it returns.
func (w *SliceWorker) slice(ctx context.Context, args SliceArgs) (PerUnitMetrics, error) {
	workdir, err := os.MkdirTemp("", "slice-*")
	if err != nil {
		return PerUnitMetrics{}, fmt.Errorf("create workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	stlPath := filepath.Join(workdir, stlName)
	if err := w.objects.Download(ctx, args.StlKey, stlPath); err != nil {
		return PerUnitMetrics{}, fmt.Errorf("download stl: %w", err)
	}

	profiles, err := ResolveProfiles(w.bambuRoot, args.Material, args.Quality)
	if err != nil {
		return PerUnitMetrics{}, err
	}

	units := args.UnitsPerBed
	if units < 1 {
		units = 1
	}
	infill := args.InfillPct
	if infill <= 0 {
		infill = defaultInfillPct
	}

	out, err := RunSlice(ctx, w.bambuRoot, profiles, stlPath, infill, units, workdir, w.sliceTimeout)
	if err != nil {
		return PerUnitMetrics{}, err
	}

	result, err := LoadResultJSON(out.ResultJSONPath)
	if err != nil {
		return PerUnitMetrics{}, fmt.Errorf("read result.json: %w", err)
	}
	sliceInfo, err := LoadSliceInfo(out.Gcode3mfPath)
	if err != nil {
		return PerUnitMetrics{}, fmt.Errorf("read slice_info: %w", err)
	}
	plateGcode, err := LoadPlateGcode(out.Gcode3mfPath)
	if err != nil {
		return PerUnitMetrics{}, fmt.Errorf("read plate gcode: %w", err)
	}
	metrics, err := ExtractMetrics(result, sliceInfo, profiles.DensityGCm3, plateGcode)
	if err != nil {
		return PerUnitMetrics{}, err
	}

	gcodeKey := fmt.Sprintf("gcode/%s.gcode.3mf", args.JobID)
	if err := w.objects.Upload(ctx, gcodeKey, out.Gcode3mfPath, gcodeMimeType); err != nil {
		return PerUnitMetrics{}, fmt.Errorf("upload gcode: %w", err)
	}
	return ToPerUnit(metrics, units, w.printerAvgPowerKW, gcodeKey), nil
}
