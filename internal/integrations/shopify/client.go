// Package shopify is a minimal Admin GraphQL client for the one write the product
// needs: creating a draft product from an approved design. It mirrors the request
// shape proven in the frontend onboarding client (POST /admin/api/<v>/graphql.json
// with an X-Shopify-Access-Token header). The token is an argument, never logged.
package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

const defaultRequestTimeout = 15 * time.Second

// ErrRateLimited is wrapped by the APIError returned when Shopify answers 429.
// Callers can errors.Is against it; the retry loop uses it to back off.
var ErrRateLimited = errors.New("shopify rate limited")

// retryPolicy governs the automatic retry on rate limiting. Only 429 is retried
// (the request was rejected before execution, so retrying cannot duplicate a
// mutation); transport and 5xx errors are surfaced immediately.
var retryPolicy = retry.Policy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second}

// Metafield is a custom metafield to attach to the product (namespace "tensor").
type Metafield struct {
	Namespace string
	Key       string
	Type      string
	Value     string
}

// ProductDraft is the set of details Tensor sends; the merchant finishes any
// remaining fields (variants, collections) in Shopify.
type ProductDraft struct {
	Title           string
	Vendor          string
	ProductType     string
	Tags            []string
	PriceINR        int
	DescriptionHTML string
	SEOTitle        string
	SEODescription  string
	SKU             string
	WeightGrams     float64
	Metafields      []Metafield
	Images          []ProductImage
}

// ProductRef points back at the created draft so the UI can link to it. VariantGID
// is the default variant, persisted so a publish retry can re-set its price on the
// existing product instead of creating a duplicate.
type ProductRef struct {
	GID        string
	VariantGID string
	Handle     string
	AdminURL   string
}

// Client talks to one store's Admin GraphQL API.
type Client struct {
	http       *http.Client
	apiVersion string
	// baseURL overrides the scheme+host (tests point it at an httptest server).
	// Empty means the real https://<shop> endpoint.
	baseURL string
}

// New builds a client for the given Admin API version (e.g. "2024-10") with the
// given per-request timeout (zero uses the default). It carries a keep-alive
// transport so publishes reuse connections; construct it once and share it.
func New(apiVersion string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	return &Client{
		http:       &http.Client{Timeout: timeout, Transport: transport},
		apiVersion: apiVersion,
	}
}

// APIError is a Shopify-side failure (transport, HTTP status, or userErrors),
// safe to surface to the caller without leaking the token. It can carry a
// wrapped cause (for errors.Is/As) and rate-limit retry hints.
type APIError struct {
	msg        string
	err        error // wrapped cause, never rendered to the client
	retryable  bool
	retryAfter time.Duration
}

func (e *APIError) Error() string             { return e.msg }
func (e *APIError) Unwrap() error             { return e.err }
func (e *APIError) Retryable() bool           { return e.retryable }
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

func apiErr(format string, args ...any) *APIError {
	return &APIError{msg: fmt.Sprintf(format, args...)}
}

// CreateProduct creates a DRAFT product with the core details + metafields and
// returns its ref (including the default variant GID) without setting a price.
// Splitting create from pricing lets a caller persist the product first, so a
// later failure and retry reuse the same draft instead of duplicating it.
// shop is "<handle>.myshopify.com".
func (c *Client) CreateProduct(ctx context.Context, shop, token string, draft ProductDraft) (ProductRef, error) {
	created, err := c.productCreate(ctx, shop, token, draft)
	if err != nil {
		return ProductRef{}, err
	}
	return ProductRef{
		GID:        created.productGID,
		VariantGID: created.variantGID,
		Handle:     created.handle,
		AdminURL:   adminURL(shop, created.productGID),
	}, nil
}

// VariantDetails are the default variant's editable fields. Zero values are
// omitted, so setting only a price leaves SKU and weight untouched.
type VariantDetails struct {
	PriceINR    int
	SKU         string
	WeightGrams float64
}

// SetPrice sets the default variant's price. It is idempotent: setting the same
// price again is a harmless no-op, so a publish retry can safely call it.
func (c *Client) SetPrice(ctx context.Context, shop, token, productGID, variantGID string, priceINR int) error {
	return c.setVariant(ctx, shop, token, productGID, variantGID, VariantDetails{PriceINR: priceINR})
}

// SetVariant sets the default variant's price, SKU and weight in one update. It
// is idempotent, so a publish retry can safely call it.
func (c *Client) SetVariant(
	ctx context.Context, shop, token, productGID, variantGID string, d VariantDetails,
) error {
	return c.setVariant(ctx, shop, token, productGID, variantGID, d)
}

