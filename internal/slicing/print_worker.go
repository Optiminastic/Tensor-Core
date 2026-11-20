package slicing

// PrintSliceWorker slices one bed for its chosen machine with Tensor's own
// slicing profile, uploads the finished .gcode.3mf to BambuBuddy's library
// and hands it to the queueing step (httpapi.SliceQueueWorker), which checks
// the trays again and queues it on the printer.
//
// The bed was marked as on its way when this job was enqueued (dispatch sets
// bambu_slice_job_id to 0 in the same transaction). Every failure here must
// therefore end in one of two ways: retried, or released with the reason on
// the batch. A failure that did neither would leave a bed nothing will ever
// send again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// releaseTimeout bounds the writes that release a bed after a failure, which
// run detached from a job context that may already be cancelled.
const releaseTimeout = 10 * time.Second

// PrintStore is the slice of the queries the worker uses.
type PrintStore interface {
	GetSlicingProfile(ctx context.Context, id uuid.UUID) (gen.SlicingProfile, error)
	SetBatchPrintError(ctx context.Context, arg gen.SetBatchPrintErrorParams) error
	ClearBatchSliceJob(ctx context.Context, id uuid.UUID) error
}

// PrintObjects is the object store: the plate in, a copy of the print file out.
type PrintObjects interface {
	Download(ctx context.Context, key, destPath string) error
	Upload(ctx context.Context, key, srcPath, contentType string) error
}

// PrintLibrary is BambuBuddy's file library.
type PrintLibrary interface {
	UploadFile(ctx context.Context, filename string, r io.Reader) (bambubuddy.UploadedFile, error)
}

// PrintQueuer schedules the queueing step once the file is in the library.
type PrintQueuer interface {
	Enqueue(ctx context.Context, args production.QueueSlicedPlateArgs) error
}

// PrintSliceDeps are the worker's collaborators. Slice defaults to RunPrintSlice.
type PrintSliceDeps struct {
	Store     PrintStore
	Objects   PrintObjects
	Library   PrintLibrary
	Queuer    PrintQueuer
	Processes SystemPresets
	Filaments SystemPresets
	BambuRoot string
	Timeout   time.Duration
	Logger    *slog.Logger
	Slice     func(context.Context, PrintSliceInput) (SliceOutput, error)
}

// PrintSliceWorker is the river.Worker for SlicePrintArgs.
type PrintSliceWorker struct {
	river.WorkerDefaults[SlicePrintArgs]
	deps PrintSliceDeps
}

// errPermanent marks a failure no retry can fix: the bed is released at once.
var errPermanent = errors.New("permanent")

func permanent(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errPermanent, fmt.Sprintf(format, a...))
}

// NewPrintSliceWorker builds the worker.
func NewPrintSliceWorker(deps PrintSliceDeps) *PrintSliceWorker {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Slice == nil {
		deps.Slice = RunPrintSlice
	}
	return &PrintSliceWorker{deps: deps}
}

// Timeout gives River room for the slice plus the uploads around it.
func (w *PrintSliceWorker) Timeout(*river.Job[SlicePrintArgs]) time.Duration {
	return w.deps.Timeout + 5*time.Minute
}

// Work slices, uploads and schedules the queueing, or releases the bed.
func (w *PrintSliceWorker) Work(ctx context.Context, job *river.Job[SlicePrintArgs]) error {
	a := job.Args
	log := w.deps.Logger.With("batch", a.BatchNumber, "machine", a.MachineName, "attempt", job.Attempt)
	log.Info("print slice start", "profile", a.ProfileID, "version", a.ProfileVersion)

	fileID, err := w.sliceAndUpload(ctx, a, log)
	if err == nil {
		err = w.deps.Queuer.Enqueue(ctx, queueArgs(a, fileID))
	}
	if err == nil {
		log.Info("print slice uploaded; queueing scheduled", "library_file", fileID)
		return nil
	}
	if !errors.Is(err, errPermanent) && job.Attempt < job.MaxAttempts {
		log.Warn("print slice failed; retrying", "error", err)
		return err
	}
	w.release(ctx, a, err)
	log.Warn("print slice failed; the bed is sendable again", "error", err)
	return nil
}

func (w *PrintSliceWorker) sliceAndUpload(ctx context.Context, a SlicePrintArgs, log *slog.Logger) (int, error) {
	files, version, err := w.presetFiles(ctx, a, log)
	if err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "print-slice-*")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	platePath := filepath.Join(dir, "plate.3mf")
	if err := w.deps.Objects.Download(ctx, a.PlateKey, platePath); err != nil {
		return 0, fmt.Errorf("download the plate: %w", err)
	}
	out, err := w.deps.Slice(ctx, PrintSliceInput{
		BambuRoot: w.deps.BambuRoot, PlatePath: platePath, Files: files,
		Dir: dir, Timeout: w.deps.Timeout,
	})
	if err != nil {
		return 0, err
	}
	// A copy in our own storage: what printed this bed, readable after
	// BambuBuddy's library has moved on. Best-effort; the print does not wait on it.
	key := fmt.Sprintf("batches/%s/print-v%d.gcode.3mf", a.BatchID, version)
	if err := w.deps.Objects.Upload(ctx, key, out.Gcode3mfPath, "application/vnd.ms-package.3dmanufacturing-3dmodel+xml"); err != nil {
		log.Warn("could not keep a copy of the print file", "key", key, "error", err)
	}
	return w.uploadToLibrary(ctx, a, out.Gcode3mfPath)
}

