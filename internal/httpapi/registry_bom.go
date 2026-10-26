package httpapi

// A bill of materials belongs to the PRODUCT, and a SKU may override it.
//
// variant_bom is right for a part only one colourway carries and wrong for
// every part they all do. A red heart goes on every Dual Name Plank; declared
// per variant that is forty-four rows saying the same thing, and forty-four
// chances for one of them to drift.

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// resolveBom picks the bill of materials one variant is built from.
//
// ALL OR NOTHING, the same rule resolveScheme applies to colour, and for a
// sharper reason. A variant that lists any part of its own lists all of them.
//
// Merging the two is the tempting alternative and it builds a kit that exists
// in neither list. Say the product carries a heart and a box, and one SKU
// overrides with a bigger box: merged, it gets the bigger box AND the original,
// and the assembly bench packs two. Nothing refuses it, the cost comes out
// wrong, and it reaches a customer looking finished. Replacing outright means a
// half-written override is visibly half-written.
func resolveBom(own, shared []bomLineResponse) []bomLineResponse {
	if len(own) > 0 {
		return own
	}
	return shared
}

// getProductBom returns the lines every variant of a product inherits.
func (s *Server) getProductBom(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.ListProductBom(c.Request.Context(), product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the bill of materials.")
		return
	}
	out := make([]bomLineResponse, 0, len(rows))
	for _, b := range rows {
		qty := db.NumFloat(b.Quantity)
		line := bomLineResponse{
			ItemID: b.ItemID.String(), ItemCode: b.ItemCode, ItemName: b.ItemName,
			Unit: b.Unit, Quantity: qty,
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
	if !costsVisibleTo(c) {
		stripBomCosts(out)
	}
	c.JSON(http.StatusOK, out)
}

// putProductBom replaces a product's bill of materials, whole.
//
// One transaction, like putVariantBom and for the same reason: a BOM is edited
// as a list and saved once, so a half-applied edit cannot leave a product
// carrying a battery and no switch.
func (s *Server) putProductBom(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	var req bomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Each line needs a part and a quantity above zero.")
		return
	}

	ids := make([]uuid.UUID, 0, len(req.Lines))
	seen := map[uuid.UUID]bool{}
	for _, line := range req.Lines {
		id, err := uuid.Parse(line.ItemID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "That part id is not valid.")
			return
		}
		// Caught by name rather than by the unique index, so the message says
		// which part rather than being a constraint violation.
		if seen[id] {
			detail(c, http.StatusUnprocessableEntity,
				"The same part is listed twice. Give it one line and the total quantity.")
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}

	ctx := c.Request.Context()
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearProductBom(ctx, product.ID); err != nil {
			return err
		}
		for i, line := range req.Lines {
			if err := q.ReplaceProductBomItem(ctx, gen.ReplaceProductBomItemParams{
				ID: uuid.New(), ProductID: product.ID, InventoryItemID: ids[i],
				Quantity: line.Quantity,
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
	s.getProductBom(c)
}
