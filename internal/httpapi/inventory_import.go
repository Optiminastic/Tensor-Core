package httpapi

// Bulk import for the two inventory shelves, from an .xlsx.
//
// ALL OR NOTHING. Every row is checked before anything is written, and the
// writes run in one transaction. A file half-applied is the worst outcome for
// a stock list: the operator cannot tell which rows landed, and re-uploading
// the fixed file would then be the only way to find out.
//
// Each row means exactly what the matching Add dialog means - the same keys
// (material + colour for filament, the case-insensitive name for an item) and
// the same rule that the count given REPLACES the count held. Re-importing the
// same file is therefore safe: it changes nothing.

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// maxImportRows bounds one file. The shop's whole shelf is a few hundred rows;
// a file far past that is the wrong file.
const maxImportRows = 5000

// importColumn is one column a sheet may carry, matched by any of its names.
type importColumn struct {
	key      string
	names    []string // lower-case, compared after trimming
	required bool
}

var filamentImportColumns = []importColumn{
	{key: "material", names: []string{"material"}, required: true},
	{key: "colour", names: []string{"colour", "color"}},
	{key: "grams", names: []string{"grams available", "grams", "grams_available", "stock (g)", "stock"}, required: true},
	{key: "reorder", names: []string{"reorder level (g)", "reorder level", "reorder_level_grams", "reorder (g)", "reorder"}},
}

var itemImportColumns = []importColumn{
	{key: "name", names: []string{"name", "item", "item name"}, required: true},
	{key: "quantity", names: []string{"quantity", "qty"}, required: true},
	{key: "unit", names: []string{"unit"}, required: true},
	{key: "price", names: []string{"unit price", "unit_price", "price"}},
	{key: "code", names: []string{"code", "part code"}},
}

// filamentImportRow is one validated filament row. reorder is nil when the
// cell was blank, which keeps an existing row's reorder level.
type filamentImportRow struct {
	material string
	colour   *string
	grams    float64
	reorder  *float64
}

// itemImportRow is one validated item row.
type itemImportRow struct {
	name      string
	quantity  float64
	unit      string
	unitPrice *float64
	code      *string
}

// sheetCells reads one row's cells by column key.
type sheetCells struct {
	index map[string]int
	row   []string
}

func (s sheetCells) get(key string) string {
	i, ok := s.index[key]
	if !ok || i >= len(s.row) {
		return ""
	}
	return strings.TrimSpace(s.row[i])
}

// mapHeaders finds each column in row 1, or says which required one is missing.
func mapHeaders(header []string, cols []importColumn) (map[string]int, []string) {
	index := map[string]int{}
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(h))
		for _, col := range cols {
			if _, seen := index[col.key]; seen {
				continue
			}
			for _, n := range col.names {
				if name == n {
					index[col.key] = i
				}
			}
		}
	}
	var problems []string
	for _, col := range cols {
		if _, ok := index[col.key]; col.required && !ok {
			problems = append(problems, fmt.Sprintf("Row 1: no %q column.", col.names[0]))
		}
	}
	return index, problems
}

// importRows walks the data rows, skipping blank ones, and hands each to fn
// with its spreadsheet row number.
func importRows(rows [][]string, cols []importColumn, fn func(n int, c sheetCells)) []string {
	if len(rows) == 0 {
		return []string{"The first sheet is empty. Row 1 must hold the column headings."}
	}
	index, problems := mapHeaders(rows[0], cols)
	if len(problems) > 0 {
		return problems
	}
	if len(rows)-1 > maxImportRows {
		return []string{fmt.Sprintf("The sheet has %d rows; at most %d are accepted at once.",
			len(rows)-1, maxImportRows)}
	}
	for i, row := range rows[1:] {
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		fn(i+2, sheetCells{index: index, row: row})
	}
	return nil
}

