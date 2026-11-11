package httpapi

// Bulk import for the two inventory shelves, from an .xlsx. Parsing is in
// inventory_import_parse.go; this is the upload and the writes.
//
// ALL OR NOTHING. Every row is checked before anything is written, and the
// writes run in one transaction. A file half-applied is the worst outcome for
// a stock list: the operator cannot tell which rows landed.
//
// Each row means what the matching Add dialog means - the same keys (material
// + colour for filament, the case-insensitive name for an item) and the same
// rule that the count given REPLACES the count held. Re-importing the same
// file changes nothing.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// Which sheet each import reads when a workbook holds both shelves - the
// shop's does, as "Filament" and "Boxes". The first sheet otherwise.
var (
	filamentSheetHints = []string{"filament", "fillament", "spool"}
	itemSheetHints     = []string{"box", "item", "other", "packag"}
)

// pickSheet is the first sheet whose name carries a hint, else the first.
func pickSheet(names, hints []string) string {
	for _, n := range names {
		lower := strings.ToLower(n)
		for _, h := range hints {
			if strings.Contains(lower, h) {
				return n
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// readImportSheet opens the uploaded workbook and reads the sheet meant for
// this shelf, answering the caller itself and returning false when it cannot.
func readImportSheet(c *gin.Context, hints []string) ([][]string, bool) {
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusBadRequest, "Attach an .xlsx spreadsheet.")
		return nil, false
	}
	if header.Size > maxSheetUploadBytes {
		detail(c, http.StatusRequestEntityTooLarge, "That file is too large to be a spreadsheet.")
		return nil, false
	}
	opened, err := header.Open()
	if err != nil {
		detail(c, http.StatusBadRequest, "That file could not be read.")
		return nil, false
	}
	defer func() { _ = opened.Close() }()
	f, err := excelize.OpenReader(opened)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"That is not a readable .xlsx workbook. Save it from Excel as .xlsx and try again.")
		return nil, false
	}
	defer func() { _ = f.Close() }()
	sheet := pickSheet(f.GetSheetList(), hints)
	rows, err := f.GetRows(sheet)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("The sheet %q could not be read.", sheet))
		return nil, false
	}
	return rows, true
}

func rejectSheet(c *gin.Context, problems []string) {
	c.JSON(http.StatusUnprocessableEntity, gin.H{
		"detail":   fmt.Sprintf("The spreadsheet has %d problem(s). Nothing was imported.", len(problems)),
		"problems": problems,
	})
}

type importResponse struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	// Columns are the sheet's other columns, kept as custom columns.
	Columns []string `json:"columns"`
}

func (s *Server) importFilament(c *gin.Context) {
	rows, ok := readImportSheet(c, filamentSheetHints)
	if !ok {
		return
	}
	parsed, problems := parseFilamentSheet(rows)
	if len(problems) > 0 {
		rejectSheet(c, problems)
		return
	}
	if len(parsed) == 0 {
		detail(c, http.StatusUnprocessableEntity, "The sheet has headings but no rows to import.")
		return
	}
	extras := make([]map[string]string, len(parsed))
	for i, r := range parsed {
		extras[i] = r.extra
	}
	res := importResponse{Columns: importColumnsUsed(sheetExtraOrder(rows, filamentImportColumns), extras)}
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		ctx := c.Request.Context()
		if _, err := ensureInventoryColumns(ctx, q, shelfFilament, res.Columns); err != nil {
			return importRefusal{err}
		}
		for _, r := range parsed {
			id, created, err := importOneFilament(ctx, q, r)
			if err != nil {
				return err
			}
			if err := mergeExtra(ctx, q, shelfFilament, id, r.extra); err != nil {
				return err
			}
			if err := setImportedSwatch(ctx, q, id, r); err != nil {
				return err
			}
			countImport(&res, created)
		}
		return nil
	})
	s.finishImport(c, err, res, "Could not save the filament. Nothing was imported.")
}

// importRefusal is an error the operator can act on, shown as it is.
type importRefusal struct{ error }

