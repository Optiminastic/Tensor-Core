package slicing

// The River worker for SliceBatchArgs: download a batch's merged plate, slice
// it, and replace the batch's MAX-of-jobs approximation with the measured
// figures for that actual bed.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

const plateName = "plate.stl"

// BatchSliceWorker slices one merged plate per job.
type BatchSliceWorker struct {
	river.WorkerDefaults[SliceBatchArgs]

	store        *db.Store
	objects      *storage.Client
	bambuRoot    string
	sliceTimeout time.Duration
	logger       *slog.Logger
	fakeSlice    bool
}

// NewBatchSliceWorker builds the worker. logger may be nil.
func NewBatchSliceWorker(
	store *db.Store, objects *storage.Client, bambuRoot string,
	sliceTimeout time.Duration, logger *slog.Logger, fakeSlice bool,
) *BatchSliceWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &BatchSliceWorker{
		store: store, objects: objects, bambuRoot: bambuRoot,
		sliceTimeout: sliceTimeout, logger: logger, fakeSlice: fakeSlice,
	}
}

// Timeout mirrors SliceWorker.Timeout - River's default is one minute, which a
// full plate will exceed routinely. See sliceOverhead.
func (w *BatchSliceWorker) Timeout(*river.Job[SliceBatchArgs]) time.Duration {
	return w.sliceTimeout + sliceOverhead
}

// Work slices one plate. A failure is retried by River; on the final attempt
// the reason is recorded on the batch so a stale approximation is not mistaken
// for a measurement.
func (w *BatchSliceWorker) Work(ctx context.Context, job *river.Job[SliceBatchArgs]) error {
	args := job.Args
	if w.fakeSlice {
		// Deliberately does nothing rather than fabricating a number.
		// FakeSlice exists so a design can reach "priced" without a slicer;
		// inventing a batch time would defeat the only reason this job kind
		// exists, and would be indistinguishable from a real measurement.
		w.logger.Warn("FAKE_SLICE enabled: leaving the batch on its estimated time",
			"batch", args.BatchID)
		return nil
	}

	w.logger.Info("plate slice start", "batch", args.BatchID, "attempt", job.Attempt)
	metrics, err := w.slicePlate(ctx, args)
	if err != nil {
		w.logger.Error("plate slice failed", "batch", args.BatchID, "attempt", job.Attempt, "error", err)
		w.recordFailureIfFinal(ctx, job, err)
		return err
	}

	total := int32(math.Round(metrics.PrintTimeS / 60))
	if total < 1 {
		total = 1
	}
	params := gen.SetBatchPlateSliceResultParams{
		ID: args.BatchID, TotalPrintTimeMinutes: &total,
		FilamentByColour: colourSplitJSON(metrics, args),
	}
	if args.Units > 0 {
		eff := float64(total) / float64(args.Units)
		params.EffectiveTimePerUnitMinutes = &eff
	}
	if metrics.FilamentWeightG > 0 {
		grams := metrics.FilamentWeightG
		params.TotalFilamentGrams = &grams
	}
	// The rest of what the slice already measured. Plate-level figures: support
	// and purge in particular are properties of the combined plate, not of any
	// one part on it, which is exactly why they cannot be summed from per-job
	// estimates.
	if metrics.SupportWeightG > 0 {
		params.SupportGrams = &metrics.SupportWeightG
	}
	if metrics.PurgeWeightG > 0 {
		params.PurgeGrams = &metrics.PurgeWeightG
	}
	if metrics.ColourChanges > 0 {
		changes := int32(metrics.ColourChanges)
		params.ColourChanges = &changes
	}
	if layers := layerCount(metrics, args.PlateHeightMM); layers > 0 {
		params.TotalLayers = &layers
	}
	if _, err := w.store.Q.SetBatchPlateSliceResult(ctx, params); err != nil {
		wrapped := fmt.Errorf("record plate slice result: %w", err)
		w.recordFailureIfFinal(ctx, job, wrapped)
		return wrapped
	}
	w.logger.Info("plate slice done", "batch", args.BatchID,
		"minutes", total, "grams", metrics.FilamentWeightG)
	return nil
}

