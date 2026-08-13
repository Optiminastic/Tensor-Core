package slicing

// The second job kind on the "slice" queue: slice a batch's merged plate.
//
// A design slice (SliceArgs) answers "what does one unit of this design cost?"
// by slicing units_per_bed copies of one model and dividing. A batch slice
// answers a different question - "how long will THIS bed occupy THIS machine?"
// - and the two are not the same number. batchTimeFromJobs approximates the
// batch as MAX(each job's own per-plate estimate), which describes a bed
// holding one design's units, not the mixed bed that will actually print. That
// approximation is what feeds MachineFreeAt and therefore every machine
// assignment.
//
// The merged plate was already being built and stored (buildMergedPlate ->
// batches/<id>/{preview,plate}.stl) for the operator preview. Slicing it is
// what turns the batch's estimate into a measurement.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// batchSliceMaxAttempts is lower than sliceMaxAttempts: a design slice failing
// leaves a design permanently unpriced and unusable, while a batch plate slice
// failing only leaves the batch on its existing approximation. It is worth one
// retry for a transient blip, not three.
const batchSliceMaxAttempts = 2

// SliceBatchArgs is one merged plate to slice. Material, Quality and InfillPct
// are resolved by the enqueuer from the batch's own jobs rather than looked up
// here: a batch is single-material and single-quality by construction (see
// production.groupKey, which partitions on exactly those axes), so the enqueuer
// already holds the answer and the worker needs no design matching of its own.
type SliceBatchArgs struct {
	BatchID uuid.UUID `json:"batch_id"`
	// PlateKey is the object key of the merged plate STL. Every unit is
	// already positioned on it, so the slice runs with a single copy.
	PlateKey string `json:"plate_key"`
	// Units is how many physical units the plate carries, used only to derive
	// effective_time_per_unit_minutes from the measured total.
	Units    int    `json:"units"`
	Material string `json:"material"`
	// Colours on the bed, for attributing the plate's filament by colour. The
	// slicer reports per extruder, not per colour name.
	Colours []string `json:"colours"`
	Quality string   `json:"quality"`
	// PlateHeightMM is the merged plate's measured Z, for deriving the layer
	// count. It comes from the plate file asset's bbox rather than the slice,
	// because result.json reports a layer height but never a layer count.
	PlateHeightMM float64 `json:"plate_height_mm"`
	InfillPct     float64 `json:"infill_pct"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (SliceBatchArgs) Kind() string { return "slice_batch" }

// EnqueueBatchTx inserts a plate-slice job on the given transaction.
func (e *Enqueuer) EnqueueBatchTx(ctx context.Context, tx pgx.Tx, args SliceBatchArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: batchSliceMaxAttempts,
	})
	return err
}

// EnqueueBatch inserts a plate-slice job outside any transaction.
//
// Unlike a design slice - which is enqueued in the same transaction as the
// design row, so a design can never exist without its slice being queued - the
// plate does not exist until cachePreview has built and uploaded it, and that
// runs deliberately outside the batch-creation transaction (see
// AutoCreateBatches). Enqueuing transactionally would race: the worker could
// pick the job up and find no plate to download.
//
// The weaker guarantee is acceptable because the failure mode is mild. A lost
// enqueue leaves the batch on batchTimeFromJobs' approximation with
// plate_sliced_at NULL, which is exactly the state every batch was in before
// this existed - degraded, visible, and never wrong about being degraded.
func (e *Enqueuer) EnqueueBatch(ctx context.Context, args SliceBatchArgs) error {
	_, err := e.client.Insert(ctx, args, &river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: batchSliceMaxAttempts,
	})
	return err
}
