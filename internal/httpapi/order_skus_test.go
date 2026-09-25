package httpapi

import (
	"reflect"
	"testing"
)

func TestSkusForTheLiveShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want []string
	}{
		{
			// The ordinary case: one plank, one SKU.
			name: "one line",
			raw:  `[{"sku":"DNP-BLU","product_name":"Dual Name Plank"}]`,
			want: []string{"DNP-BLU"},
		},
		{
			// An order of four planks in one colour says the SKU once. Saying
			// it four times is the payload this whole file exists to avoid.
			name: "repeated sku collapses",
			raw:  `[{"sku":"DNP-BLU"},{"sku":"DNP-BLU"},{"sku":"DNP-BLU"}]`,
			want: []string{"DNP-BLU"},
		},
		{
			// Sorted, so two orders holding the same lines in a different
			// order render identically instead of looking unlike each other.
			name: "mixed skus are sorted",
			raw:  `[{"sku":"SCWL-RED"},{"sku":"DNP-BLU"},{"sku":"PDNP-GLD"}]`,
			want: []string{"DNP-BLU", "PDNP-GLD", "SCWL-RED"},
		},
		{
			// Most of the catalogue was blank until recently, and a row
			// reading ", , DNP-BLU" says less than one reading "DNP-BLU".
			name: "blank skus contribute nothing",
			raw:  `[{"sku":""},{"sku":"   "},{"sku":"DNP-BLU"}]`,
			want: []string{"DNP-BLU"},
		},
		{
			name: "every line blank",
			raw:  `[{"sku":""},{"sku":""}]`,
			want: nil,
		},
		{
			name: "no line items at all",
			raw:  ``,
			want: nil,
		},
		{
			// Unreadable line items lose the column for that order, never the
			// page. Same choice personalisationNamesFor makes.
			name: "unreadable json",
			raw:  `{"not":"an array"}`,
			want: nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := skusFor([]byte(c.raw))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("skusFor(%s) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}
