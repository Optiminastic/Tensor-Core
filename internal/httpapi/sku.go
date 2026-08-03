package httpapi

import (
	"fmt"
	"strings"
)

// SKU segment lengths: a short brand prefix, the material, and a colour code,
// kept compact so the generated SKU stays scannable (e.g. GIF-PLA-WHT-0007).
const (
	skuBrandLen    = 3
	skuMaterialLen = 4
	skuColourLen   = 3
)

// generateSKU builds a design's permanent, system-assigned SKU from its brand,
// material and colour plus a globally unique sequence number. The format mirrors
// the one-time SQL backfill in migration 0018:
//
//	{BRAND}-{MATERIAL}-{COLOUR|NA}-{NNNN}
//
// It is deterministic and never re-derived: the SKU is stamped once at creation
// and stays fixed even if the design's attributes later change.
func generateSKU(brandSlug, material string, colour *string, seq int64) string {
	col := "NA"
	if colour != nil {
		if c := skuSegment(*colour, skuColourLen); c != "X" {
			col = c
		}
	}
	return fmt.Sprintf("%s-%s-%s-%04d",
		skuSegment(brandSlug, skuBrandLen), skuSegment(material, skuMaterialLen), col, seq)
}

// skuSegment upper-cases s, keeps only [A-Z0-9], and returns the first n of those
// characters. Empty input yields "X" so a segment is never blank.
func skuSegment(s string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == n {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "X"
	}
	return b.String()
}
