package accounts

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func openWritten(t *testing.T, rows []Row, loc *time.Location) *excelize.File {
	t.Helper()
	raw, err := WriteXLSX(rows, loc)
	if err != nil {
		t.Fatalf("WriteXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reopen workbook: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestHeadersMatchTheTemplateExactly(t *testing.T) {
	// THE SHEET IS IMPORTED BY SOMEBODY ELSE'S TOOL. "voucher no." is
	// lower-case and "naration" is misspelled in the template the accountant
	// gave us; correcting either is a failed import, not a tidy-up.
	f := openWritten(t, nil, ist(t))
	got, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no header row written")
	}
	want := []string{
		"Date", "voucher no.", "Party Name", "State Name", "Item", "Qty",
		"Rate", "Taxable Value", "GST %", "CGST", "SGST", "IGST",
		"total value", "naration",
	}
	if len(got[0]) != len(want) {
		t.Fatalf("got %d columns, want %d", len(got[0]), len(want))
	}
	for i := range want {
		if got[0][i] != want[i] {
			t.Errorf("column %d = %q, want %q", i+1, got[0][i], want[i])
		}
	}
}

func TestMoneyIsWrittenAsNumbersNotText(t *testing.T) {
	// A money column stored as text looks identical on screen and will not
	// sum, which is the first thing anybody does to a sales register.
	loc := ist(t)
	rows := Build([]Order{{
		PlacedAt: time.Date(2026, 10, 27, 15, 0, 0, 0, loc),
		Number:   "T3DPS-1", Customer: "A", State: "Gujarat",
		Lines: []Line{{Title: "Plank", Quantity: 1, Gross: 898}},
	}}, Options{})

	f := openWritten(t, rows, loc)
	// H2 is Taxable Value on the first data row.
	kind, err := f.GetCellType(SheetName, "H2")
	if err != nil {
		t.Fatalf("cell type: %v", err)
	}
	if kind == excelize.CellTypeSharedString || kind == excelize.CellTypeInlineString {
		t.Errorf("Taxable Value was written as text (%v); it would not sum", kind)
	}
	value, err := f.GetCellValue(SheetName, "H2")
	if err != nil {
		t.Fatalf("cell value: %v", err)
	}
	if value == "" {
		t.Error("Taxable Value is empty")
	}
}

func TestOneRowPerLineItemLandsOnTheSheet(t *testing.T) {
	loc := ist(t)
	rows := Build([]Order{{
		PlacedAt: time.Date(2026, 10, 27, 15, 0, 0, 0, loc),
		Number:   "T3DPS-9", Customer: "Two Items", State: "Maharashtra",
		Lines: []Line{
			{Title: "One", Quantity: 1, Gross: 100},
			{Title: "Two", Quantity: 3, Gross: 300},
		},
	}}, Options{})

	f := openWritten(t, rows, loc)
	got, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(got) != 4 { // header + two line items + the totals row
		t.Fatalf("sheet has %d rows, want header, two items and a total", len(got))
	}
	if got[1][4] != "One" || got[2][4] != "Two" {
		t.Errorf("item column wrong: %q %q", got[1][4], got[2][4])
	}
}

func TestAQuietDayStillProducesAValidSheet(t *testing.T) {
	// A day with no orders is a real answer and must still send: a missing
	// attachment reads as a broken report, and silence is what let the
	// personalisation sweep fail unnoticed for hours.
	raw, err := WriteXLSX(nil, ist(t))
	if err != nil {
		t.Fatalf("WriteXLSX with no rows: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("no bytes written")
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("an empty report is not a valid workbook: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, _ := f.GetRows(SheetName)
	if len(got) != 1 {
		t.Errorf("empty report has %d rows, want the header alone", len(got))
	}
}

func TestFilenameIsDatedByTheWindowItCovers(t *testing.T) {
	loc := ist(t)
	// Two reports must never collide in an inbox, and the date shown has to
	// be the day's, in the shop's zone - not UTC, which is still the previous
	// day at noon IST.
	got := Filename(time.Date(2026, 10, 28, 12, 0, 0, 0, loc), loc)
	if got != "sales-register-2026-10-28.xlsx" {
		t.Errorf("filename = %q", got)
	}
}

func TestTheSheetEndsWithATotalsRow(t *testing.T) {
	loc := ist(t)
	rows := Build([]Order{
		{
			PlacedAt: time.Date(2026, 10, 27, 15, 0, 0, 0, loc),
			Number:   "T3DPS-1", Customer: "A", State: "Gujarat",
			Lines: []Line{{Title: "One", Quantity: 1, Gross: 898}},
		},
		{
			PlacedAt: time.Date(2026, 10, 27, 16, 0, 0, 0, loc),
			Number:   "T3DPS-2", Customer: "B", State: "Maharashtra",
			Lines: []Line{{Title: "Two", Quantity: 3, Gross: 1098}},
		},
	}, Options{})

	f := openWritten(t, rows, loc)
	got, err := f.GetRows(SheetName)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	// header + two line items + totals
	if len(got) != 4 {
		t.Fatalf("sheet has %d rows, want header, two items and a total", len(got))
	}
	last := got[len(got)-1]
	if last[0] != "TOTAL" {
		t.Errorf("the last row is not labelled TOTAL: %q", last[0])
	}

	// Qty is a count and must add up.
	if last[5] != "4" {
		t.Errorf("qty total = %q, want 4", last[5])
	}
	// And the money columns must equal the rows above them, or the accountant
	// has two different answers on one sheet.
	var taxable, total float64
	for _, r := range rows {
		taxable += r.Taxable
		total += r.Total
	}
	if !nearStr(last[7], taxable) {
		t.Errorf("taxable total = %q, want %.2f", last[7], taxable)
	}
	if !nearStr(last[12], total) {
		t.Errorf("value total = %q, want %.2f", last[12], total)
	}
}

func TestRateAndGSTPercentAreNotSummed(t *testing.T) {
	// Both are PER-UNIT rates. A column of 18s adding to 990 looks like a
	// figure somebody should reconcile, and a sum of per-unit rates means
	// nothing at all - blank is the honest cell.
	loc := ist(t)
	rows := Build([]Order{{
		PlacedAt: time.Now().In(loc), Number: "T3DPS-1", Customer: "A", State: "Gujarat",
		Lines: []Line{
			{Title: "One", Quantity: 1, Gross: 898},
			{Title: "Two", Quantity: 1, Gross: 649},
		},
	}}, Options{})

	f := openWritten(t, rows, loc)
	got, _ := f.GetRows(SheetName)
	last := got[len(got)-1]
	if len(last) > 6 && last[6] != "" {
		t.Errorf("Rate was summed: %q", last[6])
	}
	if len(last) > 8 && last[8] != "" {
		t.Errorf("GST %% was summed: %q", last[8])
	}
}

func TestAQuietDayHasNoTotalsRow(t *testing.T) {
	// A row of zeroes under no rows at all reads as a broken report rather
	// than an empty one.
	f := openWritten(t, nil, ist(t))
	got, _ := f.GetRows(SheetName)
	if len(got) != 1 {
		t.Errorf("an empty report has %d rows, want the header alone", len(got))
	}
}

func nearStr(got string, want float64) bool {
	var v float64
	if _, err := fmt.Sscanf(got, "%f", &v); err != nil {
		return false
	}
	d := v - want
	return d < 0.015 && d > -0.015
}
