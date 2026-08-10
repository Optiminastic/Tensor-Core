package shopify

import "context"

// CatalogProduct is one product in a store's live catalog, flattened from the
// Admin API for the read-only catalog view. Prices/currency are Shopify's decimal
// strings; UpdatedAt is RFC3339.
type CatalogProduct struct {
	GID            string
	Title          string
	Handle         string
	Status         string
	Vendor         string
	ProductType    string
	TotalInventory int
	ImageURL       string
	ImageAlt       string
	MinPrice       string
	MaxPrice       string
	CurrencyCode   string
	UpdatedAt      string
	AdminURL       string
}

const listProductsQuery = `query ListProducts($first: Int!) {
  products(first: $first, sortKey: UPDATED_AT, reverse: true) {
    nodes {
      id title handle status vendor productType totalInventory updatedAt
      featuredImage { url altText }
      priceRangeV2 {
        minVariantPrice { amount currencyCode }
        maxVariantPrice { amount }
      }
    }
  }
}`

// ListProducts fetches the first `first` products of a store's catalog, newest
// first. shop is "<handle>.myshopify.com".
func (c *Client) ListProducts(ctx context.Context, shop, token string, first int) ([]CatalogProduct, error) {
	var out struct {
		Data struct {
			Products struct {
				Nodes []struct {
					ID             string `json:"id"`
					Title          string `json:"title"`
					Handle         string `json:"handle"`
					Status         string `json:"status"`
					Vendor         string `json:"vendor"`
					ProductType    string `json:"productType"`
					TotalInventory int    `json:"totalInventory"`
					UpdatedAt      string `json:"updatedAt"`
					FeaturedImage  *struct {
						URL     string `json:"url"`
						AltText string `json:"altText"`
					} `json:"featuredImage"`
					PriceRangeV2 struct {
						MinVariantPrice struct {
							Amount       string `json:"amount"`
							CurrencyCode string `json:"currencyCode"`
						} `json:"minVariantPrice"`
						MaxVariantPrice struct {
							Amount string `json:"amount"`
						} `json:"maxVariantPrice"`
					} `json:"priceRangeV2"`
				} `json:"nodes"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, listProductsQuery, map[string]any{"first": first}, &out); err != nil {
		return nil, err
	}

	products := make([]CatalogProduct, 0, len(out.Data.Products.Nodes))
	for _, n := range out.Data.Products.Nodes {
		p := CatalogProduct{
			GID: n.ID, Title: n.Title, Handle: n.Handle, Status: n.Status,
			Vendor: n.Vendor, ProductType: n.ProductType, TotalInventory: n.TotalInventory,
			UpdatedAt:    n.UpdatedAt,
			MinPrice:     n.PriceRangeV2.MinVariantPrice.Amount,
			MaxPrice:     n.PriceRangeV2.MaxVariantPrice.Amount,
			CurrencyCode: n.PriceRangeV2.MinVariantPrice.CurrencyCode,
			AdminURL:     adminURL(shop, n.ID),
		}
		if n.FeaturedImage != nil {
			p.ImageURL = n.FeaturedImage.URL
			p.ImageAlt = n.FeaturedImage.AltText
		}
		products = append(products, p)
	}
	return products, nil
}
