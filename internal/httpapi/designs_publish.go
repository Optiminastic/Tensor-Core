package httpapi

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

const (
	shopifyProvider   = "shopify"
	maxPublishImages  = 8
	maxPublishImageMB = 20
	maxSEODescription = 320
	// maxPriceINR bounds an explicit price override so it can never overflow the
	// int32 columns / Shopify variant it flows into.
	maxPriceINR = 100_000_000
)

// publishForm is the product data a Project Lead confirms before publishing. It
// arrives as multipart/form-data so image files can ride along. price defaults to
// the recommended selling price.
type publishForm struct {
	Title          string
	Price          *int
	ProductType    string
	Tags           []string
	Vendor         string
	Description    string
	SEOTitle       string
	SEODescription string
	SKU            string
	WeightGrams    float64
}

type publishResponse struct {
	Status     string `json:"status"`
	AdminURL   string `json:"admin_url"`
	Handle     string `json:"handle"`
	ProductGID string `json:"product_gid"`
}

// publishDesignToShopify approves a priced design and creates a Shopify DRAFT
// product from it in one step. Approval is persisted first, so a missing/failed
// Shopify connection leaves the design "approved" (retryable) rather than losing
// the decision. Guarded by shopify:publish (PROJECT_LEAD / ADMIN).
func (s *Server) publishDesignToShopify(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	form, images, ok := parsePublishForm(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	design, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	if design.Status != designPriced && design.Status != designApproved {
		detail(c, http.StatusConflict, "Only a priced design can be approved and published.")
		return
	}

	pricing, err := s.store.Q.GetDesignPricing(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design's pricing.")
		return
	}
	price, ok := resolvePrice(form.Price, pricing.RecommendedSp)
	if !ok {
		detail(c, http.StatusUnprocessableEntity, "A selling price is required to publish.")
		return
	}

	user, ok := auth.UserFrom(c)
	if !ok {
		detail(c, http.StatusUnauthorized, "Your session is not valid.")
		return
	}

	// Record the approval up front; it must survive a Shopify failure.
	if err := s.approve(ctx, id, price, user.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not approve the design.")
		return
	}

	conn, err := s.store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: design.BrandSlug, Provider: shopifyProvider,
	})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		detail(c, http.StatusConflict, "This brand's Shopify isn't connected yet. Connect it, then publish.")
		return
	}

	metrics, metricsErr := s.store.Q.GetLatestMetricsForDesign(ctx, id)
	draft := shopify.ProductDraft{
		Title:           form.Title,
		Vendor:          s.vendorFor(ctx, form.Vendor, design.BrandSlug),
		ProductType:     form.ProductType,
		Tags:            form.Tags,
		PriceINR:        price,
		DescriptionHTML: descriptionToHTML(form.Description),
		SEOTitle:        form.SEOTitle,
		SEODescription:  form.SEODescription,
		SKU:             form.SKU,
		WeightGrams:     form.WeightGrams,
		Metafields:      buildMetafields(design, pricing, metrics, metricsErr == nil),
		Images:          images,
	}

	// Create the draft product, or reuse the one a prior attempt already recorded,
	// so a retry after a mid-publish failure never creates a duplicate.
	ref, ok := s.ensureShopifyProduct(c, id, design.BrandSlug, shop, token, draft, user.ID)
	if !ok {
		return
	}

	// Set the price, SKU and weight on the (new or reused) variant. Idempotent, so
	// a retry is safe.
	if ref.VariantGID != "" && (price > 0 || form.SKU != "" || form.WeightGrams > 0) {
		if err := s.shopify.SetVariant(ctx, shop, token, ref.GID, ref.VariantGID, shopify.VariantDetails{
			PriceINR: price, SKU: form.SKU, WeightGrams: form.WeightGrams,
		}); err != nil {
			// Product exists and is recorded; the design stays "approved" and a
			// retry will reuse it. Shopify-side failure.
			obs.FromContext(ctx).Error("shopify set variant failed", "design", id, "error", err)
			detail(c, http.StatusBadGateway, err.Error())
			return
		}
	}

	if err := s.recordPublished(ctx, id, design.BrandSlug, ref, user.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Published to Shopify but could not save the reference.")
		return
	}

	c.JSON(http.StatusOK, publishResponse{
		Status: "published", AdminURL: ref.AdminURL, Handle: ref.Handle, ProductGID: ref.GID,
	})
}

// resolvePrice picks the selling price: the explicit override, else the
// recommended ladder price. Returns false when neither is available.
func resolvePrice(override *int, recommended *int32) (int, bool) {
	if override != nil {
		return *override, true
	}
	if recommended != nil {
		return int(*recommended), true
	}
	return 0, false
}

