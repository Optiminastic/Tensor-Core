package slicing

// The River worker that consumes SliceArgs jobs: download the STL, slice it
// headless with Bambu Studio, parse the output into per-unit metrics, upload the
// G-code, and price the design. This is the in-process Go replacement for the
// Python Celery task (slicer-worker/app/tasks.py) - same flow, no HTTP callback.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
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
	orientOpts        orientation.Options
	logger            *slog.Logger
}

// NewSliceWorker builds the worker. logger may be nil (falls back to slog default).
func NewSliceWorker(
	store *db.Store, objects *storage.Client, bambuRoot string,
	sliceTimeout time.Duration, printerAvgPowerKW float64,
	orientOpts orientation.Options, logger *slog.Logger,
) *SliceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &SliceWorker{
		store: store, objects: objects, bambuRoot: bambuRoot,
		sliceTimeout: sliceTimeout, printerAvgPowerKW: printerAvgPowerKW,
		orientOpts: orientOpts, logger: logger,
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

	// Advisory orientation analysis from the mesh. Never fails the slice: a parse
	// error or unsupported format (STEP) just leaves the recommendation absent.
	orient := w.recommendOrientation(stlPath, args.StlKey)

	profiles, err := w.resolveProfiles(ctx, args)
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

	out, err := RunSlice(ctx, w.bambuRoot, profiles, stlPath, infill, units, args.Settings, workdir, w.sliceTimeout)
	if err != nil {
		// Bambu Studio's CLI rejects some 3MFs it cannot parse (e.g. a foreign
		// PrusaSlicer/Slic3r project 3MF), even though our own mesh loader reads
		// them. Rebuild a clean STL from the mesh and slice that: geometry and cost
		// are preserved; the slice is single-material (fine on the single-nozzle H2S).
		fallback, ok := w.fallbackSTL(stlPath, args.StlKey, workdir)
		if !ok {
			return PerUnitMetrics{}, err
		}
		w.logger.Info("retrying slice via normalised STL", "key", args.StlKey, "reason", err)
		out, err = RunSlice(ctx, w.bambuRoot, profiles, fallback, infill, units, args.Settings, workdir, w.sliceTimeout)
		if err != nil {
			return PerUnitMetrics{}, err
		}
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
	perUnit := ToPerUnit(metrics, units, w.printerAvgPowerKW, gcodeKey)
	perUnit.Orientation = orient
	return perUnit, nil
}

// machineFilament is one entry of a machine's supported_filaments jsonb.
type machineFilament struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
	IsDefault      bool    `json:"is_default"`
}

// resolveProfiles picks the Bambu profiles for a slice: machine-driven when the
// job carries a MachineID (the machine's family/nozzle/filament + the requested
// layer height), else the legacy fixed H2S 0.4 profile so old designs still slice.
func (w *SliceWorker) resolveProfiles(ctx context.Context, args SliceArgs) (ResolvedProfiles, error) {
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

// pickFilament chooses which of a machine's filaments to slice with: the explicit
// chosen preset if given, else the one whose material matches the design's, else
// the machine's default (or first).
func pickFilament(raw []byte, chosenPreset, material string) (string, float64, error) {
	var options []machineFilament
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &options)
	}
	if len(options) == 0 {
		return "", 0, fmt.Errorf("machine has no supported filaments")
	}
	if chosenPreset != "" {
		for _, f := range options {
			if f.FilamentPreset == chosenPreset {
				return f.FilamentPreset, f.Density, nil
			}
		}
		return "", 0, fmt.Errorf("machine does not support filament %q", chosenPreset)
	}
	material = strings.ToUpper(strings.TrimSpace(material))
	fallback := options[0]
	for _, f := range options {
		if f.IsDefault {
			fallback = f
		}
		if material != "" && strings.HasPrefix(strings.ToUpper(f.Material), material) {
			return f.FilamentPreset, f.Density, nil
		}
	}
	return fallback.FilamentPreset, fallback.Density, nil
}

// fallbackSTL rebuilds a clean binary STL from a 3MF the slicer could not parse,
// to recover foreign/complex 3MFs (e.g. PrusaSlicer projects). It returns false
// when the model is not a 3MF or its mesh cannot be read, so the caller keeps the
// original slice failure. The rebuilt STL is geometry only - correct for costing,
// single-material on the single-nozzle H2S.
func (w *SliceWorker) fallbackSTL(modelPath, stlKey, workdir string) (string, bool) {
	if !strings.EqualFold(filepath.Ext(stlKey), ".3mf") {
		return "", false
	}
	mesh, err := orientation.LoadModel(modelPath, filepath.Ext(stlKey))
	if err != nil || len(mesh.Triangles) == 0 {
		return "", false
	}
	outPath := filepath.Join(workdir, "model-normalised.stl")
	if err := os.WriteFile(outPath, meshio.ConcatBinarySTL("tensor", mesh.Triangles), 0o600); err != nil {
		return "", false
	}
	return outPath, true
}

// recommendOrientation reads the model mesh and computes the least-support
// resting orientation. It is best-effort: any failure (parse error, unsupported
// format) returns nil so the slice and costing proceed unaffected. The model's
// true extension comes from the storage key, not the temp file name.
func (w *SliceWorker) recommendOrientation(modelPath, stlKey string) *orientation.Recommendation {
	mesh, err := orientation.LoadModel(modelPath, filepath.Ext(stlKey))
	if err != nil {
		w.logger.Info("orientation skipped", "key", stlKey, "reason", err)
		return nil
	}
	rec, ok := orientation.Recommend(mesh, w.orientOpts)
	if !ok {
		return nil
	}
	return &rec
}
