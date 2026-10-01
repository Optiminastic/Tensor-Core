package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

// h2cStatus is what an H2C reports: an AMS unit plus two virtual trays, 254
// holding the fixed white and 255 whatever the shop left in it.
func h2cStatus(vt ...bambubuddy.VirtualTray) bambubuddy.Status {
	return bambubuddy.Status{
		AMS: []bambubuddy.AMS{{
			ID: 0,
			Trays: []bambubuddy.Tray{
				{ID: 0, Colour: "F72323FF", Type: "PLA", Exists: true, Remain: 50},
			},
		}},
		VTTray: vt,
	}
}

// The whole point of reading vt_tray: the H2Cs' external white used to be
// invisible, so the colour gate refused all three for "no spool has been
// confirmed as #FFFFFF" while the filament sat in the machine.
func TestExternalSpoolTrayIsSyncedFromTheReportedColour(t *testing.T) {
	got, ok := externalSpoolTray(h2cStatus(
		bambubuddy.VirtualTray{ID: 254, Colour: "FFFFFFFF", Type: "PLA", InfoIdx: "GFL99"},
	))
	if !ok {
		t.Fatal("the external spool reports FFFFFFFF/PLA and must be read as loaded")
	}
	if got.Colour != "#FFFFFF" {
		t.Fatalf("colour = %q, want #FFFFFF", got.Colour)
	}
	if got.AmsID == nil || *got.AmsID != amsExternalSpool {
		t.Fatalf("ams id = %v, want %d", got.AmsID, amsExternalSpool)
	}
	// 254, not -1: [-1,1] halts at the first tool change with 0700-8012,
	// [254,1] prints. Measured on H3 with one file, one value changed.
	if index, ok := amsSlotIndex(got); !ok || index != amsExternalSpool {
		t.Fatalf("ams_mapping index = %d/%v, want %d - proved on H3", index, ok, amsExternalSpool)
	}
}

// An unloaded external feed reports type "" and colour 00000000, exactly like
// an empty AMS slot, and must not count as a spool.
func TestExternalSpoolTrayIgnoresAnEmptyFeed(t *testing.T) {
	if _, ok := externalSpoolTray(h2cStatus(
		bambubuddy.VirtualTray{ID: 254, Colour: "00000000", Type: ""},
	)); ok {
		t.Fatal("an empty external feed must not be reported as loaded")
	}
}

// 255 holds black on one machine here and no ams_mapping value for it has ever
// been observed. Counting it would let the colour gate accept a bed for a spool
// the send path cannot address.
func TestExternalSpoolTrayIgnoresTheSecondVirtualTray(t *testing.T) {
	if _, ok := externalSpoolTray(h2cStatus(
		bambubuddy.VirtualTray{ID: 255, Colour: "161616FF", Type: "PLA"},
	)); ok {
		t.Fatal("vt_tray 255 has no known ams_mapping value and must not count as loaded")
	}
}

// The declared colour is a fallback. Two trays on one feed would both answer to
// the same ams_mapping value, and which one a slot bound to would depend on
// array order.
func TestDecodeTraysDoesNotAddTheDeclaredSpoolOnTopOfTheSyncedOne(t *testing.T) {
	ams, tray := amsExternalSpool, 0
	synced, err := json.Marshal([]loadedTray{
		{Colour: "#FFFFFF", Type: "PLA", AmsID: &ams, TrayID: &tray},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	declared, index := "#FFFFFF", int32(1)
	trays := decodeTrays(gen.Machine{
		Filaments:         synced,
		FixedNozzleColour: &declared,
		FixedNozzleIndex:  &index,
	})
	external := 0
	for _, tr := range trays {
		if tr.AmsID != nil && *tr.AmsID == amsExternalSpool {
			external++
		}
	}
	if external != 1 {
		t.Fatalf("trays on the external feed = %d, want 1", external)
	}
}

// With nothing synced, the declaration still has to work - that is what keeps a
// machine whose external spool the printer cannot read usable.
func TestDecodeTraysStillUsesTheDeclaredSpoolWhenNoneIsSynced(t *testing.T) {
	declared, index := "#FFFFFF", int32(1)
	trays := decodeTrays(gen.Machine{
		Filaments:         []byte("[]"),
		FixedNozzleColour: &declared,
		FixedNozzleIndex:  &index,
	})
	if len(trays) != 1 || trays[0].Colour != "#FFFFFF" {
		t.Fatalf("trays = %+v, want the declared external spool", trays)
	}
}