// parsePublishForm reads the multipart product form and its image files, writing
// an error response and returning ok=false on any invalid input.
func parsePublishForm(c *gin.Context) (publishForm, []shopify.ProductImage, bool) {
	if err := c.Request.ParseMultipartForm(int64(maxPublishImageMB) << 20); err != nil {
		detail(c, http.StatusBadRequest, "Could not read the product form.")
		return publishForm{}, nil, false
	}

	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" || len(title) > 255 {
		detail(c, http.StatusUnprocessableEntity, "A product title (1-255 characters) is required.")
		return publishForm{}, nil, false
	}

	form := publishForm{
		Title:          title,
		ProductType:    trimMax(c.PostForm("product_type"), 255),
		Vendor:         trimMax(c.PostForm("vendor"), 255),
		Description:    c.PostForm("description"),
		SEOTitle:       trimMax(c.PostForm("seo_title"), 255),
		SEODescription: trimMax(c.PostForm("seo_description"), maxSEODescription),
		SKU:            trimMax(c.PostForm("sku"), 255),
		Tags:           splitTags(c.PostForm("tags")),
	}

	if raw := strings.TrimSpace(c.PostForm("price")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxPriceINR {
			detail(c, http.StatusUnprocessableEntity, "Price must be a positive whole number within range.")
			return publishForm{}, nil, false
		}
		form.Price = &n
	}
	if raw := strings.TrimSpace(c.PostForm("weight_grams")); raw != "" {
		w, err := strconv.ParseFloat(raw, 64)
		if err != nil || w < 0 {
			detail(c, http.StatusUnprocessableEntity, "Weight must be zero or a positive number.")
			return publishForm{}, nil, false
		}
		form.WeightGrams = w
	}

	images, ok := readPublishImages(c)
	if !ok {
		return publishForm{}, nil, false
	}
	return form, images, true
}

// readPublishImages reads the "images" file parts, enforcing the count, per-file
// size, and image content-type limits.
func readPublishImages(c *gin.Context) ([]shopify.ProductImage, bool) {
	if c.Request.MultipartForm == nil {
		return nil, true
	}
	files := c.Request.MultipartForm.File["images"]
	if len(files) > maxPublishImages {
		detail(c, http.StatusUnprocessableEntity,
			fmt.Sprintf("At most %d images can be attached.", maxPublishImages))
		return nil, false
	}
	images := make([]shopify.ProductImage, 0, len(files))
	for _, fh := range files {
		if fh.Size > int64(maxPublishImageMB)<<20 {
			detail(c, http.StatusUnprocessableEntity,
				fmt.Sprintf("Each image must be under %d MB.", maxPublishImageMB))
			return nil, false
		}
		contentType := fh.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			detail(c, http.StatusUnprocessableEntity, "Only image files can be attached.")
			return nil, false
		}
		f, err := fh.Open()
		if err != nil {
			detail(c, http.StatusBadRequest, "Could not read an uploaded image.")
			return nil, false
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			detail(c, http.StatusBadRequest, "Could not read an uploaded image.")
			return nil, false
		}
		images = append(images, shopify.ProductImage{
			Filename: fh.Filename, MimeType: contentType, Data: data,
		})
	}
	return images, true
}

// descriptionToHTML turns plain text into simple HTML: blank-line-separated
// blocks become paragraphs and single newlines become breaks. The text is
// escaped first, so any HTML in the input is shown literally, never injected.
func descriptionToHTML(text string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if trimmed == "" {
		return ""
	}
	paras := make([]string, 0)
	for _, block := range strings.Split(trimmed, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		escaped := strings.ReplaceAll(html.EscapeString(block), "\n", "<br>")
		paras = append(paras, "<p>"+escaped+"</p>")
	}
	return strings.Join(paras, "")
}

func trimMax(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

func splitTags(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			tags = append(tags, p)
		}
	}
	return tags
}

// shopifyCredentials extracts the shop domain + token from a connection row,
// requiring a connected status and both values present.
func shopifyCredentials(conn gen.GetConnectionWithTokenRow, err error) (shop, token string, ok bool) {
	if err != nil || conn.Status != "connected" || conn.AccessToken == nil || conn.ExternalAccountID == nil {
		return "", "", false
	}
	if *conn.AccessToken == "" || *conn.ExternalAccountID == "" {
		return "", "", false
	}
	return *conn.ExternalAccountID, *conn.AccessToken, true
}

func (s *Server) approve(ctx context.Context, id uuid.UUID, price int, userID string) error {
	approvedSp := int32(price)
	approvedBy := userID
	return s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ApproveDesignPricing(ctx, gen.ApproveDesignPricingParams{
			ApprovedSp: &approvedSp, ApprovedBy: &approvedBy, DesignID: id,
		}); err != nil {
			return err
		}
		return q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designApproved, ID: id})
	})
}

