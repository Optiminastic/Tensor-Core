package httpapi

// Writing the registry: option axes, their values, the variants those make, and
// which file prints each one.
//
// Read lives in registry.go; this is everything that changes it. Kept apart
// because the read path is one query fan-out and these are a dozen small
// mutations with their own ownership checks - a variant may only be built from
// ITS product's option values, and nothing in the URL proves that.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func (s *Server) registerRegistryAuthoring(r *gin.Engine) {
	g := r.Group("/registry")
	g.Use(s.guards.RequireUser())
	manage := s.guards.RequirePermission(auth.ConfigManage.Key())

	g.POST("/products/:code/options", manage, s.createProductOption)
	g.PATCH("/options/:id", manage, s.updateProductOption)
	g.DELETE("/options/:id", manage, s.deleteProductOption)

	g.POST("/options/:id/values", manage, s.createOptionValue)
	g.PATCH("/option-values/:id", manage, s.updateOptionValue)
	g.DELETE("/option-values/:id", manage, s.deleteOptionValue)

	g.POST("/products/:code/variants", manage, s.createVariant)
	g.PATCH("/variants/:id", manage, s.updateVariant)
	g.DELETE("/variants/:id", manage, s.deleteVariant)
	g.PUT("/variants/:id/design", manage, s.setVariantDesign)
}

// optionWriteRequest is one axis of choice: heart_count, light, colour.
type optionWriteRequest struct {
	Code     string `json:"code" binding:"required,max=32"`
	Label    string `json:"label" binding:"required,max=80"`
	Position int32  `json:"position"`
}

// optionValueWriteRequest is one allowed answer on an axis.
type optionValueWriteRequest struct {
	Code     string `json:"code" binding:"required,max=48"`
	Label    string `json:"label" binding:"required,max=80"`
	Position int32  `json:"position"`
}

// variantWriteRequest is one sellable combination.
//
// OptionValueIDs is the whole set, not a delta: a variant IS its option values,
// so a partial write would leave one that is "2 hearts" and nothing else. Empty
// is allowed - a product with no axes yet still has a variant to sell.
type variantWriteRequest struct {
	Name           string   `json:"name" binding:"required,max=200"`
	SKU            *string  `json:"sku"`
	Status         string   `json:"status" binding:"required,oneof=active retired"`
	OptionValueIDs []string `json:"option_value_ids"`
}

// variantDesignRequest names the file that prints one role of a variant.
//
// Exactly one of TemplateKey and DesignID, mirroring the CHECK on the table
// rather than trusting a caller: a row with both would mean two files print the
// same part, and a row with neither means nothing prints it at all.
type variantDesignRequest struct {
	Role        string  `json:"role" binding:"required,oneof=body base"`
	TemplateKey *string `json:"template_key"`
	DesignID    *string `json:"design_id"`
}

func trimmed(v string) string { return strings.TrimSpace(v) }

// productFromCode resolves the :code path segment, answering 404 itself.
func (s *Server) productFromCode(c *gin.Context) (gen.Product, bool) {
	product, err := s.store.Q.GetProductByCode(c.Request.Context(), trimmed(c.Param("code")))
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That product is not in the registry.")
		return gen.Product{}, false
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product.")
		return gen.Product{}, false
	}
	return product, true
}

func (s *Server) createProductOption(c *gin.Context) {
	product, ok := s.productFromCode(c)
	if !ok {
		return
	}
	var req optionWriteRequest
	if !bindJSON(c, &req) {
		return
	}

	row, err := s.store.Q.InsertProductOption(c.Request.Context(), gen.InsertProductOptionParams{
		ID: uuid.New(), ProductID: product.ID,
		Code: trimmed(req.Code), Label: trimmed(req.Label), Position: req.Position,
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "This product already has an option with that code.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not add the option.")
		return
	}
	c.JSON(http.StatusCreated, productOptionResponse{
		ID: row.ID.String(), Code: row.Code, Label: row.Label,
		Values: []optionValueResponse{},
	})
}

func (s *Server) updateProductOption(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req optionWriteRequest
	if !bindJSON(c, &req) {
		return
	}

	row, err := s.store.Q.UpdateProductOption(c.Request.Context(), gen.UpdateProductOptionParams{
		ID: id, Code: trimmed(req.Code), Label: trimmed(req.Label), Position: req.Position,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That option does not exist.")
		return
	}
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "This product already has an option with that code.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the option.")
		return
	}
	c.JSON(http.StatusOK, productOptionResponse{
		ID: row.ID.String(), Code: row.Code, Label: row.Label,
		Values: []optionValueResponse{},
	})
}

func (s *Server) deleteProductOption(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.Q.DeleteProductOption(c.Request.Context(), id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the option.")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) createOptionValue(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req optionValueWriteRequest
	if !bindJSON(c, &req) {
		return
	}

	row, err := s.store.Q.InsertOptionValue(c.Request.Context(), gen.InsertOptionValueParams{
		ID: uuid.New(), OptionID: id,
		Code: trimmed(req.Code), Label: trimmed(req.Label), Position: req.Position,
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "That option already has a value with that code.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not add the value.")
		return
	}
	c.JSON(http.StatusCreated, optionValueResponse{
		ID: row.ID.String(), Code: row.Code, Label: row.Label,
	})
}

func (s *Server) updateOptionValue(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req optionValueWriteRequest
	if !bindJSON(c, &req) {
		return
	}

	row, err := s.store.Q.UpdateOptionValue(c.Request.Context(), gen.UpdateOptionValueParams{
		ID: id, Code: trimmed(req.Code), Label: trimmed(req.Label), Position: req.Position,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That option value does not exist.")
		return
	}
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "That option already has a value with that code.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the value.")
		return
	}
	c.JSON(http.StatusOK, optionValueResponse{
		ID: row.ID.String(), Code: row.Code, Label: row.Label,
	})
}

