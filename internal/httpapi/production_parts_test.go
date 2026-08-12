package httpapi

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func strptr(s string) *string { return &s }

// partJobFromBase turns a product's base job into one part-job. It must reset the
// identity (fresh id + job number), print exactly one part (quantity 1), and let a
// part's own file / material / colour override the product's, falling back to the
// product otherwise.
func TestPartJobFromBase(t *testing.T) {
	template := uuid.New()
	base := gen.InsertProductionJobParams{
		ID:          uuid.New(),
		JobNumber:   "JOB-BASE00",
		Description: "Moon Lamp",
		Quantity:    3,
		Material:    strptr("PLA"),
		Colour:      strptr("white"),
		PrintFileID: &template,
	}

	// A plain part with no overrides: falls back to the product's file and material,
	// and (being the only one of its kind) is labelled by role alone.
	body := gen.DesignPart{Role: "body", Quantity: 1}
	got, err := partJobFromBase(base, body, template, 1)
	if err != nil {
		t.Fatalf("partJobFromBase: %v", err)
	}
	if got.ID == base.ID {
		t.Error("part-job must get a fresh id, not reuse the base's")
	}
	if got.JobNumber == "" || got.JobNumber == base.JobNumber {
		t.Errorf("part-job must get a fresh job number, got %q", got.JobNumber)
	}
	if got.Quantity != 1 {
		t.Errorf("part-job quantity = %d, want 1", got.Quantity)
	}
	if got.FilamentGramsRequired != nil {
		t.Error("per-part filament must be unset until slicing meters parts")
	}
	if got.Description != "Moon Lamp - body" {
		t.Errorf("description = %q, want %q", got.Description, "Moon Lamp - body")
	}
	if got.PrintFileID == nil || *got.PrintFileID != template {
		t.Errorf("print file should fall back to the product template")
	}
	if got.Material == nil || *got.Material != "PLA" {
		t.Errorf("material should fall back to the product's PLA, got %v", got.Material)
	}

	// A duplicated part with its own file/material: overrides win, and the instance
	// number disambiguates the identical copies.
	partFile := uuid.New()
	leg := gen.DesignPart{
		Role: "leg", Quantity: 2,
		PrintFileID: &partFile, Material: strptr("PETG"), Colour: strptr("black"),
	}
	got2, err := partJobFromBase(base, leg, template, 2)
	if err != nil {
		t.Fatalf("partJobFromBase: %v", err)
	}
	if got2.Description != "Moon Lamp - leg #2" {
		t.Errorf("description = %q, want %q", got2.Description, "Moon Lamp - leg #2")
	}
	if got2.PrintFileID == nil || *got2.PrintFileID != partFile {
		t.Error("part's own print file should override the product template")
	}
	if got2.Material == nil || *got2.Material != "PETG" {
		t.Errorf("part material override = %v, want PETG", got2.Material)
	}
	if got2.Colour == nil || *got2.Colour != "black" {
		t.Errorf("part colour override = %v, want black", got2.Colour)
	}
}
