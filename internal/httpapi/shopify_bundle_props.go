package httpapi

// Products sold inside a Shopify bundle.
//
// A Soulmate COMBO sold as a bundle reaches Shopify's order as three lines -
// the plank, the rose and the keychain - each holding EVERY field the
// customer typed for the whole combo (a variant bundle copies them onto each
// line; a product bundle leaves them on the bundle and the Shopify client
// hands them down). This keeps, on each line, only the ones its own product
// reads, so the plank carries the two names and the colour, the rose its name
// and the keychain its name - three products, each with its own details, in
// one order.

import (
	"context"
	"strings"

	"github.com/google/uuid"

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
		slots := slotNumbers(used)
		kept := make([]shopifyLineProp, 0, len(li.Properties))
		for _, p := range li.Properties {
			if keepsField(used, slots, p.Name) {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			items[i].Properties = kept
		}
	}
	return items
}

// slotNumbers is the slot a bundle line is, read off the fields it maps: a
// Photo Fridge Magnet's PFM-2 maps "colour 2" and "text 2", so it is slot 2.
// Empty for a product whose fields carry no number (the Soulmate items).
func slotNumbers(used map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key := range used {
		if n := trailingNumber(key); n != "" {
			out[n] = true
		}
	}
	return out
}

// keepsField reports whether a bundle line keeps one of the order's fields:
// the fields its product maps, and - for a numbered slot - that slot's other
// fields too. The magnet's design reads only the colour and the caption, but
// its line is where the floor finds the photo, so PHOTO - 2 stays on PFM-2
// beside colour 2 and text 2, and leaves every other magnet's line.
func keepsField(used, slots map[string]bool, name string) bool {
	key := personalise.NormaliseKey(name)
	if used[key] {
		return true
	}
	n := trailingNumber(key)
	return n != "" && slots[n] && !strings.HasPrefix(key, "_")
}

// trailingNumber is the number a normalised field name ends in ("photo 2"
// -> "2"), or "" when its last word is not a number.
func trailingNumber(key string) string {
	word := key
	if i := strings.LastIndex(key, " "); i >= 0 {
		word = key[i+1:]
	}
	if word == "" || word == key {
		return ""
	}
	for _, r := range word {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return word
}

// mappedPropertyKeys is every order field a SKU reads, normalised the way the
// renderer matches them. Empty when the SKU is not in the registry or maps
// nothing.
//
// Asked per SKU, the way the renderer asks (fieldMapsFor: the SKU's own
// mapping, else the product's). A Photo Fridge Magnet pack arrives as one
// line per magnet - PFM-1 ... PFM-5, all of one product - and each slot reads
// only its own fields (PFM-2: COLOUR - 2, PHOTO - 2, TEXT - 2), which only a
// per-SKU mapping can say. Read product-wide, every slot would keep all five.
func (s *Server) mappedPropertyKeys(ctx context.Context, sku string) map[string]bool {
	sku = strings.TrimSpace(sku)
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, role := range s.mappedRoles(ctx, product.ID) {
		for _, m := range s.fieldMapsFor(ctx, product.ID, sku, role) {
			if key := personalise.NormaliseKey(m.PropertyKey); key != "" {
				out[key] = true
			}
		}
	}
	return out
}

// mappedRoles is every part a product's fields can be mapped against: its
// named parts, its design files' roles and the roles its own mapping uses. A
// product with no design file yet (the magnet) still has its parts.
func (s *Server) mappedRoles(ctx context.Context, productID uuid.UUID) []string {
	seen := map[string]bool{}
	var roles []string
	add := func(role string) {
		role = roleOrBody(role)
		if !seen[strings.ToLower(role)] {
			seen[strings.ToLower(role)] = true
			roles = append(roles, role)
		}
	}
	if parts, err := s.store.Q.ListProductParts(ctx, productID); err == nil {
		for _, p := range parts {
			add(p.Role)
		}
	}
	for _, role := range s.designRolesForProduct(ctx, productID) {
		add(role)
	}
	if maps, err := s.store.Q.ListAllProductFieldMaps(ctx, productID); err == nil {
		for _, m := range maps {
			add(m.Role)
		}
	}
	add(designRoleBody)
	return roles
}

// bundleTitle is the bundle a line was sold in, for naming its job. A variant
// bundle's lines can arrive flagged with no group title; they are still
// bundle lines.
func bundleTitle(li shopifyLineItem) string {
	if t := strings.TrimSpace(li.Bundle); t != "" {
		return t
	}
	if li.FromBundle {
		return "bundle"
	}
	return ""
}
