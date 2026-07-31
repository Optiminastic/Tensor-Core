package production

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

func smallJob(id, material string) PlanJob {
	return PlanJob{
		ID: id, JobNumber: "JOB-" + id, Material: material, Quantity: 1,
		Footprint: bedpack.UnitFootprint{RefID: id, XMM: 50, YMM: 50, ZMM: 20},
	}
}

func TestPlanNoFootprintIsUnbatchable(t *testing.T) {
	j := smallJob("a", "PLA")
	j.Footprint = bedpack.UnitFootprint{}
	batches, unb := Plan([]PlanJob{j})
	if len(batches) != 0 || len(unb) != 1 {
		t.Fatalf("batches=%d unbatchable=%d, want 0/1", len(batches), len(unb))
	}
	if unb[0].Reason != "No print file with measurable STL dimensions uploaded yet." {
		t.Errorf("reason = %q", unb[0].Reason)
	}
}

func TestPlanSameKeyShareOneBatch(t *testing.T) {
	batches, unb := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PLA")})
	if len(unb) != 0 {
		t.Fatalf("unexpected unbatchable: %+v", unb)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1 (both small, same key)", len(batches))
	}
	if batches[0].UnitsPerBed != 2 {
		t.Errorf("units per bed = %d, want 2", batches[0].UnitsPerBed)
	}
}

func TestPlanDifferentMaterialSplits(t *testing.T) {
	batches, _ := Plan([]PlanJob{smallJob("a", "PLA"), smallJob("b", "PETG")})
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (different material key)", len(batches))
	}
}

func TestPlanDueDateClusteringSplits(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	far := base.Add(10 * 24 * time.Hour)
	a := smallJob("a", "PLA")
	a.DueDate = &base
	b := smallJob("b", "PLA")
	b.DueDate = &far
	batches, _ := Plan([]PlanJob{a, b})
	if len(batches) != 2 {
		t.Errorf("batches = %d, want 2 (due dates 10 days apart)", len(batches))
	}
}

func TestPlanOversizedJobIsUnbatchable(t *testing.T) {
	// Two near-max units of one job cannot share a bed, and jobs never split, so the
	// job is unbatchable.
	j := PlanJob{
		ID: "big", JobNumber: "JOB-big", Material: "PLA", Quantity: 2,
		Footprint: bedpack.UnitFootprint{RefID: "big", XMM: 290, YMM: 310, ZMM: 20},
	}
	batches, unb := Plan([]PlanJob{j})
	if len(batches) != 0 || len(unb) != 1 {
		t.Fatalf("batches=%d unbatchable=%d, want 0/1", len(batches), len(unb))
	}
	if unb[0].Reason != "Exceeds the print bed's capacity even on its own." {
		t.Errorf("reason = %q", unb[0].Reason)
	}
}

func TestPlanEffectiveTimePerUnit(t *testing.T) {
	mins := 120
	a := smallJob("a", "PLA")
	a.EstimatedMinutes = &mins
	a.Quantity = 2
	batches, _ := Plan([]PlanJob{a})
	if len(batches) != 1 {
		t.Fatalf("want 1 batch, got %d", len(batches))
	}
	b := batches[0]
	if b.TotalPrintTimeMinutes == nil || *b.TotalPrintTimeMinutes != 120 {
		t.Errorf("total time = %v, want 120", b.TotalPrintTimeMinutes)
	}
	if b.EffectiveTimePerUnitMinutes == nil || *b.EffectiveTimePerUnitMinutes != 60 {
		t.Errorf("effective/unit = %v, want 60", b.EffectiveTimePerUnitMinutes)
	}
}
