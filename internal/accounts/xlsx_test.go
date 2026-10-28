package accounts

import (
	"bytes"
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
	if len(got) != 3 { // header + two line items
		t.Fatalf("sheet has %d rows, want header plus two", len(got))
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