// slicePlate is the download-slice-parse pipeline, in a temp dir removed when
// it returns. The plate already carries every unit at its packed position, so
// it is sliced as a single copy - unlike a design slice, which loads the model
// units_per_bed times and divides.
func (w *BatchSliceWorker) slicePlate(ctx context.Context, args SliceBatchArgs) (SliceMetrics, error) {
	workdir, err := os.MkdirTemp("", "plate-*")
	if err != nil {
		return SliceMetrics{}, fmt.Errorf("create workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	platePath := filepath.Join(workdir, plateName)
	if err := w.objects.Download(ctx, args.PlateKey, platePath); err != nil {
		return SliceMetrics{}, fmt.Errorf("download plate: %w", err)
	}
	profiles, err := ResolveProfiles(w.bambuRoot, args.Material, args.Quality)
	if err != nil {
		return SliceMetrics{}, err
	}
	infill := args.InfillPct
	if infill <= 0 {
		infill = defaultInfillPct
	}

	out, err := RunSlice(ctx, w.bambuRoot, profiles, platePath, infill, 1, workdir, w.sliceTimeout)
	if err != nil {
		return SliceMetrics{}, err
	}
	result, err := LoadResultJSON(out.ResultJSONPath)
	if err != nil {
		return SliceMetrics{}, fmt.Errorf("read result.json: %w", err)
	}
	sliceInfo, err := LoadSliceInfo(out.Gcode3mfPath)
	if err != nil {
		return SliceMetrics{}, fmt.Errorf("read slice_info: %w", err)
	}
	plateGcode, err := LoadPlateGcode(out.Gcode3mfPath)
	if err != nil {
		return SliceMetrics{}, fmt.Errorf("read plate gcode: %w", err)
	}
	// Whole-plate metrics, used as-is: unlike a design slice there is nothing
	// to divide by, because the plate is the unit of interest.
	return ExtractMetrics(result, sliceInfo, profiles.DensityGCm3, plateGcode)
}

// recordFailureIfFinal writes the reason onto the batch once River has given
// up, so "never sliced" and "tried and failed" are distinguishable. Logged
// rather than returned: the caller is already returning the real failure.
func (w *BatchSliceWorker) recordFailureIfFinal(ctx context.Context, job *river.Job[SliceBatchArgs], cause error) {
	if job.Attempt < job.MaxAttempts {
		return
	}
	// Detached, for the same reason FailJob is: the usual way to reach the
	// final attempt is River cancelling the job, and a cancelled context
	// cannot commit.
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failJobTimeout)
	defer cancel()
	if err := w.store.Q.SetBatchPlateSliceError(failCtx, gen.SetBatchPlateSliceErrorParams{
		ID: job.Args.BatchID, PlateSliceError: ptrTo(cause.Error()),
	}); err != nil {
		w.logger.Error("could not record the plate slice failure",
			"batch", job.Args.BatchID, "error", err)
	}
}

func ptrTo[T any](v T) *T { return &v }

// layerCount is how many layers the plate prints, derived from the measured
// plate height and the layer height the slice actually used.
//
// Derived rather than parsed: result.json reports layer_height but not a layer
// count, and the plate's own Z is the only height that describes the merged
// bed. Zero when either input is missing, which the caller treats as unknown
// rather than as zero layers.
func layerCount(m SliceMetrics, plateHeightMM float64) int32 {
	if m.LayerHeightMM <= 0 || plateHeightMM <= 0 {
		return 0
	}
	return int32(math.Ceil(plateHeightMM / m.LayerHeightMM))
}

// colourSplitJSON is the plate's filament broken down by colour, for the batch
// row's filament_by_colour.
//
// The slicer reports filament per extruder, not per colour name, so the colour
// labels come from the batch's own jobs. With one material and one colour on
// the bed - the overwhelmingly common case here - that is exact. A genuinely
// multi-colour plate gets the total attributed to the colours present rather
// than a false per-colour precision the slice cannot support.
func colourSplitJSON(m SliceMetrics, args SliceBatchArgs) []byte {
	if m.FilamentWeightG <= 0 || len(args.Colours) == 0 {
		return []byte("[]")
	}
	share := m.FilamentWeightG / float64(len(args.Colours))
	type entry struct {
		Colour   string  `json:"colour"`
		Material string  `json:"material"`
		Grams    float64 `json:"grams"`
	}
	out := make([]entry, 0, len(args.Colours))
	for _, c := range args.Colours {
		out = append(out, entry{Colour: c, Material: args.Material, Grams: math.Round(share*100) / 100})
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}
