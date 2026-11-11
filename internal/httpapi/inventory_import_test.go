package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

func TestParseFilamentSheetReadsHumanTypedRows(t *testing.T) {
	rows := [][]string{
		{" Material ", "Color", "Grams", "Reorder level"},
		{"PLA", "BLACK", "1,000 g", ""},
		{"", "", "", ""}, // blank rows are skipped
		{"PETG", "", "250", "50"},
	}
	got, problems := parseFilamentSheet(rows)
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(got) != 2 || got[0].grams != 1000 || got[0].reorder != nil || deref(got[0].colour) != "BLACK" {
		t.Fatalf("row 1 = %+v", got[0])
	}
	if got[1].colour != nil || got[1].reorder == nil || *got[1].reorder != 50 {
		t.Fatalf("row 2 = %+v", got[1])
	}
}

// The shop's own stock sheet, as it is kept: a colour and a CLOSING spool
// count, no material, plus movement columns and a usage log beside it.
func TestParseFilamentSheetReadsTheShopsStockSheet(t *testing.T) {
	rows := [][]string{
		{"Color", "Opening stock", "New", "Used", "Closing", "Taken by", "DATE", "QUANTITY", "Colours"},
		{"Red", "16", "", "4", "12", "ADITYA", "2/11/2026", "2", "white & Purple"},
		{" Royal Blue", "29", "", "5", "24"},
		{"Brown", "", "", "", "0"},
		{"", "", "", "", "0"}, // a formula row with nothing in it
		{"TOTAL", "61", "", "9", "36"},
	}
	got, problems := parseFilamentSheet(rows)
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %+v", got)
	}
	red := got[0]
	if red.material != "PLA" || deref(red.colour) != "Red" || red.grams != 12000 {
		t.Fatalf("red = %+v", red)
	}
	if red.extra["Opening stock"] != "16" || red.extra["Taken by"] != "ADITYA" || red.extra["New"] != "" {
		t.Fatalf("red extra = %v", red.extra)
	}
	if deref(got[1].colour) != "Royal Blue" || got[1].grams != 24000 || got[2].grams != 0 {
		t.Fatalf("rows = %+v", got)
	}
	cols := importColumnsUsed(sheetExtraOrder(rows, filamentImportColumns),
		[]map[string]string{got[0].extra, got[1].extra, got[2].extra})
	if strings.Join(cols, ",") != "Opening stock,Used,Taken by,DATE,QUANTITY,Colours" {
		t.Fatalf("columns = %v", cols)
	}
}

// The shop's box list: a size column with no heading, Total / Used /
// Remaining, mostly blank counts, and one name used for two sizes.
func TestParseItemSheetReadsTheShopsBoxList(t *testing.T) {
	rows := [][]string{
		{"Boxes", "", "Total", "Used", "Remaining"},
		{"DNP Box", "25x10x8.5", "3000", "450", "2550"},
		{"DNPF Box", "34x27.5x8 - Mailer box + Foam Padding design in design files"},
		{"Moon box", "18x18x18", "", "", ""},
		{"Moon box", "15x15x10", "", "", ""},
		{"Total", "", "3000", "450", "2550"},
	}
	got, problems := parseItemSheet(rows)
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(got) != 4 || got[0].quantity != 2550 || got[0].unit != "piece" || got[1].quantity != 0 {
		t.Fatalf("rows = %+v", got)
	}
	if got[0].extra["Details"] != "25x10x8.5" || got[0].extra["Total"] != "3000" {
		t.Fatalf("extra = %v", got[0].extra)
	}
	if got[2].name != "Moon box (18x18x18)" || got[3].name != "Moon box (15x15x10)" {
		t.Fatalf("moon boxes = %q, %q", got[2].name, got[3].name)
	}
}

func TestParseFilamentSheetReadsAHexColumn(t *testing.T) {
	got, problems := parseFilamentSheet([][]string{
		{"Colour", "Colour code", "Closing"},
		{"Royal Blue", "4169e1", "3"},
		{"Bbay Blue", "not-a-colour", "2"},
	})
	if len(got) != 1 || got[0].hex != "#4169E1" {
		t.Fatalf("rows = %+v", got)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "Row 3:") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestParseFilamentSheetListsEveryProblemWithItsRow(t *testing.T) {
	rows := [][]string{
		{"Material", "Colour", "Grams available"},
		{"PLA", "BLACK", "lots"},
		{"", "", "10"},
		{"PLA", "white", "10"},
		{"pla", "WHITE", "20"},
	}
	_, problems := parseFilamentSheet(rows)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"Row 2:", "Row 3:", "Row 5: pla WHITE is already on row 4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestParseSheetRequiresTheHeadings(t *testing.T) {
	_, problems := parseItemSheet([][]string{{"Qty", "Unit"}, {"3", "box"}})
	if len(problems) == 0 || !strings.Contains(problems[0], `"Name"`) {
		t.Fatalf("problems = %v", problems)
	}
	_, problems = parseFilamentSheet([][]string{{"Colour", "Notes"}, {"Red", "x"}})
	if len(problems) != 1 || !strings.Contains(problems[0], "stock column") {
		t.Fatalf("problems = %v", problems)
	}
	if _, problems = parseItemSheet(nil); len(problems) != 1 {
		t.Fatalf("empty sheet problems = %v", problems)
	}
}

func TestParseItemSheetValidatesUnitsPricesAndCodes(t *testing.T) {
	rows := [][]string{
		{"Name", "Quantity", "Unit", "Unit price", "Code"},
		{"Gift box", "200", "Piece", "₹12.50", "gb-01"},
		{"Tape", "3", "rolls", "", ""},
		{"Card", "10", "piece", "-1", ""},
	}
	got, problems := parseItemSheet(rows)
	if len(got) != 1 || got[0].unit != "piece" || *got[0].unitPrice != 12.5 || *got[0].code != "GB-01" {
		t.Fatalf("parsed = %+v", got)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `Row 3: "rolls" is not a unit`) || !strings.Contains(joined, "Row 4: Unit price") {
		t.Fatalf("problems:\n%s", joined)
	}
}

func TestPickSheetPrefersTheShelfsOwnSheet(t *testing.T) {
	names := []string{"Filament", "Boxes"}
	if pickSheet(names, itemSheetHints) != "Boxes" || pickSheet(names, filamentSheetHints) != "Filament" {
		t.Fatal("picked the wrong sheet")
	}
	if pickSheet([]string{"Sheet1"}, itemSheetHints) != "Sheet1" {
		t.Fatal("did not fall back to the first sheet")
	}
}

// The template somebody downloads must be a file the importer accepts as is.
func TestImportTemplatesRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, tc := range map[string]struct {
		send  func(*Server, *gin.Context)
		parse func([][]string) int
	}{
		"filament": {(*Server).filamentImportTemplate, func(r [][]string) int { out, p := parseFilamentSheet(r); return len(out) - len(p)*100 }},
		"items":    {(*Server).inventoryItemsImportTemplate, func(r [][]string) int { out, p := parseItemSheet(r); return len(out) - len(p)*100 }},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		tc.send(&Server{}, c)
		f, err := excelize.OpenReader(w.Body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rows, _ := f.GetRows(f.GetSheetName(0))
		if got := tc.parse(rows); got != 1 {
			t.Errorf("%s template: want 1 clean row, got score %d (rows %v)", name, got, rows)
		}
	}
}
