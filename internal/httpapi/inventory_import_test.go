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

func TestParseFilamentSheetListsEveryProblemWithItsRow(t *testing.T) {
	rows := [][]string{
		{"Material", "Colour", "Grams available"},
		{"PLA", "BLACK", "lots"},
		{"", "WHITE", "10"},
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
	_, problems := parseItemSheet([][]string{{"Name", "Qty"}})
	if len(problems) != 1 || !strings.Contains(problems[0], `"unit"`) {
		t.Fatalf("problems = %v", problems)
	}
	_, problems = parseItemSheet(nil)
	if len(problems) != 1 {
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
