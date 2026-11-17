package httpapi

// Soulmate COMBO orders that arrived as ONE line.
//
// The combo is now a Shopify bundle: an order arrives as its three items -
// SC-PLANK-<colour>, 3DRN (3D Rose Name) and SC-KEY - each its own registry
// product with its own .scad. But orders placed before the switch, and any
// Soulmate COMBO with LIGHT order until that is bundled too, still carry the
// whole combo as one line: SC-RED, SCWL-RED.
//
// Such a line is read as the same three items, so it is made exactly like a
// bundle order - three jobs named for their item (JOB-116590-PLANK, -ROSE,
// -KEYCHAIN), each built from its own product - and nothing depends on the
// old combo-with-parts setup any more. Only when all three item products
// exist in the registry; otherwise the line is left as it came.

import (
	"context"
	"regexp"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// legacyComboSKU matches a whole-combo SKU: SC-RED, SCWL-RED. The colour
// codes are listed rather than matched as any three letters, because SC-KEY
// - the keychain item - is three letters too.
var legacyComboSKU = regexp.MustCompile(`^SC(?:WL)?-(BLU|YEL|BLK|WHT|PNK|GLD|RED)$`)

// soulmateItem is one of the combo's items: what it is, and the SKUs it may
// be sold under for a given colour, preferred first.
type soulmateItem struct {
	part string
	skus func(colour string) []string
}

// The rose is SC-ROSE, the combo's own ₹0 part, so the combo's price lands on
// the plank line in Shopify; 3DRN (3D Rose Name, sold on its own at ₹400)
// was the combo's rose for a while and is still read.
var soulmateItems = []soulmateItem{
	{"plank", func(c string) []string { return []string{"SC-PLANK-" + c} }},
	{"rose", func(string) []string { return []string{"SC-ROSE", "3DRN"} }},
	{"keychain", func(string) []string { return []string{"SC-KEY"} }},
}

// legacyComboItems reads a whole-combo line as its three item lines, or says
// it is not one.
func (s *Server) legacyComboItems(ctx context.Context, li production.LineItem) ([]production.LineItem, bool) {
	m := legacyComboSKU.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(deref(li.SKU))))
	if m == nil {
		return nil, false
	}
	bundle := strings.TrimSpace(li.ProductName)
	if bundle == "" {
		bundle = "Soulmate COMBO"
	}
	out := make([]production.LineItem, 0, len(soulmateItems))
	for _, it := range soulmateItems {
		sku, ok := s.firstRegistered(ctx, it.skus(m[1]))
		if !ok {
			return nil, false // the items are not set up; leave the line alone
		}
		item := li
		item.SKU = &sku
		item.Bundle = bundle
		item.Properties = s.narrowLineProps(ctx, sku, li.Properties)
		out = append(out, item)
	}
	return out, true
}

// expandLegacyCombos replaces each whole-combo line with its three items.
func (s *Server) expandLegacyCombos(ctx context.Context, items []production.LineItem) []production.LineItem {
	out := make([]production.LineItem, 0, len(items))
	for _, li := range items {
		if parts, ok := s.legacyComboItems(ctx, li); ok {
			out = append(out, parts...)
			continue
		}
		out = append(out, li)
	}
	return out
}

// legacyComboItemSKU is the item product a whole-combo job prints from, for
// a job made before the items existed (SC-RED, part "rose" -> 3DRN), so it
// can still be rebuilt once the combo's own parts are gone.
func (s *Server) legacyComboItemSKU(ctx context.Context, sku, part string) (string, bool) {
	m := legacyComboSKU.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(sku)))
	if m == nil {
		return "", false
	}
	for _, it := range soulmateItems {
		if strings.EqualFold(it.part, strings.TrimSpace(part)) {
			return s.firstRegistered(ctx, it.skus(m[1]))
		}
	}
	return "", false
}

// firstRegistered is the first of skus the registry knows.
func (s *Server) firstRegistered(ctx context.Context, skus []string) (string, bool) {
	for _, sku := range skus {
		if _, err := s.store.Q.FindProductBySKU(ctx, sku); err == nil {
			return sku, true
		}
	}
	return "", false
}

// narrowLineProps keeps, of a combo line's fields, the ones an item's own
// product reads - the rose its name, not the plank's two. Everything when the
// product maps nothing yet, so nothing the customer typed is lost.
func (s *Server) narrowLineProps(ctx context.Context, sku string, props []production.LineProp) []production.LineProp {
	used := s.mappedPropertyKeys(ctx, sku)
	if len(used) == 0 {
		return props
	}
	kept := make([]production.LineProp, 0, len(props))
	for _, p := range props {
		if used[personalise.NormaliseKey(p.Name)] {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return props
	}
	return kept
}
