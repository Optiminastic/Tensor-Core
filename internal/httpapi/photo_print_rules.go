package httpapi

// What size a product's customer photo prints at, as rules.
//
// A product's sizes come from the registry (registry_photo_print.go) when it
// has any, and otherwise from the sizes Tensor shipped with, written below as
// the same rules: one matcher serves both, and the registry page shows the
// built-in rules for a product that has not been given its own.
//
// A rule with a when_field applies when the order line's field of that name
// has that value (case-insensitive) - the Family Crossword's photo depends on
// how many frames were bought. A rule without one is the default.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// photoRule is one size a product's photo can print at.
type photoRule struct {
	WidthMM   float64 `json:"width_mm"`
	HeightMM  float64 `json:"height_mm"`
	Shape     string  `json:"shape"`
	WhenField string  `json:"when_field"`
	WhenValue string  `json:"when_value"`
}

const (
	shapeRect   = "rect"
	shapeCircle = "circle"
)

// builtinPhotoRules are the sizes measured off the shop's sample sheet
// (3d printing store image size.pdf), by SKU prefix.
var builtinPhotoRules = []struct {
	prefix string
	rules  []photoRule
}{
	{"T3DPS-DUO", []photoRule{{WidthMM: 65, HeightMM: 105, Shape: shapeRect}}},
	{"PFM", []photoRule{{WidthMM: 56.6, HeightMM: 59.7, Shape: shapeRect}}},
	{"FCF", []photoRule{
		{WidthMM: 106, HeightMM: 80, Shape: shapeRect, WhenField: "photo_frames", WhenValue: "3"},
		{WidthMM: 89, HeightMM: 67, Shape: shapeRect, WhenField: "photo_frames", WhenValue: "4"},
		{WidthMM: 71, HeightMM: 54, Shape: shapeRect, WhenField: "photo_frames", WhenValue: "6"},
	}},
	{"TSM", []photoRule{
		{WidthMM: 51, HeightMM: 51, Shape: shapeCircle, WhenField: "batch_size", WhenValue: "Large"},
		{WidthMM: 41, HeightMM: 41, Shape: shapeCircle},
	}},
}

// builtinRulesForSKU are the shipped sizes for a SKU, if it has any.
func builtinRulesForSKU(sku string) []photoRule {
	sku = strings.ToUpper(strings.TrimSpace(sku))
	for _, b := range builtinPhotoRules {
		if sku != "" && strings.HasPrefix(sku, b.prefix) {
			return b.rules
		}
	}
	return nil
}

// rulesFromRows turns registry rows into rules.
func rulesFromRows(rows []gen.ProductPhotoSize) []photoRule {
	out := make([]photoRule, len(rows))
	for i, r := range rows {
		out[i] = photoRule{WidthMM: r.WidthMm, HeightMM: r.HeightMm, Shape: r.Shape,
			WhenField: r.WhenField, WhenValue: r.WhenValue}
	}
	return out
}

// matchPhotoRule picks the size for one order line: the first rule whose
// field matches, else the default. problem names the field values when
// neither applies, so the operator knows which size the registry is missing.
func matchPhotoRule(rules []photoRule, li production.LineItem) (photoSpec, string) {
	var fallback *photoRule
	asked := map[string]string{}
	for i := range rules {
		r := rules[i]
		field := strings.TrimSpace(r.WhenField)
		if field == "" {
			if fallback == nil {
				fallback = &rules[i]
			}
			continue
		}
		got := lineProp(li, normalisePropKey(field))
		asked[field] = got
		if strings.EqualFold(got, strings.TrimSpace(r.WhenValue)) {
			return specOf(r), ""
		}
	}
	if fallback != nil {
		return specOf(*fallback), ""
	}
	parts := make([]string, 0, len(asked))
	for f, v := range asked {
		parts = append(parts, fmt.Sprintf("%s %q", f, v))
	}
	sort.Strings(parts)
	return photoSpec{}, "has " + strings.Join(parts, ", ") + "; no photo size is set for that"
}

func specOf(r photoRule) photoSpec {
	if r.Shape == shapeCircle {
		return photoSpec{w: r.WidthMM, h: r.WidthMM, circle: true}
	}
	return photoSpec{w: r.WidthMM, h: r.HeightMM}
}
