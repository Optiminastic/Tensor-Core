package httpapi

// Retention and the import floor are one decision.
//
// The floor is a fixed date - 26 August 2026 - so every sync asks Shopify for
// every order since then, around fifteen hundred of them, every ten minutes.
// Delete a sixty-day-old order under a thirty-day policy and the next sync
// imports it straight back, possibly creating fresh production jobs for a
// parcel delivered two months ago. Retention would quietly undo itself, and
// the only symptom would be the row count never falling.

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestTheImportFloorIsTheEventDateWhenNothingIsDeleted(t *testing.T) {
	// A shop keeping everything still wants the catch-up to reach back to the
	// day the production run began.
	s := &Server{cfg: config.Settings{RetentionEnabled: false, RetentionDays: 30}}
	if got := s.importSince(); !got.Equal(shopifyImportSince) {
		t.Errorf("importSince = %s, want the fixed event date %s", got, shopifyImportSince)
	}
}

func TestDeletingThirtyDaysStopsTheSyncReachingPastThem(t *testing.T) {
	s := &Server{cfg: config.Settings{RetentionEnabled: true, RetentionDays: 30}}
	got := s.importSince()

	if !got.After(shopifyImportSince) {
		t.Fatalf("importSince = %s; the sync would re-import orders retention just deleted", got)
	}
	// Within a day of the cutoff, which is all the precision this needs: the
	// point is that the floor MOVES with the window, not where it lands to
	// the minute.
	want := time.Now().AddDate(0, 0, -30)
	if diff := got.Sub(want); diff > 24*time.Hour || diff < -24*time.Hour {
		t.Errorf("importSince = %s, want about %s", got, want)
	}
}

func TestTheFloorNeverWalksBackPastTheProductionRun(t *testing.T) {
	// A long retention period must not pull the floor behind 26 August:
	// orders before it arrive without the personalisation properties the
	// pipeline expects and create jobs nobody means to print.
	s := &Server{cfg: config.Settings{RetentionEnabled: true, RetentionDays: 36500}}
	if got := s.importSince(); !got.Equal(shopifyImportSince) {
		t.Errorf("importSince = %s, want it clamped to %s", got, shopifyImportSince)
	}
}

func TestAZeroRetentionPeriodLeavesTheFloorAlone(t *testing.T) {
	// Days=0 is a misconfiguration, and the floor must not become "now" and
	// silently stop importing anything at all.
	s := &Server{cfg: config.Settings{RetentionEnabled: true, RetentionDays: 0}}
	if got := s.importSince(); !got.Equal(shopifyImportSince) {
		t.Errorf("importSince = %s, want the fixed date when days is zero", got)
	}
}
