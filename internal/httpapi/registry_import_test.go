package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

func variants(skus ...string) []shopify.VariantSummary {
	out := make([]shopify.VariantSummary, 0, len(skus))
	for _, sku := range skus {
		out = append(out, shopify.VariantSummary{SKU: sku, Title: sku})
	}
	return out
}

func TestProductCodeIsTheCommonestSKUFamily(t *testing.T) {
	for _, c := range []struct {
		name string
		p    shopify.ProductSummary
		want string
	}{
		{
			// The real case: every variant of a product shares its family.
			name: "one family",
			p:    shopify.ProductSummary{Variants: variants("DNPWL-BLU", "DNPWL-RED", "DNPWL-GLD")},
			want: "DNPWL",
		},
		{
			// Only the FIRST segment counts. Counting every segment would let
			// a colour shared by three variants outvote the family they are
			// all in - here BLU appears twice and must not win.
			name: "colour repeats across families",
			p:    shopify.ProductSummary{Variants: variants("SNP-BLU", "SNPWL-BLU", "SNP-RED")},
			want: "SNP",
		},
		{
			name: "case is normalised",
			p:    shopify.ProductSummary{Variants: variants("dnpf-gld", "DNPF-RED")},
			want: "DNPF",
		},
		{
			// A variant with no SKU contributes nothing, but must not stop the
			// others deciding.
			name: "blank skus ignored",
			p:    shopify.ProductSummary{Variants: variants("", "PDNP-PUR", "")},
			want: "PDNP",
		},
		{
			// No hyphen at all: the whole SKU is the family.
			name: "unsegmented sku",
			p:    shopify.ProductSummary{Variants: variants("LK", "LK")},
			want: "LK",
		},
		{
			// Nothing to match an order by. The caller refuses the import and
			// says so rather than inventing a code for a product that could
			// never render.
			name: "no skus at all",
			p:    shopify.ProductSummary{Handle: "", Variants: variants("", "")},
			want: "",
		},
		{
			// Falls back to the handle when there is no family to read.
			name: "handle fallback",
			p:    shopify.ProductSummary{Handle: "premium-dual-name-plank"},
			want: "PREMIUM-DUAL-NAME-PLANK",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := productCodeFor(c.p); got != c.want {
				t.Errorf("code = %q, want %q", got, c.want)
			}
		})
	}
}

func TestProductCodeTieBreaksTheSameWayTwice(t *testing.T) {
	// A map iterates in a different order every run, so a tie decided by
	// "whichever came first" would import the same product under two
	// different codes on two different days.
	p := shopify.ProductSummary{Variants: variants("AAA-1", "BBB-1")}
	first := productCodeFor(p)
	for i := 0; i < 50; i++ {
		if got := productCodeFor(p); got != first {
			t.Fatalf("code = %q then %q; a tie must resolve the same way twice", first, got)
		}
	}
}

func TestVariantNameFallsBackToTheSKU(t *testing.T) {
	// "Default Title" is Shopify's word for a product with no options. It
	// means nothing in a list whose job is to show which SKUs are covered.
	if got := variantName(shopify.VariantSummary{Title: "Default Title", SKU: "LK"}); got != "LK" {
		t.Errorf("name = %q, want the SKU", got)
	}
	if got := variantName(shopify.VariantSummary{Title: "", SKU: "LK"}); got != "LK" {
		t.Errorf("name = %q, want the SKU", got)
	}
	if got := variantName(shopify.VariantSummary{Title: "BLUE / NO LIGHT", SKU: "DNP-BLU"}); got != "BLUE / NO LIGHT" {
		t.Errorf("name = %q, want Shopify's own title", got)
	}
}
