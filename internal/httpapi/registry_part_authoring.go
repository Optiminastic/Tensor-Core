package httpapi

// Naming, renaming, rescoping and removing the things a product prints.
//
// A part was derived from its files, and the panel therefore had nowhere to
// put the two things somebody decides BEFORE choosing one: what it is called,
// and whether it prints one file for every colourway or a file per child SKU.
// Without the second, the page had to show a product-wide picker AND a row
// per SKU for every part, and nothing on screen said which was meant to win.
//
// THE ROLE IS A JOIN KEY. The same string addresses variant_designs,
// product_field_maps, design_colour_parts and production_jobs.part_role, so
// renaming is four updates and a part row in one transaction, and removing is
// three deletes. Doing either halfway leaves a part whose files belong to a
// name nothing refers to, and the next render holds the job complaining about
// a part that is no longer on any screen.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// maxPartRoleRunes matches the column, so an over-long name is refused with a
// sentence rather than as a constraint violation.
const maxPartRoleRunes = 64

type partWriteRequest struct {
	Role string `json:"role" binding:"required,max=64"`
	// Absent means a file per child SKU, which is the case that needs the
	// registry at all: colourways that are one geometry can be said in one
	// click, colourways that are different drawings cannot be said any other
	// way.
	PerVariant *bool `json:"per_variant"`
}

type partScopeRequest struct {
	Role       string `json:"role" binding:"omitempty,max=64"`
	PerVariant *bool  `json:"per_variant"`
}

// normalisedRole is the one spelling of a part name.
//
// Lower-cased and collapsed, because every table that joins on it compares
// case-insensitively and "Rose " and "rose" must not become two parts.
func normalisedRole(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}

// createProductPart names a part, with no file yet.
func (s *Server) createProductPart(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	var req partWriteRequest
	if !bindJSON(c, &req) {
		return
	}
	role := normalisedRole(req.Role)
	if role == "" || len([]rune(role)) > maxPartRoleRunes {
		detail(c, http.StatusUnprocessableEntity,
			"Give the part a name - what an operator will read beside the job.")
		return
	}
	perVariant := true
	if req.PerVariant != nil {
		perVariant = *req.PerVariant
	}
	part, err := s.store.Q.CreateProductPart(c.Request.Context(), gen.CreateProductPartParams{
		ID: uuid.New(), ProductID: product.ID, Role: role, PerVariant: perVariant,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not add the part.")
		return
	}
	c.JSON(http.StatusCreated, partResponse(part))
}

// updateProductPart renames a part, changes how it is scoped, or both.
func (s *Server) updateProductPart(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req partScopeRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	part, err := s.store.Q.GetProductPart(ctx, id)
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That part is not on this product any more.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the part.")
		return
	}

	out := part
	if req.PerVariant != nil && *req.PerVariant != part.PerVariant {
		if out, err = s.store.Q.SetProductPartScope(ctx, gen.SetProductPartScopeParams{
			ID: id, PerVariant: *req.PerVariant,
		}); err != nil {
			detail(c, http.StatusInternalServerError, "Could not change how the part is configured.")
			return
		}
	}

	role := normalisedRole(req.Role)
	if role == "" || role == strings.ToLower(part.Role) {
		c.JSON(http.StatusOK, partResponse(out))
		return
	}
	if len([]rune(role)) > maxPartRoleRunes {
		detail(c, http.StatusUnprocessableEntity, "That name is too long for a part.")
		return
	}
	renamed, ok := s.renamePart(c, part, role)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, partResponse(renamed))
}

// renamePart carries one rename through every table the role addresses.
func (s *Server) renamePart(
	c *gin.Context, part gen.ProductPart, role string,
) (gen.ProductPart, bool) {
	ctx := c.Request.Context()
	var out gen.ProductPart
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		if out, err = q.RenameProductPart(ctx, gen.RenameProductPartParams{
			ID: part.ID, Role: role,
		}); err != nil {
			return err
		}
		if _, err = q.RenameVariantDesignsRole(ctx, gen.RenameVariantDesignsRoleParams{
			ProductID: part.ProductID, FromRole: part.Role, Role: role,
		}); err != nil {
			return err
		}
		if _, err = q.RenameProductFieldMapsRole(ctx, gen.RenameProductFieldMapsRoleParams{
			ProductID: part.ProductID, FromRole: part.Role, Role: role,
		}); err != nil {
			return err
		}
		if _, err = q.RenameColourPartsRole(ctx, gen.RenameColourPartsRoleParams{
			ProductID: part.ProductID, FromRole: part.Role, Role: role,
		}); err != nil {
			return err
		}
		_, err = q.RenameJobPartRole(ctx, gen.RenameJobPartRoleParams{
			ProductID: part.ProductID, FromRole: part.Role, Role: role,
		})
		return err
	})
	if err != nil {
		detail(c, http.StatusConflict,
			"Could not rename the part - another part of this product may already be called that.")
		return gen.ProductPart{}, false
	}
	return out, true
}

// deleteProductPart removes a part and everything configured against it.
func (s *Server) deleteProductPart(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	part, err := s.store.Q.GetProductPart(ctx, id)
	if isNoRows(err) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the part.")
		return
	}

	// REFUSED WHILE WORK STILL HAS TO PRINT FROM IT. Deleting takes the files
	// and the mapping with it, so a job that has not printed would be left
	// unable to render with nothing on the floor saying why.
	n, err := s.store.Q.CountUnfinishedJobsForPartRole(ctx,
		gen.CountUnfinishedJobsForPartRoleParams{ProductID: part.ProductID, Role: part.Role})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check for work on this part.")
		return
	}
	if n > 0 {
		detail(c, http.StatusConflict,
			"There is still work to print from this part. Finish or cancel it first.")
		return
	}

	if err := s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.DeleteVariantDesignsForRole(ctx, gen.DeleteVariantDesignsForRoleParams{
			ProductID: part.ProductID, Role: part.Role,
		}); err != nil {
			return err
		}
		if _, err := q.DeleteProductFieldMapsForRole(ctx, gen.DeleteProductFieldMapsForRoleParams{
			ProductID: part.ProductID, Role: part.Role,
		}); err != nil {
			return err
		}
		if _, err := q.DeleteColourPartsForRole(ctx, gen.DeleteColourPartsForRoleParams{
			ProductID: part.ProductID, Role: part.Role,
		}); err != nil {
			return err
		}
		return q.DeleteProductPart(ctx, part.ID)
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the part.")
		return
	}
	c.Status(http.StatusNoContent)
}

func partResponse(p gen.ProductPart) productPartResponse {
	return productPartResponse{
		ID: p.ID.String(), Role: p.Role, PerVariant: p.PerVariant,
	}
}