// CreateDraftProduct creates a DRAFT product and sets its price in one call. It is
// the create-then-price convenience used where idempotent reuse is not needed.
func (c *Client) CreateDraftProduct(ctx context.Context, shop, token string, draft ProductDraft) (ProductRef, error) {
	ref, err := c.CreateProduct(ctx, shop, token, draft)
	if err != nil {
		return ProductRef{}, err
	}
	if draft.PriceINR > 0 && ref.VariantGID != "" {
		if err := c.SetPrice(ctx, shop, token, ref.GID, ref.VariantGID, draft.PriceINR); err != nil {
			return ProductRef{}, err
		}
	}
	return ref, nil
}

const productCreateMutation = `mutation CreateDraftProduct($product: ProductCreateInput!, $media: [CreateMediaInput!]) {
  productCreate(product: $product, media: $media) {
    product { id handle variants(first: 1) { nodes { id } } }
    userErrors { field message }
  }
}`

type createdProduct struct {
	productGID string
	handle     string
	variantGID string
}

func (c *Client) productCreate(ctx context.Context, shop, token string, draft ProductDraft) (createdProduct, error) {
	metafields := make([]map[string]string, 0, len(draft.Metafields))
	for _, m := range draft.Metafields {
		metafields = append(metafields, map[string]string{
			"namespace": m.Namespace, "key": m.Key, "type": m.Type, "value": m.Value,
		})
	}
	product := map[string]any{
		"title":       draft.Title,
		"vendor":      draft.Vendor,
		"productType": draft.ProductType,
		"tags":        draft.Tags,
		"status":      "DRAFT",
		"metafields":  metafields,
	}
	if draft.DescriptionHTML != "" {
		product["descriptionHtml"] = draft.DescriptionHTML
	}
	if seo := seoInput(draft); seo != nil {
		product["seo"] = seo
	}

	// Stage and upload the images first; their resource URLs become product media.
	media, err := c.buildMedia(ctx, shop, token, draft.Images)
	if err != nil {
		return createdProduct{}, err
	}

	variables := map[string]any{"product": product}
	if len(media) > 0 {
		variables["media"] = media
	}

	var out struct {
		Data struct {
			ProductCreate struct {
				Product struct {
					ID       string `json:"id"`
					Handle   string `json:"handle"`
					Variants struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"variants"`
				} `json:"product"`
				UserErrors []userError `json:"userErrors"`
			} `json:"productCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, productCreateMutation, variables, &out); err != nil {
		return createdProduct{}, err
	}
	if msg := firstUserError(out.Data.ProductCreate.UserErrors); msg != "" {
		return createdProduct{}, apiErr("Shopify rejected the product: %s", msg)
	}
	p := out.Data.ProductCreate.Product
	if p.ID == "" {
		return createdProduct{}, apiErr("Shopify did not return a created product")
	}
	created := createdProduct{productGID: p.ID, handle: p.Handle}
	if len(p.Variants.Nodes) > 0 {
		created.variantGID = p.Variants.Nodes[0].ID
	}
	return created, nil
}

// seoInput builds the product SEO object, or nil when neither field is set.
func seoInput(draft ProductDraft) map[string]any {
	seo := map[string]any{}
	if draft.SEOTitle != "" {
		seo["title"] = draft.SEOTitle
	}
	if draft.SEODescription != "" {
		seo["description"] = draft.SEODescription
	}
	if len(seo) == 0 {
		return nil
	}
	return seo
}

// buildMedia stages the draft's images and maps them to product media inputs.
func (c *Client) buildMedia(ctx context.Context, shop, token string, images []ProductImage) ([]map[string]any, error) {
	resources, err := c.uploadImages(ctx, shop, token, images)
	if err != nil {
		return nil, err
	}
	media := make([]map[string]any, 0, len(resources))
	for _, url := range resources {
		media = append(media, map[string]any{
			"originalSource": url, "mediaContentType": "IMAGE",
		})
	}
	return media, nil
}

const variantPriceMutation = `mutation SetVariantPrice($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id }
    userErrors { field message }
  }
}`

func (c *Client) setVariant(
	ctx context.Context, shop, token, productGID, variantGID string, d VariantDetails,
) error {
	variant := map[string]any{"id": variantGID}
	if d.PriceINR > 0 {
		variant["price"] = fmt.Sprintf("%d", d.PriceINR)
	}
	inventoryItem := map[string]any{}
	if d.SKU != "" {
		inventoryItem["sku"] = d.SKU
	}
	if d.WeightGrams > 0 {
		inventoryItem["measurement"] = map[string]any{
			"weight": map[string]any{"value": d.WeightGrams, "unit": "GRAMS"},
		}
	}
	if len(inventoryItem) > 0 {
		variant["inventoryItem"] = inventoryItem
	}

	variables := map[string]any{
		"productId": productGID,
		"variants":  []map[string]any{variant},
	}
	var out struct {
		Data struct {
			ProductVariantsBulkUpdate struct {
				UserErrors []userError `json:"userErrors"`
			} `json:"productVariantsBulkUpdate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, variantPriceMutation, variables, &out); err != nil {
		return err
	}
	if msg := firstUserError(out.Data.ProductVariantsBulkUpdate.UserErrors); msg != "" {
		return apiErr("Shopify rejected the variant details: %s", msg)
	}
	return nil
}

// ProductSummary is one product in a store's catalog, as shown in a listing.
type ProductSummary struct {
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
      id
      title
      handle
      status
      vendor
      productType
      totalInventory
      featuredImage { url altText }
      priceRangeV2 {
        minVariantPrice { amount currencyCode }
        maxVariantPrice { amount currencyCode }
      }
      updatedAt
    }
  }
}`

// maxProductsPerPage is Shopify's own cap on a single products(first:) page.
const maxProductsPerPage = 250

// ListProducts fetches up to limit products from the store's catalog, most
// recently updated first. It is a single page (no cursor pagination yet) -
// fine for a brand's own store, which is not expected to run past a few
// hundred products.
func (c *Client) ListProducts(ctx context.Context, shop, token string, limit int) ([]ProductSummary, error) {
	if limit <= 0 || limit > maxProductsPerPage {
		limit = maxProductsPerPage
	}
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
					FeaturedImage  *struct {
						URL     string  `json:"url"`
						AltText *string `json:"altText"`
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
					UpdatedAt string `json:"updatedAt"`
				} `json:"nodes"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, listProductsQuery, map[string]any{"first": limit}, &out); err != nil {
		return nil, err
	}

	products := make([]ProductSummary, 0, len(out.Data.Products.Nodes))
	for _, n := range out.Data.Products.Nodes {
		p := ProductSummary{
			GID: n.ID, Title: n.Title, Handle: n.Handle, Status: n.Status,
			Vendor: n.Vendor, ProductType: n.ProductType, TotalInventory: n.TotalInventory,
			MinPrice:     n.PriceRangeV2.MinVariantPrice.Amount,
			MaxPrice:     n.PriceRangeV2.MaxVariantPrice.Amount,
			CurrencyCode: n.PriceRangeV2.MinVariantPrice.CurrencyCode,
			UpdatedAt:    n.UpdatedAt,
			AdminURL:     adminURL(shop, n.ID),
		}
		if n.FeaturedImage != nil {
			p.ImageURL = n.FeaturedImage.URL
			if n.FeaturedImage.AltText != nil {
				p.ImageAlt = *n.FeaturedImage.AltText
			}
		}
		products = append(products, p)
	}
	return products, nil
}

type userError struct {
	Field   []string `json:"field"`
	Message string   `json:"message"`
}

func firstUserError(errs []userError) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0].Message
}

// do runs one GraphQL request and decodes data into out, retrying only on a 429
// rate-limit response (honouring Retry-After). Transport failures and 5xx are
// surfaced immediately rather than retried, so a create mutation that may have
// executed is never sent twice.
func (c *Client) do(ctx context.Context, shop, token, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	base := c.baseURL
	if base == "" {
		base = "https://" + shop
	}
	endpoint := fmt.Sprintf("%s/admin/api/%s/graphql.json", base, c.apiVersion)

	return retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		return c.doOnce(ctx, endpoint, token, body, out)
	})
}

// doOnce performs a single GraphQL request. A 429 becomes a retryable APIError
// carrying the server's Retry-After; every other failure is terminal.
func (c *Client) doOnce(ctx context.Context, endpoint, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := c.http.Do(req)
	if err != nil {
		return &APIError{msg: fmt.Sprintf("could not reach Shopify: %v", err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		return &APIError{
			msg:        "Shopify is rate limiting requests; please retry shortly",
			err:        ErrRateLimited,
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return apiErr("Shopify rejected the access token (check the token and the app's write_products scope)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErr("Shopify request failed (%d)", resp.StatusCode)
	}

	// Decode into a shape that carries both top-level errors and the data.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, err := readAll(resp)
	if err != nil {
		return apiErr("could not read Shopify response: %v", err)
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Errors) > 0 {
		return apiErr("Shopify GraphQL error: %s", envelope.Errors[0].Message)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return apiErr("could not decode Shopify response: %v", err)
	}
	return nil
}

// parseRetryAfter reads Shopify's Retry-After header, expressed in (possibly
// fractional) seconds. An absent or unparseable value yields zero, letting the
// retry loop fall back to its own backoff.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func readAll(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// adminURL builds the store admin link to the product from the shop domain and
// the product gid (gid://shopify/Product/<numeric>).
func adminURL(shop, productGID string) string {
	handle := strings.TrimSuffix(shop, ".myshopify.com")
	numeric := productGID
	if i := strings.LastIndex(productGID, "/"); i >= 0 {
		numeric = productGID[i+1:]
	}
	return fmt.Sprintf("https://admin.shopify.com/store/%s/products/%s", handle, numeric)
}
