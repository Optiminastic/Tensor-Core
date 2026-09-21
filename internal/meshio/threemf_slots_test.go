package meshio

// What the plate TELLS BambuBuddy about its filament.
//
// project_settings.config is the only colour signal an unsliced plate carries:
// BambuBuddy reads filament_type and filament_colour from it to decide which
// AMS slots a bed needs and whether a printer can take it. These tests cover
// the two ways that declaration used to go wrong on a real bed - a slot order
// that depended on where the packer happened to put things, and a material that
// was always PLA - and the agreement between the two documents that address the
// same slots from different files.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
)

// slotsOf reads the declared AMS slots back out of a written plate.
func slotsOf(t *testing.T, data []byte) (types, colours []string) {
	t.Helper()
	var got struct {
		Type   []string `json:"filament_type"`
		Colour []string `json:"filament_colour"`
	}
	cfg := entryOf(t, data, projectSettingsPath)
	if err := json.Unmarshal([]byte(cfg), &got); err != nil {
		t.Fatalf("project_settings.config is not valid JSON: %v\ngot: %s", err, cfg)
	}
	return got.Type, got.Colour
}

// A name/extruder pair, as model_settings.config writes them.
var namedExtruder = regexp.MustCompile(`key="name" value="([^"]*)"/>\s+<metadata key="extruder" value="(\d+)"`)

