package bedpack

import (
	"math"
	"testing"
)

func TestPackSingleUnitAtOrigin(t *testing.T) {
	placements, rejected := Pack([]UnitFootprint{{RefID: "a", XMM: 100, YMM: 100, ZMM: 50}})
	if len(rejected) != 0 || len(placements) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/0", len(placements), len(rejected))
	}
	p := placements[0]
	if p.XOffsetMM != 0 || p.YOffsetMM != 0 || p.Rotated {
		t.Errorf("placement = %+v, want origin, not rotated", p)
	}
}

func TestPackRejectsTooTall(t *testing.T) {
	_, rejected := Pack([]UnitFootprint{{RefID: "z", XMM: 50, YMM: 50, ZMM: BedZMM + 1}})
	if len(rejected) != 1 {
		t.Errorf("a too-tall unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRejectsTooWide(t *testing.T) {
	// 350 mm wide cannot fit even alone (needs 360 with the gap, bed is 300).
	_, rejected := Pack([]UnitFootprint{{RefID: "w", XMM: 350, YMM: 50, ZMM: 10}})
	if len(rejected) != 1 {
		t.Errorf("an over-wide unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRotatesToFit(t *testing.T) {
	// 300x50 does not fit at 0 deg (310 > 300) but does rotated (60 x 310).
	placements, rejected := Pack([]UnitFootprint{{RefID: "r", XMM: 300, YMM: 50, ZMM: 10}})
	if len(rejected) != 0 || len(placements) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/0", len(placements), len(rejected))
	}
	if !placements[0].Rotated {
		t.Error("the unit should have been rotated to fit")
	}
}

func TestPackTwoUnitsShareBed(t *testing.T) {
	placements, rejected := Pack([]UnitFootprint{
		{RefID: "a", XMM: 100, YMM: 100, ZMM: 20},
		{RefID: "b", XMM: 100, YMM: 100, ZMM: 20},
	})
	if len(placements) != 2 || len(rejected) != 0 {
		t.Fatalf("placed=%d rejected=%d, want 2/0", len(placements), len(rejected))
	}
	if placements[0].RefID != "a" || placements[1].RefID != "b" {
		t.Errorf("placement order = %s,%s, want a,b", placements[0].RefID, placements[1].RefID)
	}
}

func TestPackFullBedRejectsRemainder(t *testing.T) {
	// A near-max part consumes the whole bed; nothing else can fit.
	placements, rejected := Pack([]UnitFootprint{
		{RefID: "big", XMM: 290, YMM: 310, ZMM: 20},
		{RefID: "extra", XMM: 100, YMM: 100, ZMM: 20},
	})
	if len(placements) != 1 || len(rejected) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/1", len(placements), len(rejected))
	}
	if rejected[0].RefID != "extra" {
		t.Errorf("rejected %s, want extra", rejected[0].RefID)
	}
}

func TestUtilisationPercent(t *testing.T) {
	got := UtilisationPercent([]UnitFootprint{{XMM: 100, YMM: 100}})
	want := math.Round(10000.0/96000.0*100*100) / 100 // 10.42
	if got != want {
		t.Errorf("utilisation = %v, want %v", got, want)
	}
	if UtilisationPercent(nil) != 0 {
		t.Error("empty utilisation should be 0")
	}
}
