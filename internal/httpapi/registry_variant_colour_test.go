package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestAMissingColourFieldTakesTheVariantColour(t *testing.T) {
	maps := []personalise.FieldMap{
		{PropertyKey: "step 2 first name", ScadVariable: "name1"},
		{PropertyKey: "step 1 colour", ScadVariable: "text_color"},
	}
	variant := "BLUE"
	job := gen.ProductionJob{VariantTitle: &variant}
	older := []production.LineProp{{Name: "STEP 2 - First Name-", Value: "SINDHUSREE"}}

	got := withVariantColour(older, maps, job)
	args, err := personalise.MappedArgs(got, maps)
	if err != nil {
		t.Fatalf("an older order without the colour field still refused: %v", err)
	}
	if args["text_color"] != `"BLUE"` {
		t.Errorf("text_color = %s, want the variant's BLUE", args["text_color"])
	}
	if len(older) != 1 {
		t.Errorf("the caller's slice was changed: %v", older)
	}

	sent := append(older, production.LineProp{Name: "STEP 1 - Colour", Value: "RED"})
	args, _ = personalise.MappedArgs(withVariantColour(sent, maps, job), maps)
	if args["text_color"] != `"RED"` {
		t.Errorf("text_color = %s, want the RED the order sent", args["text_color"])
	}

	if got := withVariantColour(older, maps[:1], job); len(got) != 1 {
		t.Errorf("a product mapping no colour got one added: %v", got)
	}
}
