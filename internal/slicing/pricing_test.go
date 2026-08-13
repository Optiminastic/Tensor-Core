package slicing

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// sliceTestStore mirrors httpapi's setupStore: migrate, truncate, skip without
// TENSOR_TEST_DB. Kept local rather than exported from httpapi because importing
// a test helper across packages is not possible, and lifting it into a shared
// dbtest package is a bigger refactor than these two tests justify.
func sliceTestStore(t *testing.T) *db.Store {
	t.Helper()
	dsn := os.Getenv("TENSOR_TEST_DB")
	if dsn == "" {
		t.Skip("set TENSOR_TEST_DB to run integration tests")
	}
	ctx := context.Background()
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := db.Open(ctx, dsn, db.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.Pool.Exec(ctx,
		`TRUNCATE designs, slice_jobs, slice_metrics, design_pricing, brands,
		 cost_assumption_sets RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return store
}

// seedDesignAndJob creates the minimum a slice result needs to land against.
//
// The brand comes first and is not optional: sliceTestStore truncates brands,
// and designs.brand_slug is a foreign key, so inserting the design without it
// fails on designs_brand_slug_fkey. priceDesign also reads this row's ladder
// and thresholds, so a brand with a real ladder is what makes pricing produce
// anything at all.
func seedDesignAndJob(t *testing.T, store *db.Store) (designID, jobID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	entryHours, entryRung := 2.0, int32(1)
	if _, err := store.Q.InsertBrand(ctx, gen.InsertBrandParams{
		ID: uuid.New(), Slug: "gifting", Name: "Gifting", StartingPrice: 499, IsActive: true,
		Ladder:     []byte(`[299,499,799,999,1499,1999,2999]`),
		CpGreenMax: 0.25, CpYellowMax: 0.30, EntryMachineHours: &entryHours, EntryRung: &entryRung,
	}); err != nil {
		t.Fatalf("insert brand: %v", err)
	}
	design, err := store.Q.InsertDesign(ctx, gen.InsertDesignParams{
		ID: uuid.New(), BrandSlug: "gifting", Name: "Cube", CreatedBy: "tester",
		Status: "slicing", StlKey: "designs/cube.stl", Material: "PLA",
		Finish: "standard", UnitsPerBed: 4, Quality: "standard", InfillPct: 15,
	})
	if err != nil {
		t.Fatalf("insert design: %v", err)
	}
	job, err := store.Q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
		ID: uuid.New(), DesignID: design.ID, Status: "queued", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("insert slice job: %v", err)
	}
	return design.ID, job.ID
}

func metricsWithFilament(g float64) PerUnitMetrics {
	return PerUnitMetrics{
		PrintTimeHr: 2, EffectiveMachineTimeHr: 0.5, FilamentG: g,
		ColourChanges: 1, ElectricityKwh: 0.1, UnitsPerBed: 4,
		LayerHeightMm: 0.2, InfillDensityPct: 15, WallLoops: 2,
		FilamentLengthMm: 1000, GcodeKey: "gcode/test.gcode.3mf",
	}
}

// TestIntegrationProcessSliceResultIsIdempotent covers the crash window between
// ProcessSliceResult committing and River acking the job. slice_metrics.job_id
// is the primary key, so before the upsert every retry unique-violated, burned
// the remaining attempts and got discarded - while the design was already
// correctly priced.
//
// The second call deliberately carries DIFFERENT metrics: that is what
// distinguishes DO UPDATE from DO NOTHING. priceDesign prices from the in-memory
// metrics, so a stale stored row would leave the slicer facts and the price
// describing different slices.
func TestIntegrationProcessSliceResultIsIdempotent(t *testing.T) {
	store := sliceTestStore(t)
	designID, jobID := seedDesignAndJob(t, store)
	ctx := context.Background()

	if err := ProcessSliceResult(ctx, store, jobID, designID, "gifting", metricsWithFilament(10)); err != nil {
		t.Fatalf("first ProcessSliceResult: %v", err)
	}
	if err := ProcessSliceResult(ctx, store, jobID, designID, "gifting", metricsWithFilament(25)); err != nil {
		t.Fatalf("second ProcessSliceResult (this is the regression - a retry must not unique-violate): %v", err)
	}

	var rows int
	var filament float64
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*), max(filament_g)::float8 FROM slice_metrics WHERE job_id = $1`,
		jobID).Scan(&rows, &filament); err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if rows != 1 {
		t.Errorf("slice_metrics rows = %d, want 1", rows)
	}
	if filament != 25 {
		t.Errorf("filament_g = %v, want 25 - the retry's metrics must win, or the stored facts and the derived price disagree", filament)
	}
}

// TestIntegrationFailJobWorksOnCancelledContext is the whole point of the
// detached context. The commonest reason to reach FailJob is River cancelling
// the job on its timeout, and a cancelled context cannot commit - so the old
// `_ = store.InTx(ctx, ...)` silently did nothing on exactly that path and left
// the design stuck in "slicing" forever.
func TestIntegrationFailJobWorksOnCancelledContext(t *testing.T) {
	store := sliceTestStore(t)
	designID, jobID := seedDesignAndJob(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // exactly the state River leaves behind on a job timeout

	if err := FailJob(ctx, store, jobID, "slice timed out"); err != nil {
		t.Fatalf("FailJob on a cancelled context: %v", err)
	}

	read := context.Background()
	var jobStatus, designStatus string
	if err := store.Pool.QueryRow(read,
		`SELECT status FROM slice_jobs WHERE id = $1`, jobID).Scan(&jobStatus); err != nil {
		t.Fatalf("read slice job: %v", err)
	}
	if err := store.Pool.QueryRow(read,
		`SELECT status FROM designs WHERE id = $1`, designID).Scan(&designStatus); err != nil {
		t.Fatalf("read design: %v", err)
	}
	if jobStatus != jobFailed {
		t.Errorf("slice job status = %q, want %q", jobStatus, jobFailed)
	}
	if designStatus != designFailed {
		t.Errorf("design status = %q, want %q - a design must never be stranded in 'slicing'", designStatus, designFailed)
	}
}

// TestIntegrationMarkSlicingDoesNotDemotePricedDesign guards the retry path:
// MarkSlicing runs at the top of Work, outside ProcessSliceResult's transaction,
// so without the status guard a retry would drag an already-priced design back
// to "slicing" and strand it there if the retry then failed.
func TestIntegrationMarkSlicingDoesNotDemotePricedDesign(t *testing.T) {
	store := sliceTestStore(t)
	designID, jobID := seedDesignAndJob(t, store)
	ctx := context.Background()

	if err := ProcessSliceResult(ctx, store, jobID, designID, "gifting", metricsWithFilament(10)); err != nil {
		t.Fatalf("ProcessSliceResult: %v", err)
	}
	if err := MarkSlicing(ctx, store, designID); err != nil {
		t.Fatalf("MarkSlicing: %v", err)
	}

	var status string
	if err := store.Pool.QueryRow(ctx,
		`SELECT status FROM designs WHERE id = $1`, designID).Scan(&status); err != nil {
		t.Fatalf("read design: %v", err)
	}
	if status != designPriced {
		t.Errorf("design status = %q after a retry's MarkSlicing, want %q", status, designPriced)
	}
}
