package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxCatalogProducts bounds the live catalog page. The view is a browse, not an
// export, so the newest 50 is plenty and keeps the Shopify call cheap.
const maxCatalogProducts = 50

// shopifyCatalogProduct is one product in the read-only catalog view. Its JSON
// shape is the contract the frontend validates (ShopifyProductSchema).
type shopifyCatalogProduct struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Handle         string `json:"handle"`
	Status         string `json:"status"`
	Vendor         string `json:"vendor"`
	ProductType    string `json:"product_type"`
	TotalInventory int    `json:"total_inventory"`
	ImageURL       string `json:"image_url"`
	ImageAlt       string `json:"image_alt"`
	MinPrice       string `json:"min_price"`
	MaxPrice       string `json:"max_price"`
	CurrencyCode   string `json:"currency_code"`
	UpdatedAt      string `json:"updated_at"`
	AdminURL       string `json:"admin_url"`
}

// registerShopifyCatalog wires the read-only "live catalog" endpoint: a brand's
// connected Shopify store's products, fetched live (Tensor does not mirror them).
func (s *Server) registerShopifyCatalog(r *gin.Engine) {
	r.GET("/brands/:slug/shopify-products",
		s.guards.RequireUser(),
		s.guards.RequirePermission(auth.BrandRead.Key()),
		s.listShopifyProducts)
}

// listShopifyProducts returns the brand's live Shopify catalog. 409 when the
// brand's Shopify isn't connected; 502 when Shopify itself rejects the call.
// Guarded by brand:read + brand access.
func (s *Server) listShopifyProducts(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.requireBrandAccess(c, slug) {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}
	ctx := c.Request.Context()

	shop, token, ok := s.shopifyCredsForBrand(ctx, c, slug)
	if !ok {
		return
	}
	products, err := s.shopify.ListProducts(ctx, shop, token, maxCatalogProducts)
	if err != nil {
		obs.FromContext(ctx).Error("shopify list products failed", "brand", slug, "error", err)
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	out := make([]shopifyCatalogProduct, 0, len(products))
	for _, p := range products {
		out = append(out, catalogProductDTO(p))
	}
	c.JSON(http.StatusOK, out)
}

// catalogProductDTO maps a client product to the wire shape, lowercasing the
// status to the merchant-facing enum used elsewhere (active / draft / archived).
func catalogProductDTO(p shopify.CatalogProduct) shopifyCatalogProduct {
	return shopifyCatalogProduct{
		ID:             p.GID,
		Title:          p.Title,
		Handle:         p.Handle,
		Status:         strings.ToLower(p.Status),
		Vendor:         p.Vendor,
		ProductType:    p.ProductType,
		TotalInventory: p.TotalInventory,
		ImageURL:       p.ImageURL,
		ImageAlt:       p.ImageAlt,
		MinPrice:       p.MinPrice,
		MaxPrice:       p.MaxPrice,
		CurrencyCode:   p.CurrencyCode,
		UpdatedAt:      p.UpdatedAt,
		AdminURL:       p.AdminURL,
	}
}
