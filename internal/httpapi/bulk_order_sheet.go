package httpapi

// What a bulk order's spreadsheet must contain, and whether a given file does.
//
// THE COLUMNS ARE DERIVED, not written down here. A product's sheet asks for
// exactly the OpenSCAD variables its registry field maps say come from the
// order - NAME_L and NAME_R for a plank, plus the rose's and the keychain's own
// NAME for a Soulmate COMBO. That is the same table the renderer reads to build
// its -D flags, so a sheet that validates is a sheet the renderer can use, and
// adding a variable to a product changes the expected columns without anyone
// editing this file.
//
// Two columns are not derived, because they are not OpenSCAD variables and
// every row needs them regardless: the serial number, which is how an operator
// and a customer refer to the same line, and the colour, which a sheet carrying
// several SKUs of one product cannot do without.
//
// A PRODUCT WITH NO MAPPINGS IS SKIPPED - no sheet is expected and none is
// read. That covers both a product not in the registry at all and one that is
// there without field maps (DNP today). The alternative, demanding a sheet
// whose columns derive to nothing, would be asking for a file nobody can fill
// in correctly.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/xuri/excelize/v2"
)

// sheetColumn is one column an uploaded sheet must carry.
type sheetColumn struct {
	// Header is what row 1 must say, matched case-insensitively and ignoring
	// surrounding space: a spreadsheet typed by a person is not byte-exact.
	Header string
	// Variable is the OpenSCAD variable this column feeds, empty for the two
	// that are not variables (serial, colour).
	Variable string
	// Role is which design file wants it, so a product printing three files can
	// ask for two different NAMEs without the headers colliding.
	Role string
	// Numeric requires the cell to hold a number rather than text. Driven by the
	// field map's value_type, which is how "heart count must be a number"
	// becomes a rule rather than a convention.
	Numeric  bool
	Required bool
}

// productSheet is one product's expected sheet.
type productSheet struct {
	ProductID   uuid.UUID
	Code        string // the sheet's name, e.g. "SC"
	ProductName string
	Columns     []sheetColumn
	// Rows is how many lines the quotation promises for this product, summed
	// across every SKU of it. The file must carry exactly this many.
	Rows int
}

// Fixed headers. Capitalised as a person would write them; matching ignores case.
const (
	serialHeader = "S.No"
	colourHeader = "Colour"
)

// columnHeader names a derived column.
//
// The variable alone is not enough: a Soulmate COMBO maps NAME twice, once for
// the rose and once for the heart keychain, and two columns both headed NAME
// would be a sheet nobody could fill in correctly. The role disambiguates, and
// is appended only when it has to be.
func columnHeader(variable, role string, duplicated bool) string {
	v := strings.TrimSpace(variable)
	if !duplicated {
		return v
	}
	return fmt.Sprintf("%s (%s)", v, strings.ReplaceAll(strings.TrimSpace(role), "_", " "))
}

