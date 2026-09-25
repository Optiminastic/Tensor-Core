package httpapi

// The SKUs an order is for, carried on the orders LIST.
//
// Same reasoning as order_search_names.go, and the same constraint: the list
// response deliberately does not ship line_items (shipping every row's jsonb to
// render one integer multiplies the payload for nothing), so a page that wants
// to show or search by SKU has nowhere to look. This ships the SKUs ALONE - a
// handful of short strings per order - which keeps that decision intact.
//
// Worth having because the SKU is the only thing that says WHICH product a line
// is, precisely enough to act on. A product name is renamed freely by the
// storefront and several read almost alike: "Dual Name Plank", "PREMIUM DUAL
// NAME PLANK" and "Dual Name Plank with Light" are three different products,
// three different templates and three different prices, and on the orders page
// they are three similar strings. DNP-BLU, PDNP-BLU and DNPWL-BLU are not.

import (
	"encoding/json"
	"sort"
	"strings"
)

// skusFor returns the distinct SKUs on an order's line items.
//
// Sorted and deduplicated: an order carrying four planks of one SKU should say
// that SKU once, and two orders holding the same lines in a different order
// should render identically rather than looking like different orders.
//
// A line with no SKU contributes nothing rather than an empty string. Blank is
// the state most of the catalogue was in until recently, and a row reading ", ,
// DNP-BLU" says less than one reading "DNP-BLU".
func skusFor(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var lines []struct {
		SKU string `json:"sku"`
	}
	if err := json.Unmarshal(raw, &lines); err != nil {
		// Unreadable line items are not an error here: the page still renders,
		// it simply cannot offer this column for that order. The same choice
		// personalisationNamesFor makes.
		return nil
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		sku := strings.TrimSpace(line.SKU)
		if sku == "" || seen[sku] {
			continue
		}
		seen[sku] = true
		out = append(out, sku)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
