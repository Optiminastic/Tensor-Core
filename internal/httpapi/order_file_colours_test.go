package httpapi

import (
	"sort"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// Couple's Date Plank declares left_name_color, right_name_color,
// couple_date_color and heart_color; the mapping fills the first three.
func TestOrderColourArgsFindsTheColoursTheOrderFills(t *testing.T) {
	declared := map[string]string{"left_name": "red", "right_name": "blue", "couple_date": "black", "heart": "#C62828"}
	args := map[string]string{
		"name_1": `"TUSHAR"`, "left_name_color": `"YELLOW"`,
		"right_name_color": `"BLUE"`, "COUPLE_DATE_COLOR": `"RED"`,
	}
	got := orderColourArgs(declared, args)
	sort.Strings(got)
	if strings.Join(got, ",") != "COUPLE_DATE_COLOR,left_name_color,right_name_color" {
		t.Fatalf("got %v", got)
	}
	// A file with fixed colours the order never touches (Family Heart).
	if got := orderColourArgs(map[string]string{"top": "#C12E1E"}, map[string]string{"name1": `"JAYA"`}); len(got) != 0 {
		t.Fatalf("fixed file got %v", got)
	}
}

func TestUnquoteSCADReadsBackQuote(t *testing.T) {
	for _, v := range []string{"YELLOW", `say "hi"`, `back\slash`} {
		if got := unquoteSCAD(personalise.Quote(v)); got != v {
			t.Errorf("round trip %q -> %q", v, got)
		}
	}
}
