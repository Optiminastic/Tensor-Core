package httpapi

// The non-filament shelf: boxes, inserts, cards, tape.
//
// Filament has its own router because it is consumed by a print and reserved by
// the planner. These items are consumed by SHIPPING one, which nothing in the
// pipeline models - so this is a plain stock list, and deliberately stays one.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// inventoryUnits are the units an item may be counted in.
//
// A fixed set rather than free text: "pcs", "pieces", "Piece" and "piece" are
// one unit typed four ways, and a shelf that reports two of them as different
// things is worse than one that refuses the fourth spelling. Validated here
// rather than by a CHECK constraint so adding one is a code change reviewed
// beside the dropdown that offers it.
var inventoryUnits = map[string]bool{
	"kg": true, "g": true, "litre": true, "ml": true, "metre": true,
	"unit": true, "piece": true, "pack": true, "box": true, "roll": true,
	"sheet": true, "pair": true,
}

// InventoryUnits is that set as a sorted list, for the error message and for
// anything that needs to offer the choice.
func InventoryUnits() []string {
	out := make([]string, 0, len(inventoryUnits))
	for u := range inventoryUnits {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

type inventoryItemResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Code is the stable handle a bill of materials points at, so a BOM line
	// survives the shelf being renamed. Null until a part is used by a product.
	Code      *string   `json:"code"`
	Quantity  float64   `json:"quantity"`
	Unit      string    `json:"unit"`
	UnitPrice *float64  `json:"unit_price"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toInventoryItemResponse(i gen.InventoryItem) inventoryItemResponse {
	out := inventoryItemResponse{
		ID: i.ID.String(), Name: i.Name, Unit: i.Unit,
		Quantity:  db.NumFloat(i.Quantity),
		CreatedAt: db.Time(i.CreatedAt), UpdatedAt: db.Time(i.UpdatedAt),
	}
	out.UnitPrice = db.NumFloatPtr(i.UnitPrice)
	out.Code = i.Code
	return out
}

// upsertInventoryItemRequest is the Add-item dialog's payload.
//
// UnitPrice is a pointer so "not recorded" survives the round trip: binding it
// as a plain float64 would turn an omitted price into ₹0, which reads as free
// rather than unknown.
//
// Code is a pointer for a different reason: the Inventory dialog does not offer
// the field at all, so an omitted code must leave an existing one alone rather
// than clearing the handle every BOM points at. The queries COALESCE it.
type upsertInventoryItemRequest struct {
	Name      string   `json:"name" binding:"required"`
	Quantity  float64  `json:"quantity" binding:"gte=0"`
	Unit      string   `json:"unit" binding:"required"`
	UnitPrice *float64 `json:"unit_price"`
	Code      *string  `json:"code"`
}

func (s *Server) registerInventoryItems(r *gin.Engine) {
	g := r.Group("/inventory-items")
	g.Use(s.guards.RequireUser())
	// Guarded on the filament permissions rather than a pair of its own. This
	// is the same Inventory page and the same people keep it: a role that may
	// count spools may count the boxes they ship in. Adding inventory:read and
	// inventory:manage would mean a catalog change, a reseed and a permissions
	// version bump to express a distinction nobody has asked for.
	g.GET("", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listInventoryItems)
	g.POST("", s.guards.RequirePermission(auth.FilamentManage.Key()), s.upsertInventoryItem)
	// PATCH rather than a second POST: this one is addressed by id, which is
	// what lets it rename. The POST above is keyed on the name and exists so
	// re-adding an item restocks it.
	g.PATCH("/:id", s.guards.RequirePermission(auth.FilamentManage.Key()), s.updateInventoryItem)
	g.DELETE("/:id", s.guards.RequirePermission(auth.FilamentManage.Key()), s.deleteInventoryItem)
}

func (s *Server) listInventoryItems(c *gin.Context) {
	rows, err := s.store.Q.ListInventoryItems(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list inventory items.")
		return
	}
	out := make([]inventoryItemResponse, 0, len(rows))
	for _, i := range rows {
		out = append(out, toInventoryItemResponse(i))
	}
	c.JSON(http.StatusOK, out)
}

// validatedItem is a request that has passed every check, so add and edit
// cannot drift on what a valid item is.
type validatedItem struct {
	name      string
	unit      string
	quantity  float64
	unitPrice *float64
	code      *string
}

// bindInventoryItem parses and validates the dialog's payload, answering the
// caller with false once it has already written the error response.
func bindInventoryItem(c *gin.Context) (validatedItem, bool) {
	var req upsertInventoryItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Give the item a name, a quantity and a unit.")
		return validatedItem{}, false
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		detail(c, http.StatusUnprocessableEntity, "Give the item a name.")
		return validatedItem{}, false
	}
	unit := strings.ToLower(strings.TrimSpace(req.Unit))
	if !inventoryUnits[unit] {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"%q is not a unit this shelf counts in. Use one of: %s.",
			req.Unit, strings.Join(InventoryUnits(), ", ")))
		return validatedItem{}, false
	}
	if req.UnitPrice != nil && *req.UnitPrice < 0 {
		detail(c, http.StatusUnprocessableEntity, "A price cannot be negative.")
		return validatedItem{}, false
	}
	code, ok := validItemCode(c, req.Code)
	if !ok {
		return validatedItem{}, false
	}
	return validatedItem{
		name: name, unit: unit, quantity: req.Quantity,
		unitPrice: req.UnitPrice, code: code,
	}, true
}

// validItemCode normalises a part code, or nil when none was sent.
//
// Upper-cased and trimmed so "led-001", "LED-001 " and "Led-001" are one part.
// A blank string is treated as absent rather than as an empty code: the Registry
// form sends "" when the field is cleared, and storing that would create a row
// that collides with every other blank one under the unique index.
func validItemCode(c *gin.Context, raw *string) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	code := strings.ToUpper(strings.TrimSpace(*raw))
	if code == "" {
		return nil, true
	}
	if len(code) > 64 {
		detail(c, http.StatusUnprocessableEntity, "A part code is at most 64 characters.")
		return nil, false
	}
	return &code, true
}

func (s *Server) upsertInventoryItem(c *gin.Context) {
	req, ok := bindInventoryItem(c)
	if !ok {
		return
	}

	item, err := s.store.Q.UpsertInventoryItem(c.Request.Context(), gen.UpsertInventoryItemParams{
		ID: uuid.New(), Name: req.name, Unit: req.unit,
		Quantity:  req.quantity,
		UnitPrice: req.unitPrice,
		Code:      req.code,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the inventory item.")
		return
	}
	c.JSON(http.StatusCreated, toInventoryItemResponse(item))
}

// updateInventoryItem edits an item in place, including its name.
func (s *Server) updateInventoryItem(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid item id.")
		return
	}
	req, ok := bindInventoryItem(c)
	if !ok {
		return
	}

	item, err := s.store.Q.UpdateInventoryItem(c.Request.Context(), gen.UpdateInventoryItemParams{
		ID: id, Name: req.name, Unit: req.unit,
		Quantity:  req.quantity,
		UnitPrice: req.unitPrice,
		Code:      req.code,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That inventory item no longer exists.")
		return
	}
	if isUniqueViolation(err) {
		// Renaming onto another item would merge two shelves into one silently,
		// losing whichever count was not kept.
		// Either name or code - both are unique case-insensitively, and the
		// message names both so the operator knows which field to change.
		detail(c, http.StatusConflict,
			"An item with that name or part code is already on the shelf.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the inventory item.")
		return
	}
	c.JSON(http.StatusOK, toInventoryItemResponse(item))
}

func (s *Server) deleteInventoryItem(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid item id.")
		return
	}
	rows, err := s.store.Q.DeleteInventoryItem(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the inventory item.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "That inventory item no longer exists.")
		return
	}
	c.Status(http.StatusNoContent)
}