// ensureShopifyProduct returns the design's Shopify draft product, creating it on
// the first attempt or reusing the one a prior attempt recorded. The product is
// recorded immediately after creation (before pricing), so a failure later in the
// publish leaves a reusable product rather than an orphan and a retry is
// idempotent. It writes the error response and returns ok=false when the caller
// should stop.
func (s *Server) ensureShopifyProduct(
	c *gin.Context, id uuid.UUID, brandSlug, shop, token string, draft shopify.ProductDraft, userID string,
) (shopify.ProductRef, bool) {
	ctx := c.Request.Context()

	existing, err := s.store.Q.GetShopifyProduct(ctx, id)
	if err == nil && existing.ProductGid != "" {
		return shopify.ProductRef{
			GID: existing.ProductGid, VariantGID: existing.VariantGid,
			Handle: existing.Handle, AdminURL: existing.AdminUrl,
		}, true
	}
	if err != nil && !isNoRows(err) {
		dbError(c, err, "That design does not exist.", "Could not load the Shopify product.")
		return shopify.ProductRef{}, false
	}

	ref, err := s.shopify.CreateProduct(ctx, shop, token, draft)
	if err != nil {
		obs.FromContext(ctx).Error("shopify create product failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, err.Error())
		return shopify.ProductRef{}, false
	}
	// Record before pricing so a retry reuses this product instead of creating a
	// duplicate. The design stays "approved" until the publish fully completes.
	if err := s.recordDraftProduct(ctx, id, brandSlug, ref, userID); err != nil {
		obs.FromContext(ctx).Error("shopify record draft failed", "design", id, "error", err)
		detail(c, http.StatusInternalServerError, "Created the product but could not save its reference.")
		return shopify.ProductRef{}, false
	}
	return ref, true
}

// recordDraftProduct upserts the created product's reference without flipping the
// design to "published" - that happens only once the publish fully completes.
func (s *Server) recordDraftProduct(
	ctx context.Context, id uuid.UUID, brandSlug string, ref shopify.ProductRef, userID string,
) error {
	publishedBy := userID
	return s.store.Q.UpsertShopifyProduct(ctx, gen.UpsertShopifyProductParams{
		DesignID: id, BrandSlug: brandSlug, ProductGid: ref.GID, VariantGid: ref.VariantGID,
		Handle: ref.Handle, AdminUrl: ref.AdminURL, Status: "draft", PublishedBy: &publishedBy,
	})
}

func (s *Server) recordPublished(
	ctx context.Context, id uuid.UUID, brandSlug string, ref shopify.ProductRef, userID string,
) error {
	publishedBy := userID
	return s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.UpsertShopifyProduct(ctx, gen.UpsertShopifyProductParams{
			DesignID: id, BrandSlug: brandSlug, ProductGid: ref.GID, VariantGid: ref.VariantGID,
			Handle: ref.Handle, AdminUrl: ref.AdminURL, Status: "draft", PublishedBy: &publishedBy,
		}); err != nil {
			return err
		}
		return q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designPublished, ID: id})
	})
}

// vendorFor uses the requested vendor, else the brand's display name.
func (s *Server) vendorFor(ctx context.Context, requested, brandSlug string) string {
	if requested != "" {
		return requested
	}
	if brand, err := s.store.Q.GetBrandBySlug(ctx, brandSlug); err == nil {
		return brand.Name
	}
	return brandSlug
}

// buildMetafields attaches the costing facts as "tensor" metafields so the
// merchant sees them in Shopify. Metric-derived fields are omitted when metrics
// are unavailable.
func buildMetafields(
	design gen.GetDesignByIDRow, pricing gen.GetDesignPricingRow,
	metrics gen.GetLatestMetricsForDesignRow, hasMetrics bool,
) []shopify.Metafield {
	mf := []shopify.Metafield{
		{Namespace: "tensor", Key: "design_cp", Type: "number_decimal", Value: fmt.Sprintf("%.2f", pricing.DesignCp)},
		{Namespace: "tensor", Key: "material", Type: "single_line_text_field", Value: design.Material},
	}
	if pricing.RecommendedSp != nil {
		mf = append(mf, shopify.Metafield{
			Namespace: "tensor", Key: "recommended_sp", Type: "number_integer",
			Value: fmt.Sprintf("%d", *pricing.RecommendedSp),
		})
	}
	if hasMetrics {
		mf = append(mf,
			shopify.Metafield{Namespace: "tensor", Key: "print_time_hr", Type: "number_decimal", Value: fmt.Sprintf("%.4f", metrics.PrintTimeHr)},
			shopify.Metafield{Namespace: "tensor", Key: "filament_g", Type: "number_decimal", Value: fmt.Sprintf("%.2f", metrics.FilamentG)},
			shopify.Metafield{Namespace: "tensor", Key: "units_per_bed", Type: "number_integer", Value: fmt.Sprintf("%d", metrics.UnitsPerBed)},
			shopify.Metafield{Namespace: "tensor", Key: "effective_machine_time_hr", Type: "number_decimal", Value: fmt.Sprintf("%.4f", metrics.EffectiveMachineTimeHr)},
			shopify.Metafield{Namespace: "tensor", Key: "batchable", Type: "boolean", Value: boolStr(metrics.UnitsPerBed > 1)},
		)
	}
	return mf
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
