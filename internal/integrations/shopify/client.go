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
	// recorder, when set, receives one Call per physical Admin API request for
	// the activity log. Nil means no logging.
	recorder Recorder
}

// Call describes one physical Admin API request, handed to the Recorder.
type Call struct {
	Shop       string
	Method     string
	Operation  string
	Scope      string
	StatusCode int
	OK         bool
	Err        error
	Latency    time.Duration
}

// Recorder receives one Call per physical Admin API request. Implementations
// must be non-blocking and must never fail the caller: logging is best-effort.
type Recorder interface {
	RecordCall(ctx context.Context, call Call)
}

// SetRecorder attaches an activity-log recorder. Call once at startup.
func (c *Client) SetRecorder(r Recorder) { c.recorder = r }

func (c *Client) record(ctx context.Context, call Call) {
	if c.recorder != nil {
		c.recorder.RecordCall(ctx, call)
	}
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
// omitted, so setting only a price leaves SKU and weight untouched. Tracked is a
// pointer so "leave tracking as-is" (nil) is distinct from "turn it off" (false);
// it is set true before an inventory-quantity edit so the quantity can apply.
type VariantDetails struct {
	PriceINR    int
	SKU         string
	WeightGrams float64
	Tracked     *bool
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
	if d.Tracked != nil {
		inventoryItem["tracked"] = *d.Tracked
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

// ProductState is an already-published product read back from Shopify, used to
// prefill an edit form so the merchant edits from the current live values rather
// than from blank. Price is the default variant's price in whole INR.
type ProductState struct {
	GID              string
	Handle           string
	AdminURL         string
	Title            string
	DescriptionHTML  string
	ProductType      string
	Vendor           string
	Tags             []string
	Status           string // ACTIVE / DRAFT / ARCHIVED (Shopify's ProductStatus)
	SEOTitle         string
	SEODescription   string
	VariantGID       string
	PriceINR         int
	SKU              string
	Images           []MediaImage
	InventoryItemGID string
	InventoryQty     int
}

// MediaImage is one image already on the product, read so the edit UI can show
// the current gallery and let the merchant remove images by their media id.
type MediaImage struct {
	GID string
	URL string
}

// ProductEdit is the set of merchant-facing fields pushed to an existing product
// via productUpdate. It never touches the variant price (that is a separate,
// idempotent SetVariant call) nor the system-derived "tensor" metafields.
type ProductEdit struct {
	GID             string
	Title           string
	DescriptionHTML string
	ProductType     string
	Vendor          string
	Tags            []string
	Status          string // ACTIVE / DRAFT / ARCHIVED, or "" to leave unchanged
	SEOTitle        string
	SEODescription  string
}

const getProductQuery = `query GetProduct($id: ID!) {
  product(id: $id) {
    id handle title descriptionHtml productType vendor tags status
    seo { title description }
    media(first: 20) {
      nodes { id mediaContentType ... on MediaImage { image { url } } }
    }
    variants(first: 1) {
      nodes { id price sku inventoryQuantity inventoryItem { id } }
    }
  }
}`

// GetProduct reads the current state of a published product so the caller can
// prefill an edit. shop is "<handle>.myshopify.com".
func (c *Client) GetProduct(ctx context.Context, shop, token, productGID string) (ProductState, error) {
	variables := map[string]any{"id": productGID}
	var out struct {
		Data struct {
			Product *struct {
				ID              string   `json:"id"`
				Handle          string   `json:"handle"`
				Title           string   `json:"title"`
				DescriptionHTML string   `json:"descriptionHtml"`
				ProductType     string   `json:"productType"`
				Vendor          string   `json:"vendor"`
				Tags            []string `json:"tags"`
				Status          string   `json:"status"`
				SEO             struct {
					Title       string `json:"title"`
					Description string `json:"description"`
				} `json:"seo"`
				Media struct {
					Nodes []struct {
						ID    string `json:"id"`
						Image struct {
							URL string `json:"url"`
						} `json:"image"`
					} `json:"nodes"`
				} `json:"media"`
				Variants struct {
					Nodes []struct {
						ID                string `json:"id"`
						Price             string `json:"price"`
						SKU               string `json:"sku"`
						InventoryQuantity int    `json:"inventoryQuantity"`
						InventoryItem     struct {
							ID string `json:"id"`
						} `json:"inventoryItem"`
					} `json:"nodes"`
				} `json:"variants"`
			} `json:"product"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, getProductQuery, variables, &out); err != nil {
		return ProductState{}, err
	}
	p := out.Data.Product
	if p == nil || p.ID == "" {
		return ProductState{}, apiErr("that product no longer exists in Shopify")
	}
	state := ProductState{
		GID: p.ID, Handle: p.Handle, AdminURL: adminURL(shop, p.ID),
		Title: p.Title, DescriptionHTML: p.DescriptionHTML, ProductType: p.ProductType,
		Vendor: p.Vendor, Tags: p.Tags, Status: p.Status,
		SEOTitle: p.SEO.Title, SEODescription: p.SEO.Description,
	}
	for _, m := range p.Media.Nodes {
		if m.Image.URL != "" {
			state.Images = append(state.Images, MediaImage{GID: m.ID, URL: m.Image.URL})
		}
	}
	if len(p.Variants.Nodes) > 0 {
		v := p.Variants.Nodes[0]
		state.VariantGID = v.ID
		state.SKU = v.SKU
		state.PriceINR = parsePriceINR(v.Price)
		state.InventoryItemGID = v.InventoryItem.ID
		state.InventoryQty = v.InventoryQuantity
	}
	return state, nil
}

const productUpdateMutation = `mutation UpdateProduct($product: ProductUpdateInput!) {
  productUpdate(product: $product) {
    product { id handle }
    userErrors { field message }
  }
}`

// UpdateProduct pushes edited merchant-facing fields to an existing product and
// returns its ref. Empty string fields are still sent (a merchant may clear a
// description or vendor); Status "" is omitted so it is left unchanged.
func (c *Client) UpdateProduct(ctx context.Context, shop, token string, edit ProductEdit) (ProductRef, error) {
	product := map[string]any{
		"id":              edit.GID,
		"title":           edit.Title,
		"vendor":          edit.Vendor,
		"productType":     edit.ProductType,
		"tags":            edit.Tags,
		"descriptionHtml": edit.DescriptionHTML,
		"seo":             map[string]any{"title": edit.SEOTitle, "description": edit.SEODescription},
	}
	if edit.Status != "" {
		product["status"] = edit.Status
	}
	variables := map[string]any{"product": product}

	var out struct {
		Data struct {
			ProductUpdate struct {
				Product struct {
					ID     string `json:"id"`
					Handle string `json:"handle"`
				} `json:"product"`
				UserErrors []userError `json:"userErrors"`
			} `json:"productUpdate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, productUpdateMutation, variables, &out); err != nil {
		return ProductRef{}, err
	}
	if msg := firstUserError(out.Data.ProductUpdate.UserErrors); msg != "" {
		return ProductRef{}, apiErr("Shopify rejected the product update: %s", msg)
	}
	p := out.Data.ProductUpdate.Product
	if p.ID == "" {
		return ProductRef{}, apiErr("Shopify did not return the updated product")
	}
	return ProductRef{GID: p.ID, Handle: p.Handle, AdminURL: adminURL(shop, p.ID)}, nil
}

// parsePriceINR converts Shopify's decimal price string ("1234.00") to whole INR.
// An unparseable value yields 0, so a read never fails over a price.
func parsePriceINR(v string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int(f + 0.5)
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

	op := operationName(query)
	scope := scopeForOperation(op)
	return retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		return c.doOnce(ctx, endpoint, token, shop, op, scope, body, out)
	})
}

// doOnce performs a single GraphQL request. A 429 becomes a retryable APIError
// carrying the server's Retry-After; every other failure is terminal.
func (c *Client) doOnce(ctx context.Context, endpoint, token, shop, op, scope string, body []byte, out any) (err error) {
	start := time.Now()
	status := 0
	defer func() {
		c.record(ctx, Call{
			Shop: shop, Method: http.MethodPost, Operation: op, Scope: scope,
			StatusCode: status, OK: err == nil, Err: err, Latency: time.Since(start),
		})
	}()

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if reqErr != nil {
		err = reqErr
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, doErr := c.http.Do(req)
	if doErr != nil {
		err = &APIError{msg: fmt.Sprintf("could not reach Shopify: %v", doErr), err: doErr}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode

	if resp.StatusCode == http.StatusTooManyRequests {
		err = &APIError{
			msg:        "Shopify is rate limiting requests; please retry shortly",
			err:        ErrRateLimited,
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		err = apiErr("Shopify rejected the access token (check the token and the app's write_products scope)")
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = apiErr("Shopify request failed (%d)", resp.StatusCode)
		return err
	}

	// Decode into a shape that carries both top-level errors and the data.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, readErr := readAll(resp)
	if readErr != nil {
		err = apiErr("could not read Shopify response: %v", readErr)
		return err
	}
	if e := json.Unmarshal(raw, &envelope); e == nil && len(envelope.Errors) > 0 {
		err = apiErr("Shopify GraphQL error: %s", envelope.Errors[0].Message)
		return err
	}
	if e := json.Unmarshal(raw, out); e != nil {
		err = apiErr("could not decode Shopify response: %v", e)
		return err
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
