package delhivery

// A check against the real carrier, off by default.
//
// Everything else in this package is tested against a captured response, which
// proves the decoder but freezes Delhivery's behaviour at the moment it was
// captured. This one asks the live API, so a field that changes type or a
// reason worded differently shows up here rather than on the page.
//
// It needs two environment variables and does nothing without them:
//
//	DELHIVERY_API_KEY   the account's production key
//	DELHIVERY_TEST_AWBS a comma-separated list of that account's waybills
//
// It only ever TRACKS. Nothing here can reach /api/p/update, which changes a
// real parcel's delivery instructions.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveTrack(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DELHIVERY_API_KEY"))
	waybills := strings.TrimSpace(os.Getenv("DELHIVERY_TEST_AWBS"))
	if key == "" || waybills == "" {
		t.Skip("set DELHIVERY_API_KEY and DELHIVERY_TEST_AWBS to check against the live carrier")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	asked := strings.Split(waybills, ",")
	got, err := New(Config{APIKey: key}).Track(ctx, asked)
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the carrier returned nothing for any of those waybills")
	}

	counts := map[Reason]int{}
	for _, s := range got {
		reason := Classify(s)
		counts[reason]++

		if s.AWB == "" {
			t.Error("a shipment came back with no waybill")
		}
		if s.Status.Status == "" {
			t.Errorf("%s: no status", s.AWB)
		}
		// The two assertions that actually break: an address line typed as an
		// array, and a zoneless timestamp.
		if s.Consignee.Name == "" {
			t.Errorf("%s: consignee name empty - check the Consignee field names", s.AWB)
		}
		if !s.PickupDate.IsZero() && s.PickupDate.Year() < 2020 {
			t.Errorf("%s: pickup date parsed as %s", s.AWB, s.PickupDate)
		}

		t.Logf("%s  %-18s %-12s %-22s %s",
			s.AWB, s.Status.Status, s.Status.StatusType, reason, s.Status.Instructions)
	}
	t.Logf("asked %d, tracked %d", len(asked), len(got))
	for reason, n := range counts {
		t.Logf("  %-24s %3d  actionable=%v", reason, n, reason.Actionable())
	}
}