// parseSheetNumber reads a typed number, tolerating thousands separators, a
// rupee sign and a trailing unit - a sheet typed by a person says "1,000 g".
func parseSheetNumber(raw string) (float64, bool) {
	s := strings.NewReplacer(",", "", "₹", "", " ", "").Replace(raw)
	s = strings.TrimRight(strings.ToLower(s), "abcdefghijklmnopqrstuvwxyz")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// optionalNumber reads a cell that may be blank. ok is false only for a cell
// holding something that is not a non-negative number.
func optionalNumber(raw string) (*float64, bool) {
	if raw == "" {
		return nil, true
	}
	v, ok := parseSheetNumber(raw)
	if !ok || v < 0 {
		return nil, false
	}
	return &v, true
}

func parseFilamentSheet(rows [][]string) ([]filamentImportRow, []string) {
	var out []filamentImportRow
	var problems []string
	seen := map[string]int{}
	structural := importRows(rows, filamentImportColumns, func(n int, c sheetCells) {
		material := c.get("material")
		if material == "" || len(material) > 255 {
			problems = append(problems, fmt.Sprintf("Row %d: Material is empty or too long.", n))
			return
		}
		grams, ok := parseSheetNumber(c.get("grams"))
		if !ok || grams < 0 {
			problems = append(problems, fmt.Sprintf("Row %d: Grams available %q is not a number of grams.", n, c.get("grams")))
			return
		}
		reorder, ok := optionalNumber(c.get("reorder"))
		if !ok {
			problems = append(problems, fmt.Sprintf("Row %d: Reorder level %q is not a number of grams.", n, c.get("reorder")))
			return
		}
		colour := nonEmptyPtr(c.get("colour"))
		key := strings.ToLower(material + "\x00" + deref(colour))
		if first, dup := seen[key]; dup {
			problems = append(problems, fmt.Sprintf("Row %d: %s %s is already on row %d.", n, material, deref(colour), first))
			return
		}
		seen[key] = n
		out = append(out, filamentImportRow{material: material, colour: colour, grams: grams, reorder: reorder})
	})
	return out, append(structural, problems...)
}

func parseItemSheet(rows [][]string) ([]itemImportRow, []string) {
	var out []itemImportRow
	var problems []string
	seen := map[string]int{}
	structural := importRows(rows, itemImportColumns, func(n int, c sheetCells) {
		name := c.get("name")
		if name == "" {
			problems = append(problems, fmt.Sprintf("Row %d: Name is empty.", n))
			return
		}
		qty, ok := parseSheetNumber(c.get("quantity"))
		if !ok || qty < 0 {
			problems = append(problems, fmt.Sprintf("Row %d: Quantity %q is not a number.", n, c.get("quantity")))
			return
		}
		unit := strings.ToLower(c.get("unit"))
		if !inventoryUnits[unit] {
			problems = append(problems, fmt.Sprintf("Row %d: %q is not a unit this shelf counts in. Use one of: %s.",
				n, c.get("unit"), strings.Join(InventoryUnits(), ", ")))
			return
		}
		price, ok := optionalNumber(c.get("price"))
		if !ok {
			problems = append(problems, fmt.Sprintf("Row %d: Unit price %q is not a price.", n, c.get("price")))
			return
		}
		code := nonEmptyPtr(strings.ToUpper(c.get("code")))
		if code != nil && len(*code) > 64 {
			problems = append(problems, fmt.Sprintf("Row %d: Code is longer than 64 characters.", n))
			return
		}
		key := strings.ToLower(name)
		if first, dup := seen[key]; dup {
			problems = append(problems, fmt.Sprintf("Row %d: %q is already on row %d.", n, name, first))
			return
		}
		seen[key] = n
		out = append(out, itemImportRow{name: name, quantity: qty, unit: unit, unitPrice: price, code: code})
	})
	return out, append(structural, problems...)
}

// readImportSheet opens the uploaded workbook's first sheet, answering the
// caller itself and returning false when it cannot.
func readImportSheet(c *gin.Context) ([][]string, bool) {
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
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The first sheet could not be read.")
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
}

func (s *Server) importFilament(c *gin.Context) {
	rows, ok := readImportSheet(c)
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
	var res importResponse
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		for _, r := range parsed {
			created, err := importOneFilament(c, q, r)
			if err != nil {
				return err
			}
			if created {
				res.Created++
			} else {
				res.Updated++
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the filament. Nothing was imported.")
		return
	}
	c.JSON(http.StatusOK, res)
}

// importOneFilament applies one row, reporting whether it created a new row.
func importOneFilament(c *gin.Context, q *gen.Queries, r filamentImportRow) (bool, error) {
	ctx := c.Request.Context()
	existing, err := q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
		Material: r.material, Colour: r.colour,
	})
	if isNoRows(err) {
		reorder := 0.0
		if r.reorder != nil {
			reorder = *r.reorder
		}
		_, err = q.InsertFilament(ctx, gen.InsertFilamentParams{
			ID: uuid.New(), Material: r.material, Colour: r.colour,
			GramsAvailable: r.grams, ReorderLevelGrams: reorder,
		})
		return true, err
	}
	if err != nil {
		return false, err
	}
	reorder := db.NumFloat(existing.ReorderLevelGrams)
	if r.reorder != nil {
		reorder = *r.reorder
	}
	_, err = q.UpdateFilamentLevel(ctx, gen.UpdateFilamentLevelParams{
		ID: existing.ID, GramsAvailable: r.grams, ReorderLevelGrams: reorder,
	})
	return false, err
}

func (s *Server) importInventoryItems(c *gin.Context) {
	rows, ok := readImportSheet(c)
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
	var res importResponse
	var failed string
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		for _, r := range parsed {
			item, err := q.UpsertInventoryItem(c.Request.Context(), gen.UpsertInventoryItemParams{
				ID: uuid.New(), Name: r.name, Quantity: r.quantity, Unit: r.unit,
				UnitPrice: r.unitPrice, Code: r.code,
			})
			if err != nil {
				failed = r.name
				return err
			}
			// A row inserted just now has the two timestamps from one now().
			if item.CreatedAt.Time.Equal(item.UpdatedAt.Time) {
				res.Created++
			} else {
				res.Updated++
			}
		}
		return nil
	})
	if isUniqueViolation(err) {
		detail(c, http.StatusConflict, fmt.Sprintf(
			"%q has a part code another item already uses. Nothing was imported.", failed))
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the items. Nothing was imported.")
		return
	}
	c.JSON(http.StatusOK, res)
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
		{"Material", "Colour", "Grams available", "Reorder level (g)"},
		{"PLA", "BLACK", 5000, 1000},
	})
}

func (s *Server) inventoryItemsImportTemplate(c *gin.Context) {
	sendImportTemplate(c, "inventory-items-import.xlsx", [][]any{
		{"Name", "Quantity", "Unit", "Unit price", "Code"},
		{"Gift box", 200, "piece", 12.5, ""},
	})
}