func (s *Server) finishImport(c *gin.Context, err error, res importResponse, failure string) {
	var refusal importRefusal
	if asRefusal(err, &refusal) {
		detail(c, http.StatusUnprocessableEntity, refusal.Error()+". Nothing was imported.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, failure)
		return
	}
	if res.Columns == nil {
		res.Columns = []string{}
	}
	c.JSON(http.StatusOK, res)
}

func asRefusal(err error, target *importRefusal) bool {
	r, ok := err.(importRefusal)
	if ok {
		*target = r
	}
	return ok
}

func countImport(res *importResponse, created bool) {
	if created {
		res.Created++
	} else {
		res.Updated++
	}
}

func sheetExtraOrder(rows [][]string, cols []importColumn) []string {
	if len(rows) == 0 {
		return nil
	}
	var names []string
	for _, e := range layoutSheet(rows, cols).extras {
		names = append(names, e.name)
	}
	return names
}

// setImportedSwatch gives a row its colour dot. A hex the sheet gave wins;
// otherwise the name is resolved exactly as a render resolves it - the colour
// map, then the built-in table - so the shelf never holds a swatch the
// printers would not use. A name nothing knows is left without one.
func setImportedSwatch(ctx context.Context, q *gen.Queries, id uuid.UUID, r filamentImportRow) error {
	hex, overwrite := r.hex, r.hex != ""
	if hex == "" {
		resolved, err := ResolveColourHex(ctx, q, deref(r.colour))
		if err != nil {
			return nil
		}
		hex = resolved
	}
	return q.SetFilamentColourHex(ctx, gen.SetFilamentColourHexParams{ID: id, Hex: hex, Overwrite: overwrite})
}

// importOneFilament applies one row, returning its id and whether it was new.
func importOneFilament(ctx context.Context, q *gen.Queries, r filamentImportRow) (uuid.UUID, bool, error) {
	existing, err := q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
		Material: r.material, Colour: r.colour,
	})
	if isNoRows(err) {
		reorder := 0.0
		if r.reorder != nil {
			reorder = *r.reorder
		}
		row, err := q.InsertFilament(ctx, gen.InsertFilamentParams{
			ID: uuid.New(), Material: r.material, Colour: r.colour,
			GramsAvailable: r.grams, ReorderLevelGrams: reorder,
		})
		return row.ID, true, err
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	reorder := db.NumFloat(existing.ReorderLevelGrams)
	if r.reorder != nil {
		reorder = *r.reorder
	}
	_, err = q.UpdateFilamentLevel(ctx, gen.UpdateFilamentLevelParams{
		ID: existing.ID, GramsAvailable: r.grams, ReorderLevelGrams: reorder,
	})
	return existing.ID, false, err
}

func (s *Server) importInventoryItems(c *gin.Context) {
	rows, ok := readImportSheet(c, itemSheetHints)
	if !ok {
		return
	}
	parsed, problems := parseItemSheet(rows)
	if len(problems) > 0 {
		rejectSheet(c, problems)
		return
	}
	if len(parsed) == 0 {
		detail(c, http.StatusUnprocessableEntity, "The sheet has headings but no rows to import.")
		return
	}
	extras := make([]map[string]string, len(parsed))
	for i, r := range parsed {
		extras[i] = r.extra
	}
	res := importResponse{Columns: importColumnsUsed(sheetExtraOrder(rows, itemImportColumns), extras)}
	var failed string
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		ctx := c.Request.Context()
		if _, err := ensureInventoryColumns(ctx, q, shelfItems, res.Columns); err != nil {
			return importRefusal{err}
		}
		for _, r := range parsed {
			item, err := q.UpsertInventoryItem(ctx, gen.UpsertInventoryItemParams{
				ID: uuid.New(), Name: r.name, Quantity: r.quantity, Unit: r.unit,
				UnitPrice: r.unitPrice, Code: r.code,
			})
			if err != nil {
				failed = r.name
				return err
			}
			if err := mergeExtra(ctx, q, shelfItems, item.ID, r.extra); err != nil {
				return err
			}
			// A row inserted just now has both timestamps from one now().
			countImport(&res, item.CreatedAt.Time.Equal(item.UpdatedAt.Time))
		}
		return nil
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, fmt.Sprintf(
			"%q has a part code another item already uses. Nothing was imported.", failed))
		return
	}
	s.finishImport(c, err, res, "Could not save the items. Nothing was imported.")
}

// sendImportTemplate answers with a one-sheet workbook: the headings and one
// example row, so the file somebody fills in is one the importer reads.
func sendImportTemplate(c *gin.Context, filename string, rows [][]any) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow(sheet, cell, &row); err != nil {
			detail(c, http.StatusInternalServerError, "Could not build the template.")
			return
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		detail(c, http.StatusInternalServerError, "Could not build the template.")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

func (s *Server) filamentImportTemplate(c *gin.Context) {
	sendImportTemplate(c, "filament-import.xlsx", [][]any{
		{"Material", "Colour", "Colour hex", "Grams available", "Reorder level (g)"},
		{"PLA", "BLACK", "#1A1A1A", 5000, 1000},
	})
}

func (s *Server) inventoryItemsImportTemplate(c *gin.Context) {
	sendImportTemplate(c, "inventory-items-import.xlsx", [][]any{
		{"Name", "Quantity", "Unit", "Unit price", "Code"},
		{"Gift box", 200, "piece", 12.5, ""},
	})
}
