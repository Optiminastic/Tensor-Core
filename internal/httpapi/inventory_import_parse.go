package httpapi

// Reading an inventory sheet the way the floor actually keeps one.
//
// The shop's stock workbook is not shaped like Tensor's tables: filament is a
// colour and a CLOSING count of spools with no material, boxes are a name, a
// size column with no heading, and Total / Used / Remaining. So the parser
// recognises what it can under many names, converts spools to grams, defaults
// what the sheet leaves out, and keeps every other column as a custom column
// rather than refusing the file.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// maxImportRows bounds one file. The whole shelf is a few hundred rows.
	maxImportRows = 5000
	// gramsPerSpool converts a spool count. The shop buys 1 kg spools.
	gramsPerSpool = 1000.0
	// defaultImportMaterial is used when a sheet has no Material column: every
	// AMS tray in the fleet reports PLA.
	defaultImportMaterial = "PLA"
	// defaultImportUnit is used when an items sheet has no Unit column.
	defaultImportUnit = "piece"
	// blankHeadingName names a column that has values but no heading.
	blankHeadingName = "Details"
)

// importColumn is one column the importer understands, under any of its names.
type importColumn struct {
	key   string
	names []string // lower-case, compared after collapsing spaces
}

var filamentImportColumns = []importColumn{
	{key: "material", names: []string{"material", "type", "filament type"}},
	{key: "colour", names: []string{"colour", "color", "colour name", "color name"}},
	{key: "hex", names: []string{"hex", "colour hex", "color hex", "hex code", "colour code", "color code", "swatch"}},
	{key: "grams", names: []string{"grams available", "grams", "grams_available", "stock (g)", "weight (g)"}},
	{key: "spools", names: []string{"closing", "closing stock", "spools", "spool count", "remaining", "in stock", "stock", "balance"}},
	{key: "reorder", names: []string{"reorder level (g)", "reorder level", "reorder_level_grams", "reorder (g)", "reorder"}},
}

var itemImportColumns = []importColumn{
	{key: "name", names: []string{"name", "item", "item name", "boxes", "box", "product"}},
	{key: "quantity", names: []string{"quantity", "qty", "remaining", "closing", "closing stock", "in stock", "stock", "balance"}},
	{key: "unit", names: []string{"unit", "units", "uom"}},
	{key: "price", names: []string{"unit price", "unit_price", "price", "rate"}},
	{key: "code", names: []string{"code", "part code", "sku"}},
}

// filamentImportRow is one validated filament row. reorder is nil when the
// cell was blank, which keeps an existing row's reorder level.
type filamentImportRow struct {
	material string
	colour   *string
	grams    float64
	reorder  *float64
	// hex is the swatch the sheet gave, empty when it gave none.
	hex   string
	extra map[string]string
}

// itemImportRow is one validated item row.
type itemImportRow struct {
	name      string
	quantity  float64
	unit      string
	unitPrice *float64
	code      *string
	extra     map[string]string
}

// extraColumn is a sheet column kept as a custom column.
type extraColumn struct {
	name  string
	index int
}

// sheetLayout is where each understood column sits, and what is kept as custom.
type sheetLayout struct {
	index  map[string]int
	extras []extraColumn
}

// sheetCells reads one row through a layout.
type sheetCells struct {
	layout sheetLayout
	row    []string
}

func trimmedCell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func (s sheetCells) has(key string) bool { _, ok := s.layout.index[key]; return ok }

func (s sheetCells) get(key string) string {
	i, ok := s.layout.index[key]
	if !ok {
		return ""
	}
	return trimmedCell(s.row, i)
}

// extra is the row's non-empty custom values.
func (s sheetCells) extra() map[string]string {
	out := map[string]string{}
	for _, e := range s.layout.extras {
		if v := trimmedCell(s.row, e.index); v != "" {
			out[e.name] = v
		}
	}
	return out
}

func normaliseHeading(h string) string {
	return strings.ToLower(strings.Join(strings.Fields(h), " "))
}

// columnHasData reports whether any data row has a value in column i.
func columnHasData(rows [][]string, i int) bool {
	for _, r := range rows[1:] {
		if trimmedCell(r, i) != "" {
			return true
		}
	}
	return false
}

// layoutSheet maps row 1. Each understood column is taken by its first
// heading; every other heading - and a blank heading over data - becomes a
// custom column, de-duplicated so no two share a name.
func layoutSheet(rows [][]string, cols []importColumn) sheetLayout {
	layout := sheetLayout{index: map[string]int{}}
	used := map[string]bool{}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	for i := 0; i < width; i++ {
		heading := trimmedCell(rows[0], i)
		if key := matchColumn(normaliseHeading(heading), cols); key != "" {
			if _, taken := layout.index[key]; !taken {
				layout.index[key] = i
				continue
			}
		}
		if heading == "" {
			if !columnHasData(rows, i) {
				continue
			}
			heading = blankHeadingName
		}
		base, _ := cleanColumnName(heading)
		if r := []rune(base); len(r) > maxColumnName-3 {
			base = string(r[:maxColumnName-3])
		}
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s %d", base, n)
		}
		used[strings.ToLower(name)] = true
		layout.extras = append(layout.extras, extraColumn{name: name, index: i})
	}
	return layout
}

