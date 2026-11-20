package slicing

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type fakePrintStore struct {
	profile  *gen.SlicingProfile
	errors   []string
	released int
}

func (f *fakePrintStore) GetSlicingProfile(context.Context, uuid.UUID) (gen.SlicingProfile, error) {
	if f.profile == nil {
		return gen.SlicingProfile{}, pgx.ErrNoRows
	}
	return *f.profile, nil
}

func (f *fakePrintStore) SetBatchPrintError(_ context.Context, a gen.SetBatchPrintErrorParams) error {
	f.errors = append(f.errors, *a.PrintError)
	return nil
}

func (f *fakePrintStore) ClearBatchSliceJob(context.Context, uuid.UUID) error {
	f.released++
	return nil
}

type fakeObjects struct{ uploaded []string }

func (f *fakeObjects) Download(_ context.Context, _ string, dest string) error {
	return os.WriteFile(dest, []byte("plate"), 0o644)
}

func (f *fakeObjects) Upload(_ context.Context, key, _ string, _ string) error {
	f.uploaded = append(f.uploaded, key)
	return nil
}

type fakeLibrary struct {
	err  error
	name string
	body string
}

func (f *fakeLibrary) UploadFile(_ context.Context, name string, r io.Reader) (bambubuddy.UploadedFile, error) {
	if f.err != nil {
		return bambubuddy.UploadedFile{}, f.err
	}
	b, _ := io.ReadAll(r)
	f.name, f.body = name, string(b)
	return bambubuddy.UploadedFile{ID: 42, Filename: name}, nil
}

type fakeQueuer struct{ got []production.QueueSlicedPlateArgs }

func (f *fakeQueuer) Enqueue(_ context.Context, a production.QueueSlicedPlateArgs) error {
	f.got = append(f.got, a)
	return nil
}

func storedProfile() *gen.SlicingProfile {
	return &gen.SlicingProfile{
		ID: uuid.New(), Name: "0.28mm heart", MachineFamily: "H2C", Version: 2,
		PrinterPreset:  []byte(`{"type":"machine","name":"Bambu Lab H2C 0.4 nozzle","from":"System"}`),
		ProcessPreset:  []byte(`{"type":"process","name":"0.28mm heart","from":"User","wall_loops":"2"}`),
		FilamentPreset: []byte(`{"type":"filament","name":"PLA copy","from":"User"}`),
		Overrides:      []byte(`{"wall_loops":"3"}`),
	}
}

type printHarness struct {
	store   *fakePrintStore
	objects *fakeObjects
	library *fakeLibrary
	queuer  *fakeQueuer
	slices  int
	got     PrintSliceInput
}

func newPrintHarness(sliceErr error) (*printHarness, *PrintSliceWorker) {
	h := &printHarness{
		store: &fakePrintStore{profile: storedProfile()}, objects: &fakeObjects{},
		library: &fakeLibrary{}, queuer: &fakeQueuer{},
	}
	w := NewPrintSliceWorker(PrintSliceDeps{
		Store: h.store, Objects: h.objects, Library: h.library, Queuer: h.queuer,
		Slice: func(_ context.Context, in PrintSliceInput) (SliceOutput, error) {
			h.slices++
			h.got = in
			if sliceErr != nil {
				return SliceOutput{}, sliceErr
			}
			p := filepath.Join(in.Dir, "out.gcode.3mf")
			if err := os.WriteFile(p, []byte("gcode"), 0o644); err != nil {
				return SliceOutput{}, err
			}
			return SliceOutput{Gcode3mfPath: p}, nil
		},
	})
	return h, w
}