func (s *Server) deleteOptionValue(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.Q.DeleteOptionValue(c.Request.Context(), id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the value.")
		return
	}
	c.Status(http.StatusNoContent)
}

// parseOwnedValueIDs checks every option value exists and belongs to product.
//
// The IDs arrive from a client that can send anything, and the tables give no
// direct link from a value to a product - only value -> option -> product. A
// variant built from another product's values would be a combination that
// cannot occur, and would render as one product wearing another's options.
func (s *Server) parseOwnedValueIDs(
	c *gin.Context, raw []string, productID uuid.UUID,
) ([]uuid.UUID, bool) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, v := range raw {
		id, err := uuid.Parse(trimmed(v))
		if err != nil {
			detail(c, http.StatusBadRequest, "One of the option values is not a valid id.")
			return nil, false
		}
		owner, err := s.store.Q.GetOptionValueProduct(c.Request.Context(), id)
		if isNoRows(err) {
			detail(c, http.StatusBadRequest, "One of the option values does not exist.")
			return nil, false
		}
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not check the option values.")
			return nil, false
		}
		if owner != productID {
			detail(c, http.StatusBadRequest,
				"An option value belongs to a different product.")
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// normaliseSKU keeps a blank SKU as NULL rather than "".
//
// Nine live plank lines carry no SKU and are matched by product name; an empty
// string would collide with every other blank under the unique index and make
// the second of them unsaveable.
func normaliseSKU(sku *string) *string {
	if sku == nil {
		return nil
	}
	trimmedSKU := trimmed(*sku)
	if trimmedSKU == "" {
		return nil
	}
	return &trimmedSKU
}

func (s *Server) createVariant(c *gin.Context) {
	product, ok := s.productFromCode(c)
	if !ok {
		return
	}
	var req variantWriteRequest
	if !bindJSON(c, &req) {
		return
	}
	valueIDs, ok := s.parseOwnedValueIDs(c, req.OptionValueIDs, product.ID)
	if !ok {
		return
	}

	variantID := uuid.New()
	// One transaction: a variant that exists but carries none of its option
	// values is not a lesser version of the thing, it is a different and wrong
	// thing - it would match no order and show as a blank combination.
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		if _, err := q.InsertVariant(c.Request.Context(), gen.InsertVariantParams{
			ID: variantID, ProductID: product.ID, Sku: normaliseSKU(req.SKU),
			Name: trimmed(req.Name), Status: req.Status,
		}); err != nil {
			return err
		}
		for _, valueID := range valueIDs {
			if err := q.AddVariantOptionValue(c.Request.Context(),
				gen.AddVariantOptionValueParams{VariantID: variantID, OptionValueID: valueID},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "Another variant already uses that SKU.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the variant.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": variantID.String()})
}

func (s *Server) updateVariant(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	existing, err := s.store.Q.GetVariant(c.Request.Context(), id)
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That variant does not exist.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the variant.")
		return
	}

	var req variantWriteRequest
	if !bindJSON(c, &req) {
		return
	}
	valueIDs, ok := s.parseOwnedValueIDs(c, req.OptionValueIDs, existing.ProductID)
	if !ok {
		return
	}

	// Replace the option set whole, like a bill of materials: a variant edited
	// half-way would be two combinations at once.
	err = s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		if _, err := q.UpdateVariant(c.Request.Context(), gen.UpdateVariantParams{
			ID: id, Sku: normaliseSKU(req.SKU),
			Name: trimmed(req.Name), Status: req.Status,
		}); err != nil {
			return err
		}
		if err := q.ClearVariantOptions(c.Request.Context(), id); err != nil {
			return err
		}
		for _, valueID := range valueIDs {
			if err := q.AddVariantOptionValue(c.Request.Context(),
				gen.AddVariantOptionValueParams{VariantID: id, OptionValueID: valueID},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, "Another variant already uses that SKU.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the variant.")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteVariant(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.Q.DeleteVariant(c.Request.Context(), id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the variant.")
		return
	}
	c.Status(http.StatusNoContent)
}

// setVariantDesign names the file that prints one role of a variant.
//
// The previous active row is retired rather than deleted, so which file printed
// a given batch stays answerable after somebody changes it.
func (s *Server) setVariantDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req variantDesignRequest
	if !bindJSON(c, &req) {
		return
	}

	key := normaliseSKU(req.TemplateKey)
	var designID *uuid.UUID
	if raw := normaliseSKU(req.DesignID); raw != nil {
		parsed, err := uuid.Parse(*raw)
		if err != nil {
			detail(c, http.StatusBadRequest, "That design id is not valid.")
			return
		}
		designID = &parsed
	}
	if (key == nil) == (designID == nil) {
		detail(c, http.StatusBadRequest,
			"Name either a template or an uploaded design, not both and not neither.")
		return
	}

	if _, err := s.store.Q.GetVariant(c.Request.Context(), id); isNoRows(err) {
		detail(c, http.StatusNotFound, "That variant does not exist.")
		return
	} else if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the variant.")
		return
	}

	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		if err := q.SupersedeVariantDesign(c.Request.Context(),
			gen.SupersedeVariantDesignParams{VariantID: id, Role: req.Role},
		); err != nil {
			return err
		}
		_, err := q.InsertVariantDesign(c.Request.Context(), gen.InsertVariantDesignParams{
			ID: uuid.New(), VariantID: id, Role: req.Role,
			TemplateKey: key, DesignID: designID, Version: 1, Status: "active",
		})
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not set the design.")
		return
	}
	c.Status(http.StatusNoContent)
}
