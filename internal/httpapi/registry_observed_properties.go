package httpapi

// What the customers of ONE product have actually sent.
//
// The left-hand side of the mapping editor. Without it somebody has to type
// "step 4 first name" exactly, from memory, having first worked out that the
// storefront's "STEP 4-First Name-:" normalises to that - and a key typed one
// character wrong matches nothing, silently, in a way that looks exactly like
// an order carrying no personalisation at all.
//
// It is also what makes a mapping proposable at all: registry_field_suggest.go
// pairs these labels against the variables a .scad declares, so configuring a
// product becomes reviewing a list rather than writing one.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// observedPropertyResponse is a field the customers have actually sent.
type observedPropertyResponse struct {
	// Key is normalised - what a mapping must be written against.
	Key string `json:"key"`
	// Label is the storefront's own wording, for recognising it. The same
	// field is labelled five ways across five products, so the raw label is
	// what a person recognises and the normalised key is what matches.
	Label string `json:"label"`
	// Sample is one real answer, which settles "is this the name or the
	// number?" faster than any documentation.
	Sample string `json:"sample"`
	// Orders is how many of this product's recent lines carried it. A field
	// on one line in two hundred is a retired option rather than something to
	// map, so it sorts to the bottom.
	Orders int `json:"orders"`
}

// observedPropertyLineLimit bounds how far back the editor looks.
//
// Lines OF THIS PRODUCT, not orders of anything - see
// ListRecentOrderLineItemsMatching. Enough to cover every option a live
// product offers, few enough that the query stays a page load rather than a
// report.
const observedPropertyLineLimit = 200

// observedProperties answers the HTTP read.
func (s *Server) observedProperties(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.observedPropertiesFor(c.Request.Context(), product))
}

// observedPropertiesFor is the same answer for a caller that already has the
// product - the suggester and the drift check both need it, and a second
// reading of these orders could disagree with this one about what a label is.
//
// Never nil, so a caller can range over it and a JSON response is [] rather
// than null.
func (s *Server) observedPropertiesFor(
	ctx context.Context, product gen.Product,
) []observedPropertyResponse {
	empty := []observedPropertyResponse{}

	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		return empty
	}
	skus := make([]string, 0, len(variants))
	for _, v := range variants {
		if v.Sku != nil && strings.TrimSpace(*v.Sku) != "" {
			skus = append(skus, strings.ToLower(strings.TrimSpace(*v.Sku)))
		}
	}
	// A product with no SKU at all still has a name, and the name match below
	// is the only thing that can find its orders - which is the case for
	// every product configured before its SKUs were entered. An empty SKU
	// list is passed to the query as an empty array, which matches nothing and
	// leaves the name half to do the work.
	known := make(map[string]bool, len(skus))
	for _, sku := range skus {
		known[sku] = true
	}

	name := strings.TrimSpace(product.Name)
	if len(skus) == 0 && name == "" {
		return empty
	}

	docs, err := s.store.Q.ListRecentOrderLineItemsMatching(
		ctx, gen.ListRecentOrderLineItemsMatchingParams{
			Skus: skus,
			// Broad on purpose. matchesProduct below is the narrow rule and
			// the authority; this only narrows which rows are read.
			NamePrefix:       strings.ToLower(name) + "%",
			ShopifyProductID: deref(product.ShopifyProductID),
			LimitCount:       observedPropertyLineLimit,
		})
	if err != nil {
		return empty
	}

	found := map[string]*observedPropertyResponse{}
	for _, doc := range docs {
		var lines []production.LineItem
		// An order whose line items will not parse loses its contribution to
		// this list, never the page: this is a suggestion box, and one
		// unreadable row is not worth refusing the whole screen over.
		if err := json.Unmarshal(doc, &lines); err != nil {
			continue
		}
		for _, line := range lines {
			if !matchesProduct(line, name, known) && !sameShopifyProduct(line, product) {
				continue
			}
			collectProperties(line, found)
		}
	}

	out := make([]observedPropertyResponse, 0, len(found))
	for _, v := range found {
		out = append(out, *v)
	}
	// Commonest first: a field on one line in two hundred is a retired option
	// rather than something to map, and it should not sit above the name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// collectProperties folds one line's answers into the running tally.
func collectProperties(line production.LineItem, found map[string]*observedPropertyResponse) {
	for _, p := range line.Properties {
		if p.Hidden() || strings.TrimSpace(p.Value) == "" {
			continue
		}
		key := personalise.NormaliseKey(p.Name)
		if key == "" {
			continue
		}
		if seen, ok := found[key]; ok {
			seen.Orders++
			continue
		}
		found[key] = &observedPropertyResponse{
			Key: key, Label: strings.TrimSpace(p.Name),
			Sample: strings.TrimSpace(p.Value), Orders: 1,
		}
	}
}

// matchesProduct decides whether an order line belongs to this product.
//
// By SKU first, which is the real answer and the only one the render path
// uses.
//
// By product NAME as a fallback, because every SKU in this shop is newer than
// the orders it should describe. SCWL's seven SKUs were added last week; not
// one order carries them, so a SKU-only match offers an empty dropdown for
// every product somebody is trying to configure - the exact moment the list
// is needed.
//
// The name match is deliberately narrow. The storefront sells "Soulmate COMBO
// with LIGHT - BLUE", so the colour suffix is cut and the rest must equal the
// registry's name EXACTLY. A prefix match would be a trap: "Dual Name Plank"
// is a prefix of "Dual Name Plank with Light", and DNP would quietly absorb
// DNPWL's fields and offer somebody a mapping built from another product's
// orders.
// sameShopifyProduct reports whether a line is of the Shopify product this
// registry product was imported from - the only reliable key for a line with
// no SKU, whose name carries its options and matches no name rule.
func sameShopifyProduct(line production.LineItem, product gen.Product) bool {
	id := strings.TrimSpace(deref(product.ShopifyProductID))
	return id != "" && strings.TrimSpace(line.ProductID) == id
}

func matchesProduct(line production.LineItem, productName string, skus map[string]bool) bool {
	if line.SKU != nil && skus[strings.ToLower(strings.TrimSpace(*line.SKU))] {
		return true
	}
	want := strings.TrimSpace(productName)
	if want == "" {
		return false
	}
	got := strings.TrimSpace(line.ProductName)
	if strings.EqualFold(got, want) {
		return true
	}
	// "<product> - <colour>". LastIndex, so a product whose own name contains
	// " - " keeps it and only the trailing option is removed.
	if i := strings.LastIndex(got, " - "); i > 0 {
		return strings.EqualFold(strings.TrimSpace(got[:i]), want)
	}
	return false
}
