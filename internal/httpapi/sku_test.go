package httpapi

import "testing"

func TestGenerateSKU(t *testing.T) {
	cases := []struct {
		name     string
		brand    string
		material string
		colour   *string
		seq      int64
		want     string
	}{
		{"gifting pla white", "gifting", "PLA", ptr("White"), 7, "GIF-PLA-WHI-0007"},
		{"decor petg black", "decor", "PETG", ptr("Black"), 123, "DEC-PETG-BLA-0123"},
		{"no colour", "gifting", "ABS", nil, 42, "GIF-ABS-NA-0042"},
		{"blank colour falls back to NA", "gifting", "PLA", ptr("  "), 1, "GIF-PLA-NA-0001"},
		{"strips punctuation and spaces", "home-decor", "PLA", ptr("Sky Blue"), 5, "HOM-PLA-SKY-0005"},
		{"large sequence keeps growing", "gifting", "PLA", ptr("Red"), 12345, "GIF-PLA-RED-12345"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := generateSKU(c.brand, c.material, c.colour, c.seq); got != c.want {
				t.Errorf("generateSKU(%q,%q,%v,%d) = %q, want %q",
					c.brand, c.material, c.colour, c.seq, got, c.want)
			}
		})
	}
}
