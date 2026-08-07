package bedpack

import (
	"math"
	"testing"
)

func TestPackSingleUnitAtMarginInset(t *testing.T) {
	placements, rejected := Pack([]UnitFootprint{{RefID: "a", XMM: 100, YMM: 100, ZMM: 50}})
	if len(rejected) != 0 || len(placements) != 1 {
		t.Fatalf("placed=%d rejected=%d, want 1/0", len(placements), len(rejected))
	}
	p := placements[0]
	if p.XOffsetMM != EdgeMarginMM || p.YOffsetMM != EdgeMarginMM || p.Rotated {
		t.Errorf("placement = %+v, want (%v,%v), not rotated", p, EdgeMarginMM, EdgeMarginMM)
	}
}

func TestPackRejectsTooTall(t *testing.T) {
	_, rejected := Pack([]UnitFootprint{{RefID: "z", XMM: 50, YMM: 50, ZMM: BedZMM + 1}})
	if len(rejected) != 1 {
		t.Errorf("a too-tall unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRejectsTooWide(t *testing.T) {
	// 350 mm wide cannot fit even alone (needs 360 with the gap, and the
	// margin-inset usable width is only 310).
	_, rejected := Pack([]UnitFootprint{{RefID: "w", XMM: 350, YMM: 50, ZMM: 10}})
	if len(rejected) != 1 {
		t.Errorf("an over-wide unit should be rejected, got %d rejected", len(rejected))
	}
}

func TestPackRotatesToFit(t *testing.T) {
	// Usable placement envelope (after the edge margin) is 310x300. "a"
	// (100x290) claims a strip, leaving a 200x300 leftover to one side. "b"
	// (250x100) does not fit that leftover at 0 deg (needs 260 wide) but
	// does turned 90 degrees (needs 110x260, which the 200x300 leftover has
	// room for).
	placements, rejected := Pack([]UnitFootprint{
		{RefID: "a", XMM: 100, YMM: 290, ZMM: 20},
		{RefID: "b", XMM: 250, YMM: 100, ZMM: 20},
	})
	if len(rejected) != 0 || len(placements) != 2 {
		t.Fatalf("placed=%d rejected=%d, want 2/0", len(placements), len(rejected))
	}
	if placements[0].Rotated {
		t.Error("a should not need rotation")
	}
	if !placements[1].Rotated {
		t.Error("b should have been rotated to fit the leftover strip")
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
		{RefID: "big", XMM: 290, YMM: 290, ZMM: 20},
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
	want := math.Round(10000.0/105600.0*100*100) / 100 // 9.47
	if got != want {
		t.Errorf("utilisation = %v, want %v", got, want)
	}
	if UtilisationPercent(nil) != 0 {
		t.Error("empty utilisation should be 0")
	}
}

func TestUtilisationAreaStats(t *testing.T) {
	stats := Utilisation([]UnitFootprint{{XMM: 100, YMM: 100}})
	if stats.OccupiedMM2 != 10000 {
		t.Errorf("occupied = %v, want 10000", stats.OccupiedMM2)
	}
	if stats.FreeMM2 != bedAreaMM2-10000 {
		t.Errorf("free = %v, want %v", stats.FreeMM2, bedAreaMM2-10000)
	}
	if stats.Percent != UtilisationPercent([]UnitFootprint{{XMM: 100, YMM: 100}}) {
		t.Errorf("percent = %v, want it to match UtilisationPercent", stats.Percent)
	}

	empty := Utilisation(nil)
	if empty.OccupiedMM2 != 0 || empty.FreeMM2 != bedAreaMM2 || empty.Percent != 0 {
		t.Errorf("empty stats = %+v, want occupied=0 free=%v percent=0", empty, bedAreaMM2)
	}
}