// An operator's own upload is ONE part, and its colour is the lettering colour -
// there is no separate white base to be first. When the packer placed such a
// model first, it took slot 1, and the bed declared the lettering as its base:
// every plank on that plate then printed inside-out.
//
// Slots are assigned from the multi-part models first for exactly this case, so
// the plank body keeps slot 1 however the packer ordered the bed.
func TestSlotOrderSurvivesASinglePartModelPlacedFirst(t *testing.T) {
	upload := []Part{{Name: "Uploaded", Colour: "#1560BD", Triangles: box(0, 0, 0, 40, 40, 10)}}

	data, _, err := Merge3MF([]PlacedModel{
		{Name: "T3DPS-1", Parts: upload},
		{Name: "T3DPS-2", Parts: plank("#1560BD"), XOffsetMM: 100},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	_, colours := slotsOf(t, data)
	if len(colours) != 2 || colours[0] != "#FFFFFF" {
		t.Errorf("filament_colour = %v, want the #FFFFFF base plate in slot 1 "+
			"even though a single-part model was placed first", colours)
	}
}

// 3dmodel.model, model_settings.config and project_settings.config address the
// same slots from three files. An extruder number in one has to be the position
// of that colour in the other, and the way they drift is by being derived twice
// - which is why they share one objectPlan.
func TestExtruderNumbersMatchTheDeclaredSlotOrder(t *testing.T) {
	data, _, err := Merge3MF([]PlacedModel{
		{Name: "T3DPS-1", Parts: plank("#E4002B")},
		{Name: "T3DPS-2", Parts: plank("#1560BD"), XOffsetMM: 100},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	_, colours := slotsOf(t, data)
	slotOf := map[string]int{}
	for i, c := range colours {
		slotOf[c] = i + 1
	}

	// Read back the objects in the order the model file writes them, so a part's
	// colour can be matched to the extruder model_settings gives it.
	parts, err := Read3MF(data)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	pairs := namedExtruder.FindAllStringSubmatch(entryOf(t, data, bambuSettingsPath), -1)
	if len(pairs) == 0 {
		t.Fatal("model_settings.config declared no extruders")
	}

	// The part entries follow their object entry, so filter to the ones whose
	// name is a part name rather than a model name.
	byName := map[string]string{}
	for _, p := range pairs {
		byName[p[1]] = p[2]
	}
	for _, p := range parts {
		want := slotOf[p.Colour]
		if want == 0 {
			t.Errorf("part %q has colour %s, which no slot declares", p.Name, p.Colour)
			continue
		}
		if got := byName[p.Name]; got != "" && got != strconv.Itoa(want) {
			t.Errorf("part %q (%s) prints on extruder %s, but its colour is slot %d",
				p.Name, p.Colour, got, want)
		}
	}
}

// A bed is one filament load, so the material the jobs record has to reach every
// slot. It used to be dropped entirely and every plate declared PLA - harmless
// while the shop prints only PLA, and a wrong spool request the day it does not.
func TestDeclaredMaterialReachesEverySlot(t *testing.T) {
	parts := plank("#1560BD")
	for i := range parts {
		parts[i].Material = "PETG"
	}

	data, _, err := Merge3MF([]PlacedModel{{Name: "T3DPS-1", Parts: parts}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	types, colours := slotsOf(t, data)
	if len(types) != len(colours) {
		t.Fatalf("filament_type = %v and filament_colour = %v are different lengths", types, colours)
	}
	for i, got := range types {
		if got != "PETG" {
			t.Errorf("slot %d declares %q, want PETG - the bed's own material", i+1, got)
		}
	}
}

// ReadPlateSlots must agree with what plateProjectSettings wrote, because the
// send path uses it to decide which spool each slot prints from. If the reader
// and the writer disagree about order, the plate is sliced with the lettering
// colour in the body's slot - which looks like a successful send and prints an
// inside-out plank.
func TestReadPlateSlotsRoundTripsTheWrittenDeclaration(t *testing.T) {
	data, _, err := Merge3MF([]PlacedModel{
		{Name: "T3DPS-1", Parts: plank("#2850E0")},
		{Name: "T3DPS-2", Parts: plank("#2850E0"), XOffsetMM: 100},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	slots, err := ReadPlateSlots(data)
	if err != nil {
		t.Fatalf("ReadPlateSlots: %v", err)
	}

	_, colours := slotsOf(t, data)
	if len(slots) != len(colours) {
		t.Fatalf("read %d slots but the plate declares %d (%v)", len(slots), len(colours), colours)
	}
	for i, want := range colours {
		if slots[i].Colour != want {
			t.Errorf("slot %d reads %q, but the plate declares %q", i+1, slots[i].Colour, want)
		}
	}
	if len(slots) == 0 || slots[0].Colour != "#FFFFFF" {
		t.Errorf("slot 1 = %v, want the #FFFFFF plank body - the rule planObjects "+
			"applies and nothing outside meshio knows", slots)
	}
	for i, s := range slots {
		if s.Material == "" {
			t.Errorf("slot %d has no material; a slicer cannot act on an empty filament type", i+1)
		}
	}
}

// The reader has to agree with the writer in the awkward case too, not just the
// tidy one: a single-part upload placed first still leaves the body in slot 1.
func TestReadPlateSlotsKeepsTheBodyFirstWhenAnUploadIsPlacedFirst(t *testing.T) {
	upload := []Part{{Name: "Uploaded", Colour: "#1560BD", Triangles: box(0, 0, 0, 40, 40, 10)}}

	data, _, err := Merge3MF([]PlacedModel{
		{Name: "T3DPS-1", Parts: upload},
		{Name: "T3DPS-2", Parts: plank("#1560BD"), XOffsetMM: 100},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	slots, err := ReadPlateSlots(data)
	if err != nil {
		t.Fatalf("ReadPlateSlots: %v", err)
	}
	if len(slots) != 2 || slots[0].Colour != "#FFFFFF" {
		t.Errorf("slots = %v, want the #FFFFFF base in slot 1", slots)
	}
}

// A file that states no filament requirements is a real thing - an operator can
// upload a 3MF from anywhere - and it is the CALLER's job to refuse it with a
// reason. Returning an error here would turn "this file says nothing" into
// "this file is broken".
func TestReadPlateSlotsReportsNoSlotsRatherThanFailing(t *testing.T) {
	data, err := WriteModels3MF([]Model{{Name: "Plank", Parts: plank("#1560BD")}})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadPlateSlots(data); err != nil {
		t.Fatalf("ReadPlateSlots on a normal plate: %v", err)
	}

	if _, err := ReadPlateSlots([]byte("not a zip")); err == nil {
		t.Error("ReadPlateSlots accepted something that is not a 3MF")
	}
}