func matchColumn(heading string, cols []importColumn) string {
	for _, col := range cols {
		for _, n := range col.names {
			if heading == n {
				return col.key
			}
		}
	}
	return ""
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

// stockCell reads a stock figure where blank means none in stock - the shop's
// box list leaves the count empty for boxes it has run out of.
func stockCell(raw string) (float64, bool) {
	v, ok := optionalNumber(raw)
	if !ok {
		return 0, false
	}
	if v == nil {
		return 0, true
	}
	return *v, true
}

// walkSheet checks the shape and hands each non-blank data row to fn with its
// spreadsheet row number.
func walkSheet(rows [][]string, cols []importColumn, fn func(n int, c sheetCells)) (sheetLayout, []string) {
	if len(rows) == 0 {
		return sheetLayout{}, []string{"The sheet is empty. Row 1 must hold the column headings."}
	}
	if len(rows)-1 > maxImportRows {
		return sheetLayout{}, []string{fmt.Sprintf("The sheet has %d rows; at most %d are accepted at once.",
			len(rows)-1, maxImportRows)}
	}
	layout := layoutSheet(rows, cols)
	for i, row := range rows[1:] {
		if strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		fn(i+2, sheetCells{layout: layout, row: row})
	}
	return layout, nil
}

// isTotalRow reports a summary line - the shop's sheets end in a TOTAL row,
// which would otherwise import as a colour, or a box, called "Total".
func isTotalRow(label string) bool {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "total", "totals", "grand total", "sum", "sub total", "subtotal":
		return true
	}
	return false
}

// emptyStock reports a stock cell that is blank or zero.
func emptyStock(raw string) bool {
	v, ok := parseSheetNumber(raw)
	return raw == "" || (ok && v == 0)
}

func parseFilamentSheet(rows [][]string) ([]filamentImportRow, []string) {
	var out []filamentImportRow
	var problems []string
	seen := map[string]int{}
	layout, structural := walkSheet(rows, filamentImportColumns, func(n int, c sheetCells) {
		row, problem, skip := filamentRow(n, c)
		if skip {
			return
		}
		if problem == "" {
			key := strings.ToLower(row.material + "\x00" + deref(row.colour))
			if first, dup := seen[key]; dup {
				problem = fmt.Sprintf("Row %d: %s %s is already on row %d.", n, row.material, deref(row.colour), first)
			} else {
				seen[key] = n
				out = append(out, row)
			}
		}
		if problem != "" {
			problems = append(problems, problem)
		}
	})
	if structural == nil && len(rows) > 0 {
		structural = requireFilamentColumns(layout)
	}
	return out, append(structural, problems...)
}

func requireFilamentColumns(l sheetLayout) []string {
	var p []string
	_, material := l.index["material"]
	_, colour := l.index["colour"]
	if !material && !colour {
		p = append(p, `Row 1: no "Colour" (or "Material") column.`)
	}
	_, grams := l.index["grams"]
	_, spools := l.index["spools"]
	if !grams && !spools {
		p = append(p, `Row 1: no stock column - name one "Grams available" or "Closing" (spools).`)
	}
	return p
}

// filamentRow reads one filament row. skip is true for a row with nothing to
// record: no material, no colour and no stock.
func filamentRow(n int, c sheetCells) (filamentImportRow, string, bool) {
	material, colour := c.get("material"), c.get("colour")
	if isTotalRow(colour) || isTotalRow(material) {
		return filamentImportRow{}, "", true
	}
	stockRaw := c.get("grams")
	if !c.has("grams") {
		stockRaw = c.get("spools")
	}
	if material == "" && colour == "" {
		if emptyStock(stockRaw) {
			return filamentImportRow{}, "", true
		}
		return filamentImportRow{}, fmt.Sprintf("Row %d: has stock but no colour or material.", n), false
	}
	if material == "" {
		material = defaultImportMaterial
	}
	if len(material) > 255 || len(colour) > 255 {
		return filamentImportRow{}, fmt.Sprintf("Row %d: Material or colour is too long.", n), false
	}
	stock, ok := stockCell(stockRaw)
	if !ok {
		return filamentImportRow{}, fmt.Sprintf("Row %d: stock %q is not a number.", n, stockRaw), false
	}
	grams := stock
	if !c.has("grams") {
		grams = stock * gramsPerSpool
	}
	reorder, ok := optionalNumber(c.get("reorder"))
	if !ok {
		return filamentImportRow{}, fmt.Sprintf("Row %d: Reorder level %q is not a number of grams.", n, c.get("reorder")), false
	}
	hex := ""
	if raw := c.get("hex"); raw != "" {
		normalised, ok := normaliseHex(raw)
		if !ok {
			return filamentImportRow{}, fmt.Sprintf("Row %d: %q is not a colour code like #1A1A1A.", n, raw), false
		}
		hex = normalised
	}
	return filamentImportRow{
		material: material, colour: nonEmptyPtr(colour), grams: grams, reorder: reorder,
		hex: hex, extra: c.extra(),
	}, "", false
}

