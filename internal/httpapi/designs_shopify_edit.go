package httpapi

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxInventoryQty bounds a stock quantity so it can never overflow the int it
// flows into on the way to Shopify.
const maxInventoryQty = 1_000_000

// shopifyListingResponse is the current live state of an already-published
// product, read from Shopify to prefill the edit form. status/tags are the
// merchant-facing values; price is whole INR; description is plain text (the
// stored HTML rendered back down, so the same textarea round-trips it).
type shopifyListingResponse struct {
	ProductGID        string         `json:"product_gid"`
	Handle            string         `json:"handle"`
	AdminURL          string         `json:"admin_url"`
	Status            string         `json:"status"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	ProductType       string         `json:"product_type"`
	Vendor            string         `json:"vendor"`
	Tags              []string       `json:"tags"`
	SEOTitle          string         `json:"seo_title"`
	SEODescription    string         `json:"seo_description"`
	Price             int            `json:"price"`
	SKU               string         `json:"sku"`
	InventoryQuantity int            `json:"inventory_quantity"`
	Images            []listingImage `json:"images"`
}

// listingImage is one image currently on the product: its media id (so the UI can
// ask to remove it) and its URL (so the UI can show it).
type listingImage struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// shopifyEditForm carries a parsed edit. It arrives as multipart/form-data so new
// image files can ride along. Every field is optional except title; a nil price
// leaves the variant price untouched, an empty status leaves the product's status
// unchanged, and a nil inventory quantity leaves stock untouched.
type shopifyEditForm struct {
	Title          string
	Description    string
	ProductType    string
	Vendor         string
	Tags           []string
	Status         string
	SEOTitle       string
	SEODescription string
	Price          *int
	InventoryQty   *int
}

// getShopifyProductState reads the design's live Shopify product so the edit
// form starts from the current values rather than blank. 409 when the design
// isn't published yet. Guarded by shopify:publish.
func (s *Server) getShopifyProductState(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	rec, ok := s.loadListing(c, id)
	if !ok {
		return
	}
	shop, token, ok := s.shopifyCredsForBrand(ctx, c, rec.BrandSlug)
	if !ok {
		return
	}
	state, err := s.shopify.GetProduct(ctx, shop, token, rec.ProductGid)
	if err != nil {
		obs.FromContext(ctx).Error("shopify get product failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, listingResponse(rec, state))
}

// editShopifyProduct pushes edited merchant-facing fields (title, description,
// tags, type, vendor, SEO, status), and - when supplied - a new price, images
// (added / removed), and stock quantity to an already-published product. The
// design's lifecycle status is untouched: this edits the listing, it does not
// re-publish. Guarded by shopify:publish.
func (s *Server) editShopifyProduct(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	rec, ok := s.loadListing(c, id)
	if !ok {
		return
	}
	form, images, removeMedia, ok := parseShopifyEditForm(c)
	if !ok {
		return
	}
	shop, token, ok := s.shopifyCredsForBrand(ctx, c, rec.BrandSlug)
	if !ok {
		return
	}

	shopifyStatus, storedStatus := resolveEditStatus(form.Status, rec.Status)
	if err := s.pushListingEdit(ctx, listingEdit{
		shop: shop, token: token, shopifyStatus: shopifyStatus, rec: rec, form: form,
		images: images, removeMedia: removeMedia,
	}); err != nil {
		obs.FromContext(ctx).Error("shopify edit product failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	if err := s.store.Q.UpsertShopifyProduct(ctx, gen.UpsertShopifyProductParams{
		DesignID: id, BrandSlug: rec.BrandSlug, ProductGid: rec.ProductGid, VariantGid: rec.VariantGid,
		Handle: rec.Handle, AdminUrl: rec.AdminUrl, Status: storedStatus, PublishedBy: rec.PublishedBy,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Updated Shopify but could not save the reference.")
		return
	}
	c.JSON(http.StatusOK, publishResponse{
		Status: storedStatus, AdminURL: rec.AdminUrl, Handle: rec.Handle, ProductGID: rec.ProductGid,
	})
}

// listingEdit bundles everything one push needs (kept to a struct so the method
// stays within the argument limit).
type listingEdit struct {
	shop          string
	token         string
	shopifyStatus string
	rec           gen.ShopifyProduct
	form          shopifyEditForm
	images        []shopify.ProductImage
	removeMedia   []string
}

// pushListingEdit applies the whole edit in order: the merchant-facing fields,
// then the variant (price / inventory tracking), then media (remove then add),
// then the inventory quantity. Every step is idempotent, so a retry is safe.
func (s *Server) pushListingEdit(ctx context.Context, e listingEdit) error {
	if _, err := s.shopify.UpdateProduct(ctx, e.shop, e.token, shopify.ProductEdit{
		GID:             e.rec.ProductGid,
		Title:           e.form.Title,
		DescriptionHTML: descriptionToHTML(e.form.Description),
		ProductType:     e.form.ProductType,
		Vendor:          e.form.Vendor,
		Tags:            e.form.Tags,
		Status:          e.shopifyStatus,
		SEOTitle:        e.form.SEOTitle,
		SEODescription:  e.form.SEODescription,
	}); err != nil {
		return err
	}
	if err := s.pushVariant(ctx, e); err != nil {
		return err
	}
	if err := s.shopify.DeleteProductMedia(ctx, e.shop, e.token, e.rec.ProductGid, e.removeMedia); err != nil {
		return err
	}
	if err := s.shopify.AddProductMedia(ctx, e.shop, e.token, e.rec.ProductGid, e.images); err != nil {
		return err
	}
	return s.pushInventory(ctx, e)
}

// pushVariant sets the price and, when an inventory quantity is being set, turns
// on inventory tracking so the quantity can apply. It is a no-op when neither
// applies or the variant is unknown.
func (s *Server) pushVariant(ctx context.Context, e listingEdit) error {
	details := shopify.VariantDetails{}
	changed := false
	if e.form.Price != nil && *e.form.Price > 0 {
		details.PriceINR = *e.form.Price
		changed = true
	}
	if e.form.InventoryQty != nil {
		tracked := true
		details.Tracked = &tracked
		changed = true
	}
	if !changed || e.rec.VariantGid == "" {
		return nil
	}
	return s.shopify.SetVariant(ctx, e.shop, e.token, e.rec.ProductGid, e.rec.VariantGid, details)
}

// pushInventory sets the absolute on-hand quantity at the store's primary
// location. It reads the product to resolve the variant's inventory item id, so
// it runs only when a quantity was supplied. A no-op otherwise.
func (s *Server) pushInventory(ctx context.Context, e listingEdit) error {
	if e.form.InventoryQty == nil || e.rec.VariantGid == "" {
		return nil
	}
	state, err := s.shopify.GetProduct(ctx, e.shop, e.token, e.rec.ProductGid)
	if err != nil {
		return err
	}
	if state.InventoryItemGID == "" {
		return nil
	}
	location, err := s.shopify.PrimaryLocationGID(ctx, e.shop, e.token)
	if err != nil {
		return err
	}
	return s.shopify.SetInventoryQuantity(ctx, e.shop, e.token, state.InventoryItemGID, location, *e.form.InventoryQty)
}

// loadListing fetches the design's recorded Shopify product, writing a 409 when
// the design has not been published yet and returning ok=false.
func (s *Server) loadListing(c *gin.Context, id uuid.UUID) (gen.ShopifyProduct, bool) {
	rec, err := s.store.Q.GetShopifyProduct(c.Request.Context(), id)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusConflict, "This design isn't published to Shopify yet.")
			return gen.ShopifyProduct{}, false
		}
		detail(c, http.StatusInternalServerError, "Could not load the Shopify product.")
		return gen.ShopifyProduct{}, false
	}
	return rec, true
}

// shopifyCredsForBrand loads the brand's Shopify connection, writing a 409 when
// it is not connected and returning ok=false.
func (s *Server) shopifyCredsForBrand(
	ctx context.Context, c *gin.Context, brandSlug string,
) (shop, token string, ok bool) {
	conn, err := s.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: brandSlug, Provider: shopifyProvider,
	})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		detail(c, http.StatusConflict, "This brand's Shopify isn't connected yet. Connect it, then edit.")
		return "", "", false
	}
	return shop, token, true
}

// listingResponse maps the recorded product + live Shopify state to the wire
// shape, rendering the stored HTML description back to plain text and lowering
// the status to the merchant-facing enum.
func listingResponse(rec gen.ShopifyProduct, state shopify.ProductState) shopifyListingResponse {
	images := make([]listingImage, 0, len(state.Images))
	for _, img := range state.Images {
		images = append(images, listingImage{ID: img.GID, URL: img.URL})
	}
	return shopifyListingResponse{
		ProductGID:        rec.ProductGid,
		Handle:            state.Handle,
		AdminURL:          state.AdminURL,
		Status:            strings.ToLower(state.Status),
		Title:             state.Title,
		Description:       htmlToPlainText(state.DescriptionHTML),
		ProductType:       state.ProductType,
		Vendor:            state.Vendor,
		Tags:              state.Tags,
		SEOTitle:          state.SEOTitle,
		SEODescription:    state.SEODescription,
		Price:             state.PriceINR,
		SKU:               state.SKU,
		InventoryQuantity: state.InventoryQty,
		Images:            images,
	}
}

// parseShopifyEditForm reads the multipart edit: the product fields, the media
// ids to remove, and any new image files. It writes an error response and returns
// ok=false on invalid input.
func parseShopifyEditForm(c *gin.Context) (shopifyEditForm, []shopify.ProductImage, []string, bool) {
	if err := c.Request.ParseMultipartForm(int64(maxPublishImageMB) << 20); err != nil {
		detail(c, http.StatusBadRequest, "Could not read the product form.")
		return shopifyEditForm{}, nil, nil, false
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" || len([]rune(title)) > 255 {
		detail(c, http.StatusUnprocessableEntity, "A product title (1-255 characters) is required.")
		return shopifyEditForm{}, nil, nil, false
	}
	if len([]rune(c.PostForm("description"))) > 50_000 {
		detail(c, http.StatusUnprocessableEntity, "The description is too long.")
		return shopifyEditForm{}, nil, nil, false
	}
	form := shopifyEditForm{
		Title:          title,
		Description:    c.PostForm("description"),
		ProductType:    trimMax(c.PostForm("product_type"), 255),
		Vendor:         trimMax(c.PostForm("vendor"), 255),
		SEOTitle:       trimMax(c.PostForm("seo_title"), 255),
		SEODescription: trimMax(c.PostForm("seo_description"), maxSEODescription),
		Tags:           splitTags(c.PostForm("tags")),
		Status:         c.PostForm("status"),
	}
	if !parseEditNumbers(c, &form) {
		return shopifyEditForm{}, nil, nil, false
	}
	if st := strings.ToLower(strings.TrimSpace(form.Status)); st != "" &&
		st != "active" && st != "draft" && st != "archived" {
		detail(c, http.StatusUnprocessableEntity, "Status must be active, draft or archived.")
		return shopifyEditForm{}, nil, nil, false
	}
	images, ok := readPublishImages(c)
	if !ok {
		return shopifyEditForm{}, nil, nil, false
	}
	return form, images, c.PostFormArray("remove_media"), true
}

// parseEditNumbers parses the optional price and inventory quantity onto the
// form, writing an error response and returning false when either is invalid.
func parseEditNumbers(c *gin.Context, form *shopifyEditForm) bool {
	if raw := strings.TrimSpace(c.PostForm("price")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxPriceINR {
			detail(c, http.StatusUnprocessableEntity, "Price must be a positive whole number within range.")
			return false
		}
		form.Price = &n
	}
	if raw := strings.TrimSpace(c.PostForm("inventory_quantity")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxInventoryQty {
			detail(c, http.StatusUnprocessableEntity, "Stock must be a whole number within range.")
			return false
		}
		form.InventoryQty = &n
	}
	return true
}

// resolveEditStatus maps the form status to Shopify's ProductStatus enum and the
// value Tensor stores. An empty form status leaves Shopify unchanged and keeps
// the recorded status.
func resolveEditStatus(raw, current string) (shopifyStatus, storedStatus string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active":
		return "ACTIVE", "active"
	case "draft":
		return "DRAFT", "draft"
	case "archived":
		return "ARCHIVED", "archived"
	default:
		return "", strings.ToLower(current)
	}
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// htmlToPlainText renders the simple paragraph/break HTML that descriptionToHTML
// produces back to plain text, so an edit round-trips through the same textarea:
// paragraph breaks become blank lines and <br> becomes a single newline. It is
// only meant for that Tensor-generated shape, not arbitrary HTML.
func htmlToPlainText(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"</p><p>", "\n\n",
		"</p>", "\n\n",
		"<p>", "",
		"<br>", "\n",
		"<br/>", "\n",
		"<br />", "\n",
	)
	text := htmlTagPattern.ReplaceAllString(replacer.Replace(h), "")
	return strings.TrimSpace(html.UnescapeString(text))
}
