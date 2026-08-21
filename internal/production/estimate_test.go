package production

import "testing"

// TestDefaultPrintTimeMinutesOrdersPartsByVolume is the property that matters.
// The exact minutes are a model, not a measurement, and will move; what must
// never break is that a bigger part estimates longer than a smaller one.
//
// A flat constant would satisfy "every job has a time" while failing this, and
// that is precisely the FAKE_SLICE failure: every design 25 minutes, so bed
// occupancy, queue order and every countdown were equally fictional.
func TestDefaultPrintTimeMinutesOrdersPartsByVolume(t *testing.T) {
	small := DefaultPrintTimeMinutes(40, 40, 20)
	medium := DefaultPrintTimeMinutes(80, 80, 40)
	large := DefaultPrintTimeMinutes(150, 150, 100)

	if !(small < medium && medium < large) {
		t.Errorf("estimates do not increase with volume: small=%d medium=%d large=%d", small, medium, large)
	}
	if small < minDefaultPrintMinutes {
		t.Errorf("small = %d, want at least the %d-minute floor: bed prep, purge and a first layer cost time no matter how tiny the part",
			small, minDefaultPrintMinutes)
	}
}

func TestDefaultPrintTimeMinutesGuardsItsInputs(t *testing.T) {
	tests := []struct {
		name    string
		x, y, z float64
		want    int
	}{
		{"no bounding box", 0, 0, 0, 0},
		{"one axis missing", 100, 0, 50, 0},
		{"negative axis", 100, -10, 50, 0},
		{"absurdly large is capped", 5000, 5000, 5000, maxDefaultPrintMinutes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultPrintTimeMinutes(tc.x, tc.y, tc.z); got != tc.want {
				t.Errorf("DefaultPrintTimeMinutes(%v, %v, %v) = %d, want %d", tc.x, tc.y, tc.z, got, tc.want)
			}
		})
	}
}

// TestEstimateBatchMinutesSumsRatherThanMaxes pins the change this model exists
// to make. The old batch estimate was MAX of the per-job times, so a bed of
// eight 40-minute parts was scheduled as 40 minutes of machine time - a machine
// could be handed a full day of work while looking nearly free to the
// scheduler.
func TestEstimateBatchMinutesSumsRatherThanMaxes(t *testing.T) {
	unit := []int{40, 40, 40, 40, 40, 40, 40, 40}
	qty := []int{1, 1, 1, 1, 1, 1, 1, 1}

	got := EstimateBatchMinutes(unit, qty, DefaultBatchTimeCorrection)

	if got <= 40 {
		t.Fatalf("batch of eight 40-minute parts estimated at %d minutes; MAX-of-jobs is exactly the bug this replaces", got)
	}
	if want := 272; got != want { // 320 * 0.85
		t.Errorf("estimate = %d, want %d (sum 320 x %.2f correction)", got, want, DefaultBatchTimeCorrection)
	}
}

// TestEstimateBatchMinutesScalesByQuantity: a job row carries units, not one
// part, and its estimate is per unit - the same convention
// filament_grams_required already uses. Ten of a part must not be scheduled as
// one of it.
func TestEstimateBatchMinutesScalesByQuantity(t *testing.T) {
	one := EstimateBatchMinutes([]int{30}, []int{1}, 1)
	ten := EstimateBatchMinutes([]int{30}, []int{10}, 1)

	if ten != one*10 {
		t.Errorf("quantity 10 estimated %d, want %d (10 x the single-unit %d)", ten, one*10, one)
	}
}

// TestEstimateBatchMinutesAppliesTheCorrection covers the shared-overhead
// discount, and that a nonsensical factor falls back rather than zeroing every
// batch on the floor.
func TestEstimateBatchMinutesAppliesTheCorrection(t *testing.T) {
	unit, qty := []int{100, 100}, []int{1, 1}

	if got, want := EstimateBatchMinutes(unit, qty, 0.5), 100; got != want {
		t.Errorf("with correction 0.5 = %d, want %d", got, want)
	}
	// A zero or negative factor is a misconfiguration; honouring it literally
	// would tell the scheduler every batch takes no time at all.
	if got, want := EstimateBatchMinutes(unit, qty, 0), 170; got != want {
		t.Errorf("with correction 0 = %d, want %d (the default %.2f applied)", got, want, DefaultBatchTimeCorrection)
	}
	if got := EstimateBatchMinutes(nil, nil, DefaultBatchTimeCorrection); got != 0 {
		t.Errorf("empty batch = %d, want 0", got)
	}
}
