package personalise

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestPlankParamsReadsGloboStepLabels(t *testing.T) {
	item := production.LineItem{
		ProductName: "Dual Name Plank - RED / I NEED LIGHT",
		Options: []production.Option{
			{Name: "STEP 3-", Value: "2 RED HEART"},
			{Name: "STEP 4-First Name-", Value: "kajal"},
			{Name: "STEP 5-Second Name", Value: "anand"},
			{Name: "STEP 6 - WhatsApp Number", Value: "8218740630"},
		},
	}

	params := PlankParams(item, "/assets/RED HEART.stl")

	if params["name_one"] != `"KAJAL"` {
		t.Errorf("name_one = %s, want \"KAJAL\" (planks are printed upper case)", params["name_one"])
	}
	if params["name_two"] != `"ANAND"` {
		t.Errorf("name_two = %s, want \"ANAND\"", params["name_two"])
	}
	if params["hearts"] != "2" {
		t.Errorf("hearts = %s, want 2", params["hearts"])
	}
	if params["heart_stl"] != `"/assets/RED HEART.stl"` {
		t.Errorf("heart_stl = %s, want the asset path", params["heart_stl"])
	}
	// The phone number is not geometry and must not become one.
	if len(params) != 4 {
		t.Errorf("params = %v, want exactly the four the template needs", params)
	}
}

func TestPlankParamsReadsAnotherStoresLabels(t *testing.T) {
	item := production.LineItem{
		ProductName: "DUAL NAME & PHOTO FRAME - GOLD",
		Options: []production.Option{
			{Name: "LEFT NAME", Value: "RAESMA"},
			{Name: "RIGHT NAME", Value: "STUVERT"},
		},
	}

	params := PlankParams(item, "")

	if params["name_one"] != `"RAESMA"` || params["name_two"] != `"STUVERT"` {
		t.Errorf("params = %v, want both names read from LEFT/RIGHT labels", params)
	}
	if params["hearts"] != "0" {
		t.Errorf("hearts = %s, want 0 - this product has none", params["hearts"])
	}
}

func TestPlankParamsFallsBackToTheTypedField(t *testing.T) {
	joined := "BHAVANI / SRIGIRISH"
	item := production.LineItem{
		ProductName:         "Dual Name Plank",
		PersonalisationName: &joined,
		Options:             []production.Option{{Name: "Some Label The Shop Invented", Value: "x"}},
	}

	params := PlankParams(item, "")

	if params["name_one"] != `"BHAVANI"` || params["name_two"] != `"SRIGIRISH"` {
		t.Errorf("params = %v, want the joined typed field split back apart", params)
	}
}

func TestCountPrefix(t *testing.T) {
	cases := map[string]int{
		"2 RED HEART": 2, "1 RED HEART": 1, "RED HEART": 1,
		"": 0, "0 HEARTS": 0, "10 HEARTS": 10,
	}
	for value, want := range cases {
		if got := countPrefix(value); got != want {
			t.Errorf("countPrefix(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestTemplateForProduct(t *testing.T) {
	cases := map[string]string{
		"Dual Name Plank - BLUE / NO LIGHT": "dual_name_plank",
		"Doctor - Desk Name Plank":          "dual_name_plank",
		"MOON LAMP 6 INCH":                  "",
		"":                                  "",
	}
	for product, want := range cases {
		if got := TemplateForProduct(product); got != want {
			t.Errorf("TemplateForProduct(%q) = %q, want %q", product, got, want)
		}
	}
}

func TestQuoteEscapes(t *testing.T) {
	if got := Quote(`say "hi"\`); got != `"say \"hi\"\\"` {
		t.Errorf("Quote = %s", got)
	}
}

func TestCSSColour(t *testing.T) {
	cases := map[string]string{
		"BABY PINK":            "#f4c2c2",
		"baby pink":            "#f4c2c2",
		"BABY PINK / NO LIGHT": "#f4c2c2",
		"MIDNIGHT TEAL":        "", // unknown: a grey preview, never a wrong print
		"":                     "",
	}
	for name, want := range cases {
		if got := CSSColour(name); got != want {
			t.Errorf("CSSColour(%q) = %q, want %q", name, got, want)
		}
	}
}
