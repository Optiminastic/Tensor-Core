package httpapi

// One SKU's own mapping of one design file.
//
// A product's mapping says what fills the file every colourway prints from. A
// SKU's override says it for one colourway, and `fieldMapsFor` returns the
// override WHOLE where it exists - never merged with the product's, for
// resolveScheme's reason: a half-override is a mapping that exists in neither
// place and that nobody chose.
//
// The column has been there since migration 0129 and the render path has read
// it all along. This is the half that was missing: a way to write one.
//
// THE PRODUCT'S ROWS AND A SKU'S LIVE IN THE SAME TABLE, which is the thing
// to keep in mind here. Both writers clear a role and write it again, so each
// has to say which kind it is clearing - see ClearProductFieldMaps, which did
// not, and took every SKU's override with it on an ordinary save.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func (s *Server) registerVariantFieldMaps(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/variants/:id/field-maps", read, s.listVariantFieldMaps)
	g.PUT("/variants/:id/field-maps", manage, s.putVariantFieldMaps)
	// Dropping the override, so the SKU follows the product again. A real
	// choice, spelled rather than inferred from an empty list - see the
	// refusal in putVariantFieldMaps.
	g.DELETE("/variants/:id/field-maps", manage, s.deleteVariantFieldMaps)
}

func (s *Server) listVariantFieldMaps(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.ListVariantFieldMaps(c.Request.Context(),
		gen.ListVariantFieldMapsParams{VariantID: &id, Role: roleParam(c)})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read this SKU's mapping.")
		return
	}
	out := make([]fieldMapResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, fieldMapResponse{
			Role: r.Role, Required: r.Required,
			PropertyKey: r.PropertyKey, ScadVariable: r.ScadVariable,
			ValueType: r.ValueType, Position: r.Position,
			FixedValue: deref(r.FixedValue), DefaultValue: deref(r.DefaultValue),
			MaxRunes: r.MaxRunes,
		})
	}
	c.JSON(http.StatusOK, out)
}

// putVariantFieldMaps replaces one SKU's whole mapping of one design file.
func (s *Server) putVariantFieldMaps(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req fieldMapWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Each mapped field needs an order field, a variable, and a type of string or number.")
		return
	}

	// AN EMPTY LIST IS REFUSED, not stored.
	//
	// fieldMapsFor treats "no rows" as "this SKU has no override" and falls
	// back to the product's mapping. So a stored-empty override and no
	// override at all are the same state read two ways, and saving an empty
	// list would look like pinning the SKU to nothing while silently meaning
	// the opposite. Saying it outright is the only way it can mean one thing.
	if len(req.Maps) == 0 {
		detail(c, http.StatusUnprocessableEntity,
			"An empty mapping would just make this SKU follow the product's. "+
				"Remove the override to say that.")
		return
	}

	ctx := c.Request.Context()
	variant, err := s.store.Q.GetVariant(ctx, id)
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "No such SKU.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read that SKU.")
		return
	}

	entries, ok := validateFieldMapEntries(c, req)
	if !ok {
		return
	}
	role := roleParam(c)

	// CHECKED AGAINST THIS SKU'S OWN FILE, which is the point of the override
	// and the reason this cannot reuse mappingNamesUnknownVariables: that one
	// resolves the template product-wide and gives up when the SKUs disagree -
	// which is exactly the state a product configured per SKU is in, so the
	// check would silently never run on the products it matters most for.
	//
	// Best-effort in the same way: no renderer, no file yet, or an unreadable
	// source allows the save. A check that cannot see the file must not
	// report it as broken.
	if template, found := s.templateForVariant(ctx, variant.ProductID, deref(variant.Sku), role); found {
		// The helper takes stored rows; these are not stored yet, which is
		// the point of checking now. Only the two fields it reads are needed.
		pending := make([]gen.ProductFieldMap, 0, len(entries))
		for _, e := range entries {
			row := gen.ProductFieldMap{ScadVariable: e.variable}
			if e.fixed != "" {
				v := e.fixed
				row.FixedValue = &v
			}
			pending = append(pending, row)
		}
		if missing := s.undeclaredVariables(ctx, template, pending); len(missing) > 0 {
			detail(c, http.StatusUnprocessableEntity, undeclaredVariablesDetail(template, missing))
			return
		}
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearVariantFieldMaps(ctx, gen.ClearVariantFieldMapsParams{
			VariantID: &id, Role: role,
		}); err != nil {
			return err
		}
		for i, e := range entries {
			var fixed *string
			if e.fixed != "" {
				v := e.fixed
				fixed = &v
			}
			var deflt *string
			if e.deflt != "" {
				v := e.deflt
				deflt = &v
			}
			if err := q.InsertVariantFieldMap(ctx, gen.InsertVariantFieldMapParams{
				ID: uuid.New(), ProductID: variant.ProductID, VariantID: &id,
				PropertyKey: e.key, ScadVariable: e.variable, ValueType: e.valueType,
				Role: role, Required: e.required, FixedValue: fixed,
				DefaultValue: deflt, MaxRunes: e.maxRunes, Position: int32(i),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save this SKU's mapping.")
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteVariantFieldMaps drops the override, so the SKU follows the product.
func (s *Server) deleteVariantFieldMaps(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.Q.ClearVariantFieldMaps(c.Request.Context(),
		gen.ClearVariantFieldMapsParams{VariantID: &id, Role: roleParam(c)}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove this SKU's mapping.")
		return
	}
	c.Status(http.StatusNoContent)
}

// undeclaredVariablesDetail is what the screen says when a mapping names a
// variable the file does not read.
//
// The same sentence the product-level route uses, because it is the same
// mistake: OpenSCAD accepts an unknown -D and never reads it, so the model
// comes out missing exactly the thing the customer asked for, and exits 0.
func undeclaredVariablesDetail(template string, missing []string) string {
	return template + " does not declare " + strings.Join(missing, ", ") +
		". OpenSCAD accepts a variable it does not read, so a model mapped to it " +
		"would come out missing what the customer asked for."
}
