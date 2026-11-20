package slicing

// The third job kind on the "slice" queue: slice a bed for printing.
//
// SliceArgs costs a design and SliceBatchArgs measures a bed; neither produces
// the file a printer runs. SlicePrintArgs does: dispatch has already chosen the
// machine and bound each plate slot to a tray, so the bed is sliced once, for
// that machine, with Tensor's own slicing profile, and the finished
// .gcode.3mf is handed to BambuBuddy to queue - the step BambuBuddy's own
// slicer used to do.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// printSliceMaxAttempts is low: a failed attempt has already released nothing,
// and the bed is marked as on its way, so a retry loop would hold it off every
// printer. Two tries cover a transient blip; after that the bed is released
// with the reason and dispatch can send it again.
const printSliceMaxAttempts = 2

// SlicePrintArgs is one bed to slice for one chosen machine.
type SlicePrintArgs struct {
	BatchID     uuid.UUID `json:"batch_id"`
	BatchNumber string    `json:"batch_number"`
	// ProfileID and ProfileVersion name the settings dispatch chose. The worker
	// slices with the profile as it stands and records both versions when they
	// differ, rather than refusing a bed over an edit made in the meantime.
	ProfileID      uuid.UUID `json:"profile_id"`
	ProfileVersion int       `json:"profile_version"`
	// PlateKey is the object key of the bed's merged 3MF.
	PlateKey      string `json:"plate_key"`
	PlateFilename string `json:"plate_filename"`
	// FilamentColours holds one "#RRGGBB" per plate slot: the colour of the
	// tray bound to it, which is what BambuBuddy sliced with too.
	FilamentColours []string `json:"filament_colours"`
	// NozzleOverrides are the per-bed keys dispatch computed for this machine:
	// filament_map, extruder_ams_count and the prime tower.
	NozzleOverrides map[string]any `json:"nozzle_overrides"`

	// What the queueing step needs once the file exists.
	PrinterID   int       `json:"printer_id"`
	MachineID   uuid.UUID `json:"machine_id"`
	MachineName string    `json:"machine_name"`
	AmsMapping  []int     `json:"ams_mapping"`
	TrayHexes   []string  `json:"tray_hexes"`
}

// Kind is River's stable job type name; it must not change once jobs exist.
func (SlicePrintArgs) Kind() string { return "slice_print" }

// EnqueuePrintTx inserts a print slice on the given transaction - the same one
// that marks the bed as on its way, so a bed can never be marked with no slice
// coming, nor sliced without the mark that stops a second send.
func (e *Enqueuer) EnqueuePrintTx(ctx context.Context, tx pgx.Tx, args SlicePrintArgs) error {
	_, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue:       QueueName,
		MaxAttempts: printSliceMaxAttempts,
	})
	return err
}
