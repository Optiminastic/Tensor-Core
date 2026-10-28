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

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("write workbook: %w", err)
	}
	return buf.Bytes(), nil
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
