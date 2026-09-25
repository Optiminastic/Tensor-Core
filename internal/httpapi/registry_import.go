package httpapi

// Importing a product from Shopify, with its SKUs.
//
// The registry used to be filled in by hand: type a code, invent option axes,
// build every variant. None of that survives contact with per-SKU rendering -
// what matters is the SKU, and Shopify already knows every one of them.
//
// The state this replaces is the argument for it. DNP carried six hand-typed
// variants named "2 hearts, With light" with NO SKUs at all, modelling a world
// where heart count chose the template; DNPF carried one SKU that stopped
// matching anything the moment the shop renamed it. Neither could render.
//
// Variants are still rows - FindProductBySKU reads product_variants.sku and
// that is the only place a SKU lives - but nobody types one again.

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

type importProductRequest struct {
	// ShopifyProductID is the gid the picker listed.
	ShopifyProductID string `json:"shopify_product_id" binding:"required"`
	// Code overrides the derived one. Optional: the derived code is right
	// almost always, and asking for it every time would put a decision in
	// front of somebody who has none to make.
	Code string `json:"code"`
	// Notes is carried straight through, for whatever the next person needs.
	Notes *string `json:"notes"`
}

type importProductResponse struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Imported is how many variants now carry a SKU and can match an order.
	Imported int `json:"imported"`
	// Skipped is how many Shopify variants carried no SKU. Reported rather
	// than hidden: they are colours no order can ever be matched to, and the
	// person importing is the one who can go and fix that.
	Skipped int `json:"skipped"`
	// Retired is how many variants this import stopped matching, because
	// Shopify no longer lists them.
	Retired int `json:"retired"`
}

// importShopifyProduct creates or refreshes a registry product from Shopify.
//
// Idempotent, and meant to be run again: a product that gained a colour gets
// its new SKU by re-importing, which is how the registry stays true without
// anybody remembering to maintain it.
func (s *Server) importShopifyProduct(c *gin.Context) {
	var req importProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Choose a Shopify product to import.")
		return
	}
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}
	ctx := c.Request.Context()

	// The same resolution listShopifyProducts uses, so the picker and the
	// import cannot disagree about which store they are talking to.
	conn, err := s.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: slug, Provider: shopifyProvider,
	})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		detail(c, http.StatusConflict, "This brand's Shopify isn't connected yet.")
		return
	}
	products, err := s.shopify.ListProducts(ctx, shop, token, 0)
	if err != nil {
		detail(c, http.StatusBadGateway, "Could not read the products from Shopify.")
		return
	}

	var found *shopify.ProductSummary
	for i := range products {
		if products[i].GID == strings.TrimSpace(req.ShopifyProductID) {
			found = &products[i]
			break
		}
	}
	if found == nil {
		detail(c, http.StatusNotFound, "That product is no longer in the Shopify catalogue.")
		return
	}

	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		code = productCodeFor(*found)
	}
	if code == "" {
		detail(c, http.StatusUnprocessableEntity,
			"This product's variants carry no SKUs, so there is nothing to match an order by. "+
				"Add SKUs in Shopify first.")
		return
	}

	withSKU := make([]shopify.VariantSummary, 0, len(found.Variants))
	for _, v := range found.Variants {
		if strings.TrimSpace(v.SKU) != "" {
			withSKU = append(withSKU, v)
		}
	}
	if len(withSKU) == 0 {
		detail(c, http.StatusUnprocessableEntity,
			"None of this product's variants carry a SKU, so no order could ever be matched to it.")
		return
	}

	// Lower-cased, because the retire query compares against lower(sku) and
	// the storefront is not consistent about case.
	keep := make([]string, 0, len(withSKU))
	for _, v := range withSKU {
		keep = append(keep, strings.ToLower(strings.TrimSpace(v.SKU)))
	}

	var retired int64
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		product, err := q.UpsertProductByCode(ctx, gen.UpsertProductByCodeParams{
			ID: uuid.New(), Code: code, Name: found.Title,
			// Generated: a product imported here is one Tensor is being asked
			// to render. Not overwritten on a re-import.
			Kind: "generated", Status: "active", Notes: req.Notes,
		})
		if err != nil {
			return err
		}
		for _, v := range withSKU {
			if err := q.UpsertVariantBySKU(ctx, gen.UpsertVariantBySKUParams{
				ID: uuid.New(), ProductID: product.ID,
				Sku: strPtr(strings.TrimSpace(v.SKU)), Name: variantName(v),
			}); err != nil {
				return err
			}
		}
		retired, err = q.RetireVariantsNotInSKUs(ctx, gen.RetireVariantsNotInSKUsParams{
			ProductID: product.ID, Skus: keep,
		})
		return err
	})
	if err != nil {
		obs.FromContext(ctx).Error("could not import a Shopify product",
			"code", code, "error", err)
		detail(c, http.StatusInternalServerError, "Could not import that product.")
		return
	}

	obs.FromContext(ctx).Info("imported a product from Shopify",
		"code", code, "title", found.Title,
		"variants", len(withSKU), "retired", retired)

	c.JSON(http.StatusOK, importProductResponse{
		Code: code, Name: found.Title,
		Imported: len(withSKU),
		Skipped:  len(found.Variants) - len(withSKU),
		Retired:  int(retired),
	})
}

// productCodeFor derives a registry code from the product's SKUs.
//
// The commonest family segment wins: DNPWL-BLU, DNPWL-RED and DNPWL-GLD give
// DNPWL. That is the segment generatedSKUSegments would have been taught by
// hand, read off the data instead.
//
// Falls back to the handle, upper-cased, when the SKUs share no segment - a
// product whose SKUs are LK and PCL has no family, and a code is still needed
// to address it in a URL.
func productCodeFor(p shopify.ProductSummary) string {
	counts := map[string]int{}
	for _, v := range p.Variants {
		sku := strings.TrimSpace(v.SKU)
		if sku == "" {
			continue
		}
		// The FIRST segment only. A SKU is <family>-<variant>, and counting
		// every segment would let a colour shared by three variants outvote
		// the family they all belong to.
		if seg := strings.SplitN(strings.ToUpper(sku), "-", 2)[0]; seg != "" {
			counts[seg]++
		}
	}
	best, bestN := "", 0
	// Sorted, so a tie resolves the same way twice rather than by whichever
	// key the map happened to yield first.
	segs := make([]string, 0, len(counts))
	for seg := range counts {
		segs = append(segs, seg)
	}
	sort.Strings(segs)
	for _, seg := range segs {
		if counts[seg] > bestN {
			best, bestN = seg, counts[seg]
		}
	}
	if best != "" {
		return best
	}
	code := strings.ToUpper(strings.TrimSpace(p.Handle))
	if len(code) > 32 {
		code = code[:32]
	}
	return code
}

// variantName is what the registry calls a variant.
//
// Shopify's own title, which is the colour and light option a person reading
// the SKU list needs to see. "Default Title" is Shopify's word for a product
// with no options at all and means nothing here, so the SKU stands in.
func variantName(v shopify.VariantSummary) string {
	title := strings.TrimSpace(v.Title)
	if title == "" || strings.EqualFold(title, "Default Title") {
		return strings.TrimSpace(v.SKU)
	}
	return title
}
