package accounts

// Writing the register as the .xlsx the accountant already works from.
//
// THE TEMPLATE IS THE CONTRACT. Sales Format.xlsx is a single header row and
// nothing else - fourteen columns, in this order, spelled exactly as they are
// spelled there, lower-case "voucher no." and misspelled "naration" included.
// Those are not typos to tidy: the sheet is imported by whatever the
// accountant uses, and a renamed column is a failed import.

import (
	"bytes"
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"
)

// SheetName is what the template calls its only sheet.
const SheetName = "Sheet1"

// headers are the template's own, verbatim.
var headers = []string{
	"Date", "voucher no.", "Party Name", "State Name", "Item", "Qty", "Rate",
	"Taxable Value", "GST %", "CGST", "SGST", "IGST", "total value", "naration",
}

// columnWidths keeps the sheet readable without anybody dragging a column.
// Indexed with the headers above.
var columnWidths = []float64{12, 16, 24, 16, 38, 6, 12, 14, 8, 11, 11, 11, 13, 20}

// WriteXLSX renders the rows into a workbook and returns the file's bytes.
//
// BYTES, NOT A PATH. The only caller attaches this to an email; writing it to
// disk first would mean a temp file to clean up on a worker that may be killed
// mid-send, and nothing else ever wants it on disk.
func WriteXLSX(rows []Row, loc *time.Location) ([]byte, error) {
	if loc == nil {
		loc = time.UTC
	}
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	index, err := f.NewSheet(SheetName)
	if err != nil {
		return nil, fmt.Errorf("create sheet: %w", err)
	}
	f.SetActiveSheet(index)

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#EFEFEF"}},
		Border: []excelize.Border{
			{Type: "bottom", Color: "#BFBFBF", Style: 1},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("header style: %w", err)
	}

	// 2 = 0.00. The money columns are written as NUMBERS with a format, never
	// as pre-formatted strings: a string looks identical on screen and will
	// not sum, which is the first thing anybody does to a sales register.
	moneyStyle, err := f.NewStyle(&excelize.Style{NumFmt: 2})
	if err != nil {
		return nil, fmt.Errorf("money style: %w", err)
	}
	// 14 = mm-dd-yy in the default locale; a custom format is used instead so
	// an Indian accountant reads the day first.
	dateStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: strptr("dd-mm-yyyy")})
	if err != nil {
		return nil, fmt.Errorf("date style: %w", err)
	}
	totalStyle, err := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		NumFmt: 2,
		Border: []excelize.Border{{Type: "top", Color: "#7F7F7F", Style: 2}},
	})
	if err != nil {
		return nil, fmt.Errorf("total style: %w", err)
	}
	totalLabelStyle, err := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		Border: []excelize.Border{{Type: "top", Color: "#7F7F7F", Style: 2}},
	})
	if err != nil {
		return nil, fmt.Errorf("total label style: %w", err)
	}

	for i, h := range headers {
		cell, cellErr := excelize.CoordinatesToCellName(i+1, 1)
		if cellErr != nil {
			return nil, cellErr
		}
		if err := f.SetCellValue(SheetName, cell, h); err != nil {
			return nil, err
		}
		col, _ := excelize.ColumnNumberToName(i + 1)
		if i < len(columnWidths) {
			_ = f.SetColWidth(SheetName, col, col, columnWidths[i])
		}
	}
	_ = f.SetCellStyle(SheetName, "A1", "N1", headerStyle)
	// The header stays put while somebody scrolls a few hundred rows.
	_ = f.SetPanes(SheetName, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	})

	for n, r := range rows {
		row := n + 2 // 1 is the header

		// Dates as real dates, in the shop's zone. A string here would sort
		// alphabetically, putting the 10th before the 2nd.
		if err := f.SetCellValue(SheetName, cellAt(1, row), r.Date.In(loc)); err != nil {
			return nil, err
		}
		values := []any{
			r.Voucher, r.Party, r.State, r.Item, r.Qty,
			r.Rate, r.Taxable, r.GSTPercent, r.CGST, r.SGST, r.IGST, r.Total,
			r.Narration,
		}
		for i, v := range values {
			if err := f.SetCellValue(SheetName, cellAt(i+2, row), v); err != nil {
				return nil, err
			}
		}
		_ = f.SetCellStyle(SheetName, cellAt(1, row), cellAt(1, row), dateStyle)
		// G..M: Rate, Taxable, GST %, CGST, SGST, IGST, total value.
		_ = f.SetCellStyle(SheetName, cellAt(7, row), cellAt(13, row), moneyStyle)
	}

	if err := writeTotals(f, rows, totalStyle, totalLabelStyle); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("write workbook: %w", err)
	}
	return buf.Bytes(), nil
}

// writeTotals closes the sheet with a bold summed row.
//
// NOT EVERY COLUMN IS SUMMED, and the two gaps are deliberate. Rate is a
// PER-UNIT figure, so adding rates together produces a number that means
// nothing; GST % is a rate too, and a column of 18s summing to 990 would look
// like a figure somebody should reconcile. Both are left blank rather than
// filled with something arithmetically valid and semantically wrong.
//
// STATIC VALUES, NOT FORMULAS. The sheet is imported by whatever the
// accountant uses, and a reader that does not evaluate formulas would see an
// empty total - which is worse than no total row at all. These are computed
// from the same rows the sheet was built from, so they cannot disagree with
// what is above them.
func writeTotals(f *excelize.File, rows []Row, numberStyle, labelStyle int) error {
	if len(rows) == 0 {
		// A quiet day gets no totals line: a row of zeroes under no rows at
		// all reads as a broken report rather than an empty one.
		return nil
	}
	row := len(rows) + 2

	var qty int
	var taxable, cgst, sgst, igst, total float64
	for _, r := range rows {
		qty += r.Qty
		taxable += r.Taxable
		cgst += r.CGST
		sgst += r.SGST
		igst += r.IGST
		total += r.Total
	}

	if err := f.SetCellValue(SheetName, cellAt(1, row), "TOTAL"); err != nil {
		return err
	}
	if err := f.SetCellValue(SheetName, cellAt(6, row), qty); err != nil {
		return err
	}
	for col, value := range map[int]float64{
		8: round2(taxable), 10: round2(cgst), 11: round2(sgst),
		12: round2(igst), 13: round2(total),
	} {
		if err := f.SetCellValue(SheetName, cellAt(col, row), value); err != nil {
			return err
		}
	}

	// The label and the empty cells carry the rule above them so the row
	// reads as one band rather than scattered bold numbers.
	_ = f.SetCellStyle(SheetName, cellAt(1, row), cellAt(6, row), labelStyle)
	_ = f.SetCellStyle(SheetName, cellAt(7, row), cellAt(13, row), numberStyle)
	_ = f.SetCellStyle(SheetName, cellAt(14, row), cellAt(14, row), labelStyle)
	return nil
}

// Filename is what the attachment is called, dated by the day the window
// closed so two reports never collide in an inbox.
func Filename(windowEnd time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return fmt.Sprintf("sales-register-%s.xlsx", windowEnd.In(loc).Format("2006-01-02"))
}

func cellAt(col, row int) string {
	name, err := excelize.CoordinatesToCellName(col, row)
	if err != nil {
		// Unreachable for the fixed grid above; a panic here would take down
		// a worker over a spreadsheet cell.
		return "A1"
	}
	return name
}

func strptr(s string) *string { return &s }