func printJob(attempt int) *river.Job[SlicePrintArgs] {
	return &river.Job[SlicePrintArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: printSliceMaxAttempts},
		Args: SlicePrintArgs{
			BatchID: uuid.New(), BatchNumber: "B-116688", ProfileVersion: 2,
			PlateKey: "batches/x/plate.3mf", PlateFilename: "116688-116706.3mf",
			FilamentColours: []string{"#F72323", "#FFFFFF"},
			NozzleOverrides: map[string]any{"filament_map": []any{"2", "1"}},
			PrinterID:       2, MachineID: uuid.New(), MachineName: "H2",
			AmsMapping: []int{2, 254}, TrayHexes: []string{"#F72323", "#FFFFFF"},
		},
	}
}

func TestPrintSliceUploadsAndSchedulesTheQueueing(t *testing.T) {
	h, w := newPrintHarness(nil)
	if err := w.Work(context.Background(), printJob(1)); err != nil {
		t.Fatal(err)
	}
	if len(h.queuer.got) != 1 {
		t.Fatalf("queueing scheduled %d times, want 1", len(h.queuer.got))
	}
	q := h.queuer.got[0]
	if q.LibraryFileID != 42 || q.SliceJobID != 0 || len(q.AmsMapping) != 2 || q.PrinterID != 2 {
		t.Errorf("queue args: %+v", q)
	}
	if h.library.name != "116688-116706-tensor.gcode.3mf" || h.library.body != "gcode" {
		t.Errorf("uploaded %q with %q", h.library.name, h.library.body)
	}
	if len(h.got.Files.Filaments) != 2 {
		t.Errorf("one filament file per slot, got %d", len(h.got.Files.Filaments))
	}
	if !strings.Contains(string(h.got.Files.Process), `"wall_loops":"3"`) {
		t.Error("the profile's override must reach the process file")
	}
	if len(h.objects.uploaded) != 1 || !strings.HasSuffix(h.objects.uploaded[0], "print-v2.gcode.3mf") {
		t.Errorf("kept copy: %v", h.objects.uploaded)
	}
	if h.store.released != 0 {
		t.Error("a successful slice must not release the bed")
	}
}

func TestPrintSliceReleasesAtOnceWhenTheProfileIsGone(t *testing.T) {
	h, w := newPrintHarness(nil)
	h.store.profile = nil
	if err := w.Work(context.Background(), printJob(1)); err != nil {
		t.Fatalf("a permanent failure must not ask River to retry: %v", err)
	}
	if h.slices != 0 || h.store.released != 1 || len(h.store.errors) != 1 {
		t.Fatalf("slices=%d released=%d errors=%v", h.slices, h.store.released, h.store.errors)
	}
	if !strings.Contains(h.store.errors[0], "import the profiles") || strings.Contains(h.store.errors[0], "permanent") {
		t.Errorf("reason on the batch: %q", h.store.errors[0])
	}
}

func TestPrintSliceRetriesOnceThenReleases(t *testing.T) {
	h, w := newPrintHarness(errors.New("the slicer exported no file"))
	if err := w.Work(context.Background(), printJob(1)); err == nil {
		t.Fatal("the first failed attempt should be retried")
	}
	if h.store.released != 0 {
		t.Fatal("a bed must not be released while a retry is coming")
	}
	if err := w.Work(context.Background(), printJob(2)); err != nil {
		t.Fatalf("the last attempt releases instead of erroring: %v", err)
	}
	if h.store.released != 1 || !strings.Contains(h.store.errors[0], "for H2") {
		t.Fatalf("released=%d errors=%v", h.store.released, h.store.errors)
	}
	if len(h.queuer.got) != 0 {
		t.Error("nothing may be queued from a failed slice")
	}
}

func TestPrintSliceReleasesWhenTheUploadFailsOnTheLastAttempt(t *testing.T) {
	h, w := newPrintHarness(nil)
	h.library.err = errors.New("connection refused")
	if err := w.Work(context.Background(), printJob(printSliceMaxAttempts)); err != nil {
		t.Fatal(err)
	}
	if h.store.released != 1 || len(h.queuer.got) != 0 {
		t.Fatalf("released=%d queued=%d", h.store.released, len(h.queuer.got))
	}
}
