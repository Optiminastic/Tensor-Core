package httpapi

// A bulk job's personalisation has to read exactly like a storefront line's,
// because every reader downstream was written for that shape.
//
// This is the bug the first version shipped: the properties were stored as a
// MAP keyed by OpenSCAD variable ({"NAME_L": "ABB"}), and the two readers that
// matter both rejected it - jobLineProperties decodes a LIST of {name, value},
// and ParamsFromProperties matches labels like "left name", not variables. Ten
// jobs were created carrying every name they needed and none could render.

import (
	"encoding/json"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bulkPropsFor is what insertBulkJob now writes, built from a sheet row.
func bulkPropsFor(t *testing.T, values map[string]string) []byte {
	t.Helper()
	props := make([]production.LineProp, 0, len(values))
	for name, value := range values {
		props = append(props, production.LineProp{Name: name, Value: value})
	}
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBulkJobPersonalisationReadsBackLikeAStorefrontLine(t *testing.T) {
	// A row from the default (unmapped) sheet: the shape a DNP bulk order has.
	row := map[string]string{"left name": "AMENA", "right name": "KABIR", "hearts": "2"}
	job := gen.ProductionJob{
		JobNumber:                 "ABB-1",
		PersonalisationProperties: bulkPropsFor(t, row),
	}

	props, ok := jobLineProperties(job)
	if !ok {
		t.Fatal("the job's own properties did not decode; the renderer would fall back to an " +
			"order a bulk job does not have")
	}

	params, err := personalise.ParamsFromProperties(props)
	if err != nil {
		t.Fatalf("the plank renderer refused them: %v", err)
	}
	if params.NameLeft != "AMENA" || params.NameRight != "KABIR" {
		t.Errorf("names came back as %q / %q, want AMENA / KABIR", params.NameLeft, params.NameRight)
	}
	if params.Hearts != 2 {
		t.Errorf("hearts = %d, want 2", params.Hearts)
	}
}

// The shape that shipped first, pinned so it cannot come back.
func TestVariableKeyedPropertiesDoNotRender(t *testing.T) {
	raw, err := json.Marshal(map[string]string{"NAME_L": "AMENA", "NAME_R": "KABIR"})
	if err != nil {
		t.Fatal(err)
	}
	job := gen.ProductionJob{JobNumber: "ABB-1", PersonalisationProperties: raw}
	if _, ok := jobLineProperties(job); ok {
		t.Error("a map of variables decoded as line properties; it must not, or the bug " +
			"returns silently")
	}
}
