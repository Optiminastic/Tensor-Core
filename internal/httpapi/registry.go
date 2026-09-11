package httpapi

// The product registry: what a product IS, as against what happened to one.
//
// Every other production router here records work - orders that arrived, jobs
// that were made, beds that printed. This one serves the master data underneath
// them: which products exist, in what variants, printed from which file, built
// from which parts.
//
// It exists because that knowledge currently lives in Go constants
// (generatedSKUSegments, templateForHearts) and three embedded .scad files, so
// adding a colour or a heart count means a developer, a recompile and a deploy.
//
// Read is config:read and write is config:manage rather than a pair of their
// own. A registry is configuration in exactly the sense that permission already
// names - "cost assumptions, materials and machines" - and adding registry:read
// would mean a catalog entry, a reseed, a permissions-version bump and an
// update to the separation-of-duties tests, to express a distinction nobody has
// asked for.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type productResponse struct {
	ID     string  `json:"id"`
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Status string  `json:"status"`
	Notes  *string `json:"notes"`
	// OptionCount and VariantCount say how far a product has been registered.
	// A product with options but no variants is half-defined, and that is what
	// somebody opening the page needs to see without clicking into each one.
	OptionCount  int64 `json:"option_count"`
	VariantCount int64 `json:"variant_count"`
}

type optionValueResponse struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Label string `json:"label"`
}

type productOptionResponse struct {
	ID     string                `json:"id"`
	Code   string                `json:"code"`
	Label  string                `json:"label"`
	Values []optionValueResponse `json:"values"`
}

