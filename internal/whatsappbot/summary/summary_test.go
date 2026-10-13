package summary

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func attrs(kv ...string) []Attribute {
	out := make([]Attribute, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out = append(out, Attribute{Key: kv[i], Value: kv[i+1]})
	}
	return out
}

func TestBuildDualNamePlank(t *testing.T) {
	items := []LineItem{{
		Title:        "Dual Name Plank",
		VariantTitle: "GOLD / NO LIGHT",
		Attributes: attrs(
			"_has_gpo", "622778",
			"STEP 3-", "2 RED HEART",
			"STEP 4-First Name-", "ARADHYA",
			"STEP 5-Second Name", "AJAYRAM ",
			"_gpo_addon_price", "50",
		),
	}}
	got := Build(items)
	want := "Dual Name Plank (GOLD / NO LIGHT): 2 RED HEART, First Name ARADHYA, Second Name AJAYRAM"
	if got.Text != want {
		t.Fatalf("Text\n got %q\nwant %q", got.Text, want)
	}
	if !got.HasPersonalisation || len(got.Warnings) != 0 {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestBuildComboDropsWhatsAppNumber(t *testing.T) {
	items := []LineItem{{
		Title:        "Soulmate COMBO",
		VariantTitle: "BLUE",
		Attributes: attrs(
			"STEP 2 - First Name-", "RAHUL",
			"STEP 3 -Second Name", "PRIYA",
			"STEP 4 - Name On 3D Rose", "PRIYA",
			"STEP 5 - Name On Heart Keychain", "RAHUL",
			"STEP 6 - WhatsApp Number", "98765 43210",
		),
	}}
	got := Build(items)
	if strings.Contains(got.Text, "98765") || strings.Contains(strings.ToLower(got.Text), "whatsapp") {
		t.Fatalf("WhatsApp number leaked into summary: %q", got.Text)
	}
	if !strings.Contains(got.Text, "Name On 3D Rose PRIYA") {
		t.Fatalf("missing rose name: %q", got.Text)
	}
	if n := WhatsAppNumber(items); n != "98765 43210" {
		t.Fatalf("WhatsAppNumber = %q", n)
	}
}

func TestBuildWarnings(t *testing.T) {
	items := []LineItem{{
		Title: "Dual Name Plank",
		Attributes: attrs(
			"First Name on Plank", "RA HUL",
			"Second Name", "JOSÉ",
			"Red Heart", "2 RED HEART",
		),
	}}
	got := Build(items)
	if len(got.Warnings) != 2 {
		t.Fatalf("want 2 warnings, got %v", got.Warnings)
	}
	if !strings.Contains(got.Text, "Please check:") {
		t.Fatalf("warnings not in text: %q", got.Text)
	}
}

func TestBuildMultipleItemsAndPlainItem(t *testing.T) {
	items := []LineItem{
		{Title: "Moon Lamp"},
		{Title: "Pet Tag", Attributes: attrs("Pet Name", "BRUNO")},
		{Title: "Keychain", VariantTitle: "RED", Attributes: attrs("Name", "ANU")},
	}
	got := Build(items)
	want := "Pet Tag: Pet Name BRUNO | Keychain (RED): Name ANU"
	if got.Text != want {
		t.Fatalf("Text\n got %q\nwant %q", got.Text, want)
	}
}

func TestBuildNoPersonalisation(t *testing.T) {
	got := Build([]LineItem{{Title: "Moon Lamp", Attributes: attrs("_has_gpo", "1")}})
	if got.HasPersonalisation || got.Text != "" {
		t.Fatalf("expected nothing to confirm, got %+v", got)
	}
}

func TestBuildSanitisesAndTruncates(t *testing.T) {
	long := strings.Repeat("A", 2000)
	got := Build([]LineItem{{Title: "Plate", Attributes: attrs("Text", "line1\nline2\t   x", "Name", long)}})
	if strings.ContainsAny(got.Text, "\n\t") || strings.Contains(got.Text, "  ") {
		t.Fatalf("text not single-line: %q", got.Text)
	}
	if utf8.RuneCountInString(got.Text) > MaxLength {
		t.Fatalf("text too long: %d", utf8.RuneCountInString(got.Text))
	}
	if !strings.HasSuffix(got.Text, "...") {
		t.Fatalf("truncated text should end with ...: %q", got.Text[len(got.Text)-10:])
	}
}
