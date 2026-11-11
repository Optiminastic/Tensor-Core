package httpapi

// Custom columns on the two inventory shelves (migration 0141).
//
// A shelf has a list of named columns; a row keeps its value for each in its
// own `extra`, keyed by the column's name. Guarded on the filament permissions
// like the rest of the Inventory page.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

const (
	shelfFilament = "filament"
	shelfItems    = "items"
	// maxCustomColumns per shelf: a sheet with more than this is not a stock
	// list, and every one is a column across the table.
	maxCustomColumns = 30
	maxColumnName    = 64
	maxExtraValue    = 500
)

type inventoryColumnResponse struct {
	ID       string `json:"id"`
	Shelf    string `json:"shelf"`
	Name     string `json:"name"`
	Position int32  `json:"position"`
}

func inventoryColumnDTO(c gen.InventoryCustomColumn) inventoryColumnResponse {
	return inventoryColumnResponse{ID: c.ID.String(), Shelf: c.Shelf, Name: c.Name, Position: c.Position}
}

// decodeExtra reads a row's custom values; anything unreadable is no values.
func decodeExtra(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (s *Server) registerInventoryColumns(r *gin.Engine) {
	g := r.Group("/inventory-columns")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listInventoryColumns)
	g.POST("", s.guards.RequirePermission(auth.FilamentManage.Key()), s.addInventoryColumn)
	g.DELETE("/:id", s.guards.RequirePermission(auth.FilamentManage.Key()), s.deleteInventoryColumn)
}

func validShelf(shelf string) bool { return shelf == shelfFilament || shelf == shelfItems }

func (s *Server) listInventoryColumns(c *gin.Context) {
	shelf := c.Query("shelf")
	if !validShelf(shelf) {
		detail(c, http.StatusBadRequest, "shelf must be filament or items.")
		return
	}
	rows, err := s.store.Q.ListInventoryCustomColumns(c.Request.Context(), shelf)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list the custom columns.")
		return
	}
	out := make([]inventoryColumnResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, inventoryColumnDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

type addInventoryColumnRequest struct {
	Shelf string `json:"shelf"`
	Name  string `json:"name"`
}

// cleanColumnName trims a column name and says what is wrong with it, if anything.
func cleanColumnName(raw string) (string, string) {
	name := strings.Join(strings.Fields(raw), " ")
	switch {
	case name == "":
		return "", "Give the column a name."
	case utf8.RuneCountInString(name) > maxColumnName:
		return "", fmt.Sprintf("A column name is at most %d characters.", maxColumnName)
	}
	return name, ""
}

func (s *Server) addInventoryColumn(c *gin.Context) {
	var req addInventoryColumnRequest
	if err := c.ShouldBindJSON(&req); err != nil || !validShelf(req.Shelf) {
		detail(c, http.StatusUnprocessableEntity, "Name the shelf (filament or items) and the column.")
		return
	}
	name, problem := cleanColumnName(req.Name)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	col, err := ensureInventoryColumns(c.Request.Context(), s.store.Q, req.Shelf, []string{name})
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusCreated, inventoryColumnDTO(col[name]))
}

// ensureInventoryColumns adds any of names the shelf lacks, refusing to take
// it past maxCustomColumns. Returns each name's column, keyed by the name as
// given - which may differ in case from the stored one.
func ensureInventoryColumns(
	ctx context.Context, q *gen.Queries, shelf string, names []string,
) (map[string]gen.InventoryCustomColumn, error) {
	existing, err := q.ListInventoryCustomColumns(ctx, shelf)
	if err != nil {
		return nil, fmt.Errorf("could not read the custom columns")
	}
	known := map[string]bool{}
	for _, e := range existing {
		known[strings.ToLower(e.Name)] = true
	}
	added := 0
	for _, n := range names {
		if !known[strings.ToLower(n)] {
			known[strings.ToLower(n)] = true
			added++
		}
	}
	if len(existing)+added > maxCustomColumns {
		return nil, fmt.Errorf("a shelf has at most %d custom columns; this would make %d",
			maxCustomColumns, len(existing)+added)
	}
	out := make(map[string]gen.InventoryCustomColumn, len(names))
	for _, n := range names {
		col, err := q.EnsureInventoryCustomColumn(ctx, gen.EnsureInventoryCustomColumnParams{
			ID: uuid.New(), Shelf: shelf, Name: n,
		})
		if err != nil {
			return nil, fmt.Errorf("could not save the column %q", n)
		}
		out[n] = col
	}
	return out, nil
}