// buildProductSheets turns field-map rows into the sheets a file must contain.
//
// rowsByProduct carries how many units of each product the quotation promises;
// a product absent from the maps is absent from the result, which is what
// "skip what is not mapped" means in practice.
func buildProductSheets(
	maps []gen.ListFieldMapsForProductsRow, rowsByProduct map[uuid.UUID]int,
) []productSheet {
	byProduct := map[uuid.UUID][]gen.ListFieldMapsForProductsRow{}
	for _, m := range maps {
		byProduct[m.ProductID] = append(byProduct[m.ProductID], m)
	}

	out := make([]productSheet, 0, len(byProduct))
	for productID, rows := range byProduct {
		// Which variable names appear more than once, so only those carry their
		// role in the header.
		seen := map[string]int{}
		for _, r := range rows {
			seen[strings.ToUpper(strings.TrimSpace(r.ScadVariable))]++
		}

		columns := []sheetColumn{{Header: serialHeader, Numeric: true, Required: true}}
		for _, r := range rows {
			key := strings.ToUpper(strings.TrimSpace(r.ScadVariable))
			columns = append(columns, sheetColumn{
				Header:   columnHeader(r.ScadVariable, r.Role, seen[key] > 1),
				Variable: strings.TrimSpace(r.ScadVariable),
				Role:     r.Role,
				Numeric:  r.ValueType == "number",
				Required: r.Required,
			})
		}
		columns = append(columns, sheetColumn{Header: colourHeader, Required: true})

		out = append(out, productSheet{
			ProductID: productID, Code: rows[0].ProductCode,
			ProductName: rows[0].ProductName,
			Columns:     columns, Rows: rowsByProduct[productID],
		})
	}
	// Stable order, so the sample file and the error messages list sheets the
	// same way twice.
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// sheetRow is one validated line of personalisation.
type sheetRow struct {
	Serial int
	Colour string
	// Values are the OpenSCAD variables for this row, keyed "ROLE/VARIABLE" so
	// a product printing three files keeps them apart.
	Values map[string]string
}

// validateWorkbook checks an uploaded file against the expected sheets.
//
// It collects EVERY problem rather than stopping at the first. Somebody fixing
// a hundred-row spreadsheet needs the whole list in one pass; returning "row 3
// is wrong" and then "row 7 is wrong" after they re-upload is how a five-minute
// job becomes an afternoon.
func validateWorkbook(f *excelize.File, sheets []productSheet) (map[string][]sheetRow, []string) {
	var problems []string
	out := map[string][]sheetRow{}

	present := map[string]bool{}
	for _, name := range f.GetSheetList() {
		present[strings.ToUpper(strings.TrimSpace(name))] = true
	}

	for _, sheet := range sheets {
		actual := findSheet(f, sheet.Code)
		if actual == "" {
			problems = append(problems, fmt.Sprintf(
				"The file has no sheet named %q, which this order needs for %s.",
				sheet.Code, sheet.ProductName))
			continue
		}
		rows, err := f.GetRows(actual)
		if err != nil {
			problems = append(problems, fmt.Sprintf("Sheet %q could not be read.", actual))
			continue
		}
		if len(rows) == 0 {
			problems = append(problems, fmt.Sprintf("Sheet %q is empty.", actual))
			continue
		}

		index, headerProblems := matchHeaders(actual, rows[0], sheet.Columns)
		problems = append(problems, headerProblems...)
		if len(headerProblems) > 0 {
			// Without a header match the row checks would report every cell as
			// wrong, which buries the one error that matters.
			continue
		}

		body := rows[1:]
		// Trailing blank rows are what a spreadsheet grows when somebody
		// deletes content rather than the row; they are not data and not an
		// error.
		for len(body) > 0 && blankRow(body[len(body)-1]) {
			body = body[:len(body)-1]
		}
		if len(body) != sheet.Rows {
			problems = append(problems, fmt.Sprintf(
				"Sheet %q has %d rows; the order is for %d of %s.",
				actual, len(body), sheet.Rows, sheet.ProductName))
		}

		parsed, rowProblems := readRows(actual, body, sheet.Columns, index)
		problems = append(problems, rowProblems...)
		out[sheet.Code] = parsed
	}

	return out, problems
}

// findSheet resolves a sheet by name, ignoring case and surrounding space.
func findSheet(f *excelize.File, code string) string {
	want := strings.ToUpper(strings.TrimSpace(code))
	for _, name := range f.GetSheetList() {
		if strings.ToUpper(strings.TrimSpace(name)) == want {
			return name
		}
	}
	return ""
}

// matchHeaders maps each expected column to the position it was found at.
func matchHeaders(sheet string, header []string, columns []sheetColumn) (map[string]int, []string) {
	at := map[string]int{}
	for i, cell := range header {
		at[strings.ToUpper(strings.TrimSpace(cell))] = i
	}
	index := map[string]int{}
	var problems []string
	for _, col := range columns {
		pos, found := at[strings.ToUpper(col.Header)]
		if !found {
			problems = append(problems, fmt.Sprintf(
				"Sheet %q is missing the %q column.", sheet, col.Header))
			continue
		}
		index[col.Header] = pos
	}
	return index, problems
}

// readRows validates and reads the body of one sheet.
func readRows(
	sheet string, body [][]string, columns []sheetColumn, index map[string]int,
) ([]sheetRow, []string) {
	var problems []string
	out := make([]sheetRow, 0, len(body))

	for i, row := range body {
		// +2: one for the header, one because spreadsheets count from 1. The
		// number in the message is the row the operator sees in Excel.
		line := i + 2
		parsed := sheetRow{Values: map[string]string{}}

		for _, col := range columns {
			value := strings.TrimSpace(cellAt(row, index[col.Header]))
			if value == "" {
				if col.Required {
					problems = append(problems, fmt.Sprintf(
						"Sheet %q row %d: %s is empty.", sheet, line, col.Header))
				}
				continue
			}
			if col.Numeric {
				n, err := strconv.ParseFloat(value, 64)
				if err != nil {
					problems = append(problems, fmt.Sprintf(
						"Sheet %q row %d: %s must be a number, not %q.",
						sheet, line, col.Header, value))
					continue
				}
				if col.Header == serialHeader {
					parsed.Serial = int(n)
					continue
				}
			}
			switch col.Header {
			case serialHeader:
			case colourHeader:
				parsed.Colour = value
			default:
				parsed.Values[col.Role+"/"+col.Variable] = value
			}
		}
		out = append(out, parsed)
	}
	return out, problems
}

func cellAt(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

func blankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