func parseItemSheet(rows [][]string) ([]itemImportRow, []string) {
	var out []itemImportRow
	var problems []string
	layout, structural := walkSheet(rows, itemImportColumns, func(n int, c sheetCells) {
		row, problem, skip := itemRow(n, c)
		if skip {
			return
		}
		if problem != "" {
			problems = append(problems, problem)
			return
		}
		out = append(out, row)
	})
	if structural == nil && len(rows) > 0 {
		if _, ok := layout.index["name"]; !ok {
			structural = []string{`Row 1: no "Name" column.`}
		}
	}
	out, dupProblems := disambiguateItems(out, rows)
	return out, append(append(structural, problems...), dupProblems...)
}

// itemRow reads one item row. skip is true for a row with no name and no stock.
func itemRow(n int, c sheetCells) (itemImportRow, string, bool) {
	name := c.get("name")
	if isTotalRow(name) {
		return itemImportRow{}, "", true
	}
	if name == "" {
		if emptyStock(c.get("quantity")) {
			return itemImportRow{}, "", true
		}
		return itemImportRow{}, fmt.Sprintf("Row %d: has a quantity but no name.", n), false
	}
	qty, ok := stockCell(c.get("quantity"))
	if !ok {
		return itemImportRow{}, fmt.Sprintf("Row %d: Quantity %q is not a number.", n, c.get("quantity")), false
	}
	unit := strings.ToLower(c.get("unit"))
	if unit == "" {
		unit = defaultImportUnit
	}
	if !inventoryUnits[unit] {
		return itemImportRow{}, fmt.Sprintf("Row %d: %q is not a unit this shelf counts in. Use one of: %s.",
			n, c.get("unit"), strings.Join(InventoryUnits(), ", ")), false
	}
	price, ok := optionalNumber(c.get("price"))
	if !ok {
		return itemImportRow{}, fmt.Sprintf("Row %d: Unit price %q is not a price.", n, c.get("price")), false
	}
	code := nonEmptyPtr(strings.ToUpper(c.get("code")))
	if code != nil && len(*code) > 64 {
		return itemImportRow{}, fmt.Sprintf("Row %d: Code is longer than 64 characters.", n), false
	}
	return itemImportRow{
		name: name, quantity: qty, unit: unit, unitPrice: price, code: code, extra: c.extra(),
	}, "", false
}

// disambiguateItems keeps two rows that share a name apart by their first
// custom value - the box list has "Moon box" at 18x18x18 and at 15x15x10,
// which are two shelves. A repeat nothing tells apart is a problem.
func disambiguateItems(rows []itemImportRow, sheet [][]string) ([]itemImportRow, []string) {
	count := map[string]int{}
	for _, r := range rows {
		count[strings.ToLower(r.name)]++
	}
	order := sheetExtraOrder(sheet, itemImportColumns)
	var problems []string
	seen := map[string]bool{}
	for i, r := range rows {
		if count[strings.ToLower(r.name)] > 1 {
			if v := firstExtra(r.extra, order); v != "" {
				rows[i].name = fmt.Sprintf("%s (%s)", r.name, shortLabel(v))
			}
		}
		key := strings.ToLower(rows[i].name)
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%q appears more than once and nothing on the rows tells them apart.", rows[i].name))
		}
		seen[key] = true
	}
	return rows, problems
}

func firstExtra(extra map[string]string, order []string) string {
	for _, name := range order {
		if v := extra[name]; v != "" {
			return v
		}
	}
	return ""
}

// shortLabel is the part of a description before its first " - ", which is
// where the box list puts the size: "34x27.5x8 - Mailer box ...".
func shortLabel(v string) string {
	if i := strings.Index(v, " - "); i > 0 {
		v = v[:i]
	}
	if r := []rune(v); len(r) > 40 {
		v = string(r[:40])
	}
	return strings.TrimSpace(v)
}

// importColumnsUsed is every custom column name carried by the rows, in sheet
// order - only those with at least one value are created.
func importColumnsUsed(order []string, extras []map[string]string) []string {
	var out []string
	for _, name := range order {
		for _, e := range extras {
			if e[name] != "" {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
