package slicing

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// River persists a job's Kind as a string. Renaming it would orphan every plate
// slice already queued in river_job, which would silently never run.
func TestPlateSliceArgsKindIsStable(t *testing.T) {
	if got := (PlateSliceArgs{}).Kind(); got != "slice_plate" {
		t.Fatalf("Kind() = %q, want %q - renaming it orphans queued jobs", got, "slice_plate")
	}
	// And it must not collide with the design slice kind, or River would hand a
	// plate to the wrong worker.
	if (PlateSliceArgs{}).Kind() == (SliceArgs{}).Kind() {
		t.Fatal("plate and design slices share a Kind")
	}
}

// The key is what links a batch row to its printable artefact in object storage.
func TestPlateGcodeKeyIsNamespacedPerBatch(t *testing.T) {
	a, b := uuid.New(), uuid.New()

	key := PlateGcodeKey(a)
	if !strings.HasPrefix(key, "gcode/batches/") {
		t.Errorf("key %q is not under the batch namespace", key)
	}
	if !strings.HasSuffix(key, ".gcode.3mf") {
		t.Errorf("key %q does not carry the Bambu G-code extension", key)
	}
	if !strings.Contains(key, a.String()) {
		t.Errorf("key %q does not identify its batch", key)
	}
	if key == PlateGcodeKey(b) {
		t.Error("two batches produced the same key: one plate would overwrite the other")
	}
}