func (w *PrintSliceWorker) uploadToLibrary(ctx context.Context, a SlicePrintArgs, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	uploaded, err := w.deps.Library.UploadFile(ctx, printFilename(a), f)
	if err != nil {
		return 0, fmt.Errorf("upload the print file to BambuBuddy: %w", err)
	}
	if uploaded.ID <= 0 {
		return 0, fmt.Errorf("BambuBuddy accepted the print file but returned no library id")
	}
	return uploaded.ID, nil
}

// presetFiles loads the profile and renders this bed's preset files.
func (w *PrintSliceWorker) presetFiles(ctx context.Context, a SlicePrintArgs, log *slog.Logger) (PresetFiles, int, error) {
	row, err := w.deps.Store.GetSlicingProfile(ctx, a.ProfileID)
	if err != nil {
		return PresetFiles{}, 0, permanent("the slicing profile this bed was sent with is gone; import the profiles and send it again")
	}
	if int(row.Version) != a.ProfileVersion {
		log.Info("profile changed since dispatch; slicing with the current version",
			"dispatched", a.ProfileVersion, "current", row.Version)
	}
	profile, err := DecodeProfile(row)
	if err != nil {
		return PresetFiles{}, 0, permanent("%v", err)
	}
	if IsSystemFilamentRef(profile.Filament) {
		if w.deps.Filaments == nil {
			return PresetFiles{}, 0, permanent("profile %q slices with a Bambu system filament and this worker cannot read the bundled filaments", row.Name)
		}
		if profile.Filament, err = w.deps.Filaments.ResolveFilament(profile.Filament); err != nil {
			return PresetFiles{}, 0, permanent("profile %q: %v", row.Name, err)
		}
	}
	if w.deps.Processes != nil {
		if _, err := w.deps.Processes.FillFromChain(profile.Process); err != nil {
			return PresetFiles{}, 0, permanent("profile %q: %v", row.Name, err)
		}
	}
	files, err := BuildPresetFiles(profile, BedSettings{
		FilamentColours: a.FilamentColours, NozzleOverrides: a.NozzleOverrides,
	})
	if err != nil {
		return PresetFiles{}, 0, permanent("%v", err)
	}
	return files, profile.Version, nil
}

// release records why the bed did not slice and frees it to be sent again.
func (w *PrintSliceWorker) release(ctx context.Context, a SlicePrintArgs, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	reason := fmt.Sprintf("Tensor could not slice this bed for %s: %s",
		a.MachineName, strings.TrimPrefix(cause.Error(), errPermanent.Error()+": "))
	if err := w.deps.Store.SetBatchPrintError(ctx, gen.SetBatchPrintErrorParams{
		ID: a.BatchID, PrintError: &reason,
	}); err != nil {
		w.deps.Logger.Warn("could not record why the bed did not slice", "batch", a.BatchID, "error", err)
	}
	if err := w.deps.Store.ClearBatchSliceJob(ctx, a.BatchID); err != nil {
		w.deps.Logger.Warn("could not release the bed after a failed slice", "batch", a.BatchID, "error", err)
	}
}

func queueArgs(a SlicePrintArgs, fileID int) production.QueueSlicedPlateArgs {
	return production.QueueSlicedPlateArgs{
		BatchID: a.BatchID, PrinterID: a.PrinterID, MachineID: a.MachineID,
		MachineName: a.MachineName, AmsMapping: a.AmsMapping, TrayHexes: a.TrayHexes,
		LibraryFileID: fileID,
	}
}

// printFilename names the upload so BambuBuddy files it as sliced and an
// operator can tell which bed and which slicer it came from.
func printFilename(a SlicePrintArgs) string {
	base := strings.TrimSuffix(filepath.Base(a.PlateFilename), ".3mf")
	if base == "" || base == "." {
		base = a.BatchNumber
	}
	return base + "-tensor.gcode.3mf"
}

// DecodeProfile turns a stored profile row into a PrintProfile.
func DecodeProfile(r gen.SlicingProfile) (PrintProfile, error) {
	p := PrintProfile{Name: r.Name, MachineFamily: r.MachineFamily, Version: int(r.Version)}
	for _, part := range []struct {
		raw  []byte
		into *map[string]any
		what string
	}{
		{r.PrinterPreset, &p.Printer, "printer"},
		{r.ProcessPreset, &p.Process, "process"},
		{r.FilamentPreset, &p.Filament, "filament"},
		{r.Overrides, &p.Overrides, "overrides"},
	} {
		if err := json.Unmarshal(part.raw, part.into); err != nil {
			return PrintProfile{}, fmt.Errorf("profile %q has an unreadable %s preset", r.Name, part.what)
		}
	}
	return p, nil
}
