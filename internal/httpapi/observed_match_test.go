package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func line(sku, name string) production.LineItem {
	if sku == "" {
		return production.LineItem{ProductName: name}
	}
	return production.LineItem{SKU: &sku, ProductName: name}
}

func TestMatchesProduct(t *testing.T) {
	skus := map[string]bool{"scwl-red": true, "scwl-blu": true}

	for _, c := range []struct {
		name    string
		line    production.LineItem
		product string
		want    bool
	}{
		{
			// The real answer, and the only one the render path uses.
			name: "by SKU", line: line("SCWL-RED", "anything"),
			product: "Soulmate COMBO with LIGHT", want: true,
		},
		{
			name: "SKU case does not matter", line: line("scwl-BLU", ""),
			product: "Soulmate COMBO with LIGHT", want: true,
		},
		{
			// Every SKU in this shop is newer than the orders it describes:
			// not one order carries SCWL-*, so without this the dropdown is
			// empty at the moment somebody needs it.
			name: "by name with a colour suffix", line: line("", "Soulmate COMBO with LIGHT - BLUE"),
			product: "Soulmate COMBO with LIGHT", want: true,
		},
		{
			name: "by name exactly", line: line("", "Soulmate COMBO with LIGHT"),
			product: "Soulmate COMBO with LIGHT", want: true,
		},
		{
			// The trap a prefix match would fall into. These are two products,
			// two templates and two prices, and DNP absorbing DNPWL's fields
			// would offer a mapping built from another product's orders.
			name:    "a longer product name is NOT this one",
			line:    line("", "Dual Name Plank with Light - BLUE"),
			product: "Dual Name Plank", want: false,
		},
		{
			name: "unrelated product", line: line("", "Photo Fridge Magnet - RED"),
			product: "Soulmate COMBO with LIGHT", want: false,
		},
		{
			// An old order for this product that carries a stale SKU still
			// matches, because the name is what it has.
			name: "stale SKU, right name", line: line("T3DPS-DNP-7", "Soulmate COMBO with LIGHT - RED"),
			product: "Soulmate COMBO with LIGHT", want: true,
		},
		{
			name: "no product name to match on", line: line("", "Soulmate COMBO with LIGHT - RED"),
			product: "", want: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := matchesProduct(c.line, c.product, skus); got != c.want {
				t.Errorf("matchesProduct(%q, %q) = %v, want %v",
					c.line.ProductName, c.product, got, c.want)
			}
		})
	}
}
