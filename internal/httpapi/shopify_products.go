package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

type shopifyProductResponse struct {
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

func shopifyProductDTO(p shopify.ProductSummary) shopifyProductResponse {
	return shopifyProductResponse{
		ID: p.GID, Title: p.Title, Handle: p.Handle, Status: p.Status,
		Vendor: p.Vendor, ProductType: p.ProductType, TotalInventory: p.TotalInventory,
		ImageURL: p.ImageURL, ImageAlt: p.ImageAlt,
		MinPrice: p.MinPrice, MaxPrice: p.MaxPrice, CurrencyCode: p.CurrencyCode,
		UpdatedAt: p.UpdatedAt, AdminURL: p.AdminURL,
	}
}

func (s *Server) registerShopifyProducts(r *gin.Engine) {
	g := r.Group("/brands/:slug/shopify-products")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.BrandRead.Key()), s.listShopifyProducts)
}

// listShopifyProducts fetches the brand's connected store's product catalog
// live from the Shopify Admin API. Tensor does not mirror products locally,
// so this always reflects Shopify's current state.
func (s *Server) listShopifyProducts(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	if !s.brandMustExist(c, slug) {
		return
	}

	ctx := c.Request.Context()
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
		detail(c, http.StatusBadGateway, "Could not fetch products from Shopify.")
		return
	}

	out := make([]shopifyProductResponse, 0, len(products))
	for _, p := range products {
		out = append(out, shopifyProductDTO(p))
	}
	c.JSON(http.StatusOK, out)
}