type variantResponse struct {
	ID     string  `json:"id"`
	SKU    *string `json:"sku"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	// Options is "heart_count=2,light=wired" - what this variant IS, since a
	// variant is a combination of option values rather than a row of columns.
	Options   string `json:"options"`
	PartCount int64  `json:"part_count"`
	// Parts and Design travel with the variant so the panel can expand one
	// without another request. Six variants would otherwise be six round trips
	// to open one row, growing with every colour the shop adds.
	Parts  []bomLineResponse      `json:"parts"`
	Design *variantDesignResponse `json:"design"`
	// PartsCost is what one of this variant's kits costs, or null when any part
	// has no recorded price - a total that silently treated unknown as zero
	// would read as a real figure.
	PartsCost *float64 `json:"parts_cost"`
}

type bomLineResponse struct {
	ItemID   string  `json:"item_id"`
	ItemCode *string `json:"item_code"`
	ItemName string  `json:"item_name"`
	Unit     string  `json:"unit"`
	Quantity float64 `json:"quantity"`
	// UnitPrice is null when nobody has recorded one, which is different from
	// free - a BOM that totalled unknown prices as zero would understate every
	// product carrying that part.
	UnitPrice *float64 `json:"unit_price"`
	LineCost  *float64 `json:"line_cost"`
	// InStock is null on the product-detail path, which answers "what is this
	// made of" rather than "can we build it today" and never reads the shelf. A
	// zero there would read as "none left" - the one answer nobody asked for.
	InStock *float64 `json:"in_stock"`
}

type variantDesignResponse struct {
	Role        string  `json:"role"`
	TemplateKey *string `json:"template_key"`
	DesignID    *string `json:"design_id"`
	Version     int32   `json:"version"`
}

// productDetailResponse is everything the Products tab needs for one product,
// in one request: its axes, its variants, and nothing else.
type productDetailResponse struct {
	productResponse
	Options  []productOptionResponse `json:"options"`
	Variants []variantResponse       `json:"variants"`
	// Designs are the files this product's variants actually print from, shown
	// with the product rather than in a list of their own. A template means
	// nothing on its own - "dnp_two_heart" is only legible next to the product
	// whose two-heart variant renders from it.
	Designs []templateResponse `json:"designs"`
}

func (s *Server) registerRegistry(r *gin.Engine) {
	g := r.Group("/registry")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.ConfigRead.Key())
	manage := s.guards.RequirePermission(auth.ConfigManage.Key())

	g.GET("/products", read, s.listRegistryProducts)
	g.POST("/products", manage, s.createRegistryProduct)
	g.GET("/products/:code", read, s.getRegistryProduct)
	g.PATCH("/products/:code", manage, s.updateRegistryProduct)
	g.DELETE("/products/:code", manage, s.deleteRegistryProduct)
	g.GET("/variants/:id/bom", read, s.getVariantBom)
	g.PUT("/variants/:id/bom", manage, s.putVariantBom)
}

func (s *Server) listRegistryProducts(c *gin.Context) {
	rows, err := s.store.Q.ListProducts(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list products.")
		return
	}
	out := make([]productResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, productResponse{
			ID: p.ID.String(), Code: p.Code, Name: p.Name, Kind: p.Kind,
			Status: p.Status, Notes: p.Notes,
			OptionCount: p.OptionCount, VariantCount: p.VariantCount,
		})
	}
	c.JSON(http.StatusOK, out)
}

// getRegistryProduct answers with one product's whole definition.
//
// Addressed by CODE rather than id: the code is what an order's SKU carries and
// what a person says out loud ("the DNP"), so it is what a URL should carry too.
func (s *Server) getRegistryProduct(c *gin.Context) {
	ctx := c.Request.Context()
	product, err := s.store.Q.GetProductByCode(ctx, strings.TrimSpace(c.Param("code")))
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That product is not in the registry.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product.")
		return
	}

	options, err := s.store.Q.ListProductOptions(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product's options.")
		return
	}
	values, err := s.store.Q.ListOptionValues(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product's options.")
		return
	}
	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product's variants.")
		return
	}

	boms, err := s.store.Q.ListBomForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the bills of materials.")
		return
	}
	designs, err := s.store.Q.ListDesignsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the variants' designs.")
		return
	}

	partsByVariant := map[uuid.UUID][]bomLineResponse{}
	// Null once ANY part on the variant has no price, rather than summing what
	// is known: a partial total is indistinguishable from a complete one on
	// screen, and this figure exists to be trusted.
	costKnown := map[uuid.UUID]bool{}
	costByVariant := map[uuid.UUID]float64{}
	for _, b := range boms {
		qty := db.NumFloat(b.Quantity)
		line := bomLineResponse{
			ItemID: b.ItemID.String(), ItemCode: b.ItemCode,
			ItemName: b.ItemName, Unit: b.Unit, Quantity: qty,
		}
		if _, seen := costKnown[b.VariantID]; !seen {
			costKnown[b.VariantID] = true
		}
		if price := db.NumFloatPtr(b.UnitPrice); price != nil {
			line.UnitPrice = price
			cost := *price * qty
			line.LineCost = &cost
			costByVariant[b.VariantID] += cost
		} else {
			costKnown[b.VariantID] = false
		}
		partsByVariant[b.VariantID] = append(partsByVariant[b.VariantID], line)
	}

	designByVariant := map[uuid.UUID]variantDesignResponse{}
	for _, d := range designs {
		if d.Role != "body" {
			continue
		}
		out := variantDesignResponse{Role: d.Role, TemplateKey: d.TemplateKey, Version: d.Version}
		if d.DesignID != nil {
			id := d.DesignID.String()
			out.DesignID = &id
		}
		designByVariant[d.VariantID] = out
	}

	byOption := map[uuid.UUID][]optionValueResponse{}
	for _, v := range values {
		byOption[v.OptionID] = append(byOption[v.OptionID], optionValueResponse{
			ID: v.ID.String(), Code: v.Code, Label: v.Label,
		})
	}

	out := productDetailResponse{
		productResponse: productResponse{
			ID: product.ID.String(), Code: product.Code, Name: product.Name,
			Kind: product.Kind, Status: product.Status, Notes: product.Notes,
			OptionCount: int64(len(options)), VariantCount: int64(len(variants)),
		},
		Options:  make([]productOptionResponse, 0, len(options)),
		Variants: make([]variantResponse, 0, len(variants)),
	}

	// Distinct keys in variant order, so a product that prints three variants
	// from one template lists that template once.
	seenKey := map[string]bool{}
	keys := make([]string, 0, len(designs))
	for _, d := range designs {
		if d.TemplateKey == nil || seenKey[*d.TemplateKey] {
			continue
		}
		seenKey[*d.TemplateKey] = true
		keys = append(keys, *d.TemplateKey)
	}
	if out.Designs, err = s.templatesForKeys(ctx, keys); err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product's design files.")
		return
	}
	for _, o := range options {
		// An axis with no values yet is a real state - it is what you have the
		// moment you add one - and a nil slice marshals to null, not []. The
		// page reads that as a malformed response and refuses to render at all,
		// so one half-finished option hides every product on the screen.
		values := byOption[o.ID]
		if values == nil {
			values = []optionValueResponse{}
		}
		out.Options = append(out.Options, productOptionResponse{
			ID: o.ID.String(), Code: o.Code, Label: o.Label, Values: values,
		})
	}
	for _, v := range variants {
		parts := partsByVariant[v.ID]
		if parts == nil {
			parts = []bomLineResponse{}
		}
		row := variantResponse{
			ID: v.ID.String(), SKU: v.Sku, Name: v.Name, Status: v.Status,
			Options: string(v.OptionCodes), PartCount: v.PartCount,
			Parts: parts,
		}
		if d, ok := designByVariant[v.ID]; ok {
			row.Design = &d
		}
		if known, seen := costKnown[v.ID]; seen && known {
			cost := costByVariant[v.ID]
			row.PartsCost = &cost
		}
		out.Variants = append(out.Variants, row)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getVariantBom(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.ListVariantBom(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the bill of materials.")
		return
	}
	out := make([]bomLineResponse, 0, len(rows))
	for _, b := range rows {
		qty := db.NumFloat(b.Quantity)
		line := bomLineResponse{
			ItemID: b.InventoryItemID.String(), ItemCode: b.ItemCode,
			ItemName: b.ItemName, Unit: b.Unit, Quantity: qty,
		}
		stock := db.NumFloat(b.StockQuantity)
		line.InStock = &stock
		if price := db.NumFloatPtr(b.UnitPrice); price != nil {
			line.UnitPrice = price
			cost := *price * qty
			line.LineCost = &cost
		}
		out = append(out, line)
	}
	c.JSON(http.StatusOK, out)
}

// bomRequest is a whole bill of materials, saved at once.
type bomRequest struct {
	Lines []struct {
		ItemID   string  `json:"item_id" binding:"required"`
		Quantity float64 `json:"quantity" binding:"gt=0"`
	} `json:"lines"`
}

// putVariantBom replaces a variant's bill of materials.
//
// The whole list, in one transaction, rather than per-line POST and DELETE. A
// BOM is edited as a list and saved once, so a half-applied edit cannot leave a
// variant carrying a battery and no switch - and a variant with the wrong parts
// is worse than one with none, because it looks finished.
func (s *Server) putVariantBom(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req bomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Each part needs an item and a quantity above zero.")
		return
	}

	type line struct {
		item uuid.UUID
		qty  float64
	}
	lines := make([]line, 0, len(req.Lines))
	for _, l := range req.Lines {
		itemID, err := uuid.Parse(l.ItemID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "That is not a valid part id.")
			return
		}
		lines = append(lines, line{item: itemID, qty: l.Quantity})
	}

	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		if err := q.ClearVariantBom(c.Request.Context(), id); err != nil {
			return err
		}
		for _, l := range lines {
			if err := q.ReplaceVariantBomItem(c.Request.Context(), gen.ReplaceVariantBomItemParams{
				ID: uuid.New(), VariantID: id, InventoryItemID: l.item, Quantity: l.qty,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the bill of materials.")
		return
	}
	s.getVariantBom(c)
}

// productWriteRequest is a product as somebody typed it.
//
// `kind` is constrained rather than free text because it is not a label: it
// decides whether a job waits for Tensor to render a file or for a person to
// upload one, and a third value would mean a job that waits for neither.
type productWriteRequest struct {
	Code   string  `json:"code" binding:"required,max=32"`
	Name   string  `json:"name" binding:"required,max=160"`
	Kind   string  `json:"kind" binding:"required,oneof=generated uploaded"`
	Status string  `json:"status" binding:"required,oneof=active retired"`
	Notes  *string `json:"notes"`
}

// normalise trims what a person typed. The code is upper-cased because it is
// the segment an order's SKU carries ("T3DPS-DNP-2"), and a product registered
// as "dnp" that never matches an order is a silent failure.
func (r *productWriteRequest) normalise() {
	r.Code = strings.ToUpper(strings.TrimSpace(r.Code))
	r.Name = strings.TrimSpace(r.Name)
	if r.Notes != nil {
		if n := strings.TrimSpace(*r.Notes); n == "" {
			r.Notes = nil
		} else {
			r.Notes = &n
		}
	}
}

func (s *Server) createRegistryProduct(c *gin.Context) {
	var req productWriteRequest
	if !bindJSON(c, &req) {
		return
	}
	req.normalise()

	row, err := s.store.Q.InsertProduct(c.Request.Context(), gen.InsertProductParams{
		ID: uuid.New(), Code: req.Code, Name: req.Name,
		Kind: req.Kind, Status: req.Status, Notes: req.Notes,
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "A product with that code already exists.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the product.")
		return
	}
	c.JSON(http.StatusCreated, productResponse{
		ID: row.ID.String(), Code: row.Code, Name: row.Name,
		Kind: row.Kind, Status: row.Status, Notes: row.Notes,
	})
}

func (s *Server) updateRegistryProduct(c *gin.Context) {
	ctx := c.Request.Context()
	existing, err := s.store.Q.GetProductByCode(ctx, strings.TrimSpace(c.Param("code")))
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That product is not in the registry.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product.")
		return
	}

	var req productWriteRequest
	if !bindJSON(c, &req) {
		return
	}
	req.normalise()

	row, err := s.store.Q.UpdateProduct(ctx, gen.UpdateProductParams{
		ID: existing.ID, Code: req.Code, Name: req.Name,
		Kind: req.Kind, Status: req.Status, Notes: req.Notes,
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "Another product already uses that code.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the product.")
		return
	}
	c.JSON(http.StatusOK, productResponse{
		ID: row.ID.String(), Code: row.Code, Name: row.Name,
		Kind: row.Kind, Status: row.Status, Notes: row.Notes,
	})
}

// deleteRegistryProduct removes a product and everything defined under it.
//
// The cascade reaches options, variants, designs and bills of materials, which
// is a lot to lose to a stray click - so the caller must name the code it
// believes it is deleting, and it must match. Nothing else in the registry is
// destructive, and this is the one place a mis-click cannot be undone.
func (s *Server) deleteRegistryProduct(c *gin.Context) {
	ctx := c.Request.Context()
	code := strings.TrimSpace(c.Param("code"))
	existing, err := s.store.Q.GetProductByCode(ctx, code)
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That product is not in the registry.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product.")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(c.Query("confirm")), existing.Code) {
		detail(c, http.StatusBadRequest,
			"Confirm the deletion by sending the product's code.")
		return
	}
	if err := s.store.Q.DeleteProduct(ctx, existing.ID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the product.")
		return
	}
	c.Status(http.StatusNoContent)
}