// deleteInventoryColumn removes a column and its value from every row.
func (s *Server) deleteInventoryColumn(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid column id.")
		return
	}
	err = s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		col, err := q.DeleteInventoryCustomColumn(c.Request.Context(), id)
		if err != nil {
			return err
		}
		if col.Shelf == shelfFilament {
			return q.RemoveFilamentExtraKey(c.Request.Context(), col.Name)
		}
		return q.RemoveItemExtraKey(c.Request.Context(), col.Name)
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That column no longer exists.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the column.")
		return
	}
	c.Status(http.StatusNoContent)
}

type setExtraRequest struct {
	Column string `json:"column"`
	Value  string `json:"value"`
	// Values sets several columns at once - the Edit form saves every custom
	// field in one go. Takes precedence over Column/Value when present.
	Values map[string]string `json:"values"`
}

func (s *Server) setFilamentExtra(c *gin.Context)      { s.setExtra(c, shelfFilament) }
func (s *Server) setInventoryItemExtra(c *gin.Context) { s.setExtra(c, shelfItems) }

// setExtra writes one row's custom values; an empty value clears it. Several
// values are written together or not at all.
func (s *Server) setExtra(c *gin.Context, shelf string) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a valid row id.")
		return
	}
	var req setExtraRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Name the column and give a value.")
		return
	}
	values := req.Values
	if values == nil {
		values = map[string]string{req.Column: req.Value}
	}
	ctx := c.Request.Context()
	resolved, problem := s.resolveExtraValues(ctx, shelf, values)
	if problem != "" {
		detail(c, http.StatusUnprocessableEntity, problem)
		return
	}
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		for name, value := range resolved {
			if err := writeExtraValue(ctx, q, shelf, id, name, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the values.")
		return
	}
	c.Status(http.StatusNoContent)
}

// resolveExtraValues maps each named column to its stored name and trims each
// value, or says what is wrong with the first that is not acceptable.
func (s *Server) resolveExtraValues(
	ctx context.Context, shelf string, values map[string]string,
) (map[string]string, string) {
	out := make(map[string]string, len(values))
	for column, raw := range values {
		value := strings.TrimSpace(raw)
		if utf8.RuneCountInString(value) > maxExtraValue {
			return nil, fmt.Sprintf("A value is at most %d characters.", maxExtraValue)
		}
		name, ok := s.columnNamed(ctx, shelf, column)
		if !ok {
			return nil, fmt.Sprintf("This shelf has no column %q.", column)
		}
		out[name] = value
	}
	return out, ""
}

// columnNamed resolves a column by name, case-insensitively, to its stored name.
func (s *Server) columnNamed(ctx context.Context, shelf, name string) (string, bool) {
	cols, err := s.store.Q.ListInventoryCustomColumns(ctx, shelf)
	if err != nil {
		return "", false
	}
	for _, col := range cols {
		if strings.EqualFold(col.Name, strings.TrimSpace(name)) {
			return col.Name, true
		}
	}
	return "", false
}

func writeExtraValue(ctx context.Context, q *gen.Queries, shelf string, id uuid.UUID, key, value string) error {
	if value == "" {
		if shelf == shelfFilament {
			return q.ClearFilamentExtraValue(ctx, gen.ClearFilamentExtraValueParams{ID: id, Key: key})
		}
		return q.ClearItemExtraValue(ctx, gen.ClearItemExtraValueParams{ID: id, Key: key})
	}
	return mergeExtra(ctx, q, shelf, id, map[string]string{key: value})
}

func mergeExtra(ctx context.Context, q *gen.Queries, shelf string, id uuid.UUID, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if shelf == shelfFilament {
		return q.MergeFilamentExtra(ctx, gen.MergeFilamentExtraParams{ID: id, Extra: raw})
	}
	return q.MergeItemExtra(ctx, gen.MergeItemExtraParams{ID: id, Extra: raw})
}
