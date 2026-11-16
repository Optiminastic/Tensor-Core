package httpapi

// Products sold inside a Shopify bundle.
//
// A Soulmate COMBO sold as a bundle reaches Shopify's order as three lines -
// the plank, the rose and the keychain - but Shopify keeps everything the
// customer typed on the BUNDLE: every component line arrives with no fields
// of its own. The Shopify client hands each line the bundle's fields; this
// keeps, on each line, only the ones its own product reads, so the plank
// carries the two names and the colour, the rose its name and the keychain
// its name - three products, each with its own details, in one order.

import (
	"context"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// narrowBundleProps trims each bundle-fed line to the fields its registry
// product maps. A line not from a bundle, or whose product maps nothing yet,
// is returned untouched: dropping a customer's words because the registry is
// still being set up would lose them for good.
func (s *Server) narrowBundleProps(ctx context.Context, items []shopifyLineItem) []shopifyLineItem {
	for i, li := range items {
		if !li.FromBundle || strings.TrimSpace(li.SKU) == "" {
			continue
		}
		used := s.mappedPropertyKeys(ctx, li.SKU)
		if len(used) == 0 {
			continue
		}
		kept := make([]shopifyLineProp, 0, len(li.Properties))
		for _, p := range li.Properties {
			if used[personalise.NormaliseKey(p.Name)] {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			items[i].Properties = kept
		}
	}
	return items
}

// mappedPropertyKeys is every order field a SKU's registry product reads,
// normalised the way the renderer matches them. Empty when the SKU is not in
// the registry or maps nothing.
func (s *Server) mappedPropertyKeys(ctx context.Context, sku string) map[string]bool {
	product, err := s.store.Q.FindProductBySKU(ctx, strings.TrimSpace(sku))
	if err != nil {
		return nil
	}
	maps, err := s.store.Q.ListAllProductFieldMaps(ctx, product.ID)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(maps))
	for _, m := range maps {
		if key := personalise.NormaliseKey(m.PropertyKey); key != "" {
			out[key] = true
		}
	}
	return out
}
