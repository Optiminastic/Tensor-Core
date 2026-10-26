package production

import "strings"

// Which product family a job belongs to, for deciding what may share a bed.
//
// A plate is one filament load, which is why colour is the rule - but it is
// also one GEOMETRY, laid out once and sliced once. Two products that happen
// to be red are still two different things to pack, and the shop's rule is
// plain: a bed carrying planks carries only planks.
//
// NAMED SETS, NOT PREFIXES. The same discipline IsGeneratedProduct uses, and
// for the same scar: a rule that accepted anything starting with "DNP"
// swallowed DNPF - the Dual Name & PHOTO FRAME - and held four orders behind
// an error about a product they were not. The families are known and short,
// so listing them is both safer and more honest than a prefix that fits today.

// familyBySegment maps a hyphen-separated SKU segment to the family that may
// share a bed.
//
// THE WHOLE PLANK RANGE IS ONE FAMILY, deliberately. DNP-GLD and DNPWL-GLD are
// one gold plank differing by whether an LED ships in the box; splitting them
// sent two beds under the three-unit floor and both stalled. PDNP is the same
// plank in a premium finish and DNPLB the older with-light code. Separating any
// of them would re-create a failure this codebase has already had once.
var familyBySegment = map[string]string{
	"DNP":   "DNP",
	"DNPWL": "DNP",
	"DNPLB": "DNP",
	"PDNP":  "DNP",

	// A photo frame, not a plank: different finished size and its own bed.
	"DNPF": "DNPF",

	// The Soulmate combos, which print three things per order line.
	"SC":   "SC",
	"SCWL": "SC",

	// The Diwali products. Each is its own geometry and its own bed.
	"PDHR": "PDHR",
	"PWDH": "PWDH",
}

// familyByName is the fallback for a line carrying no usable SKU.
//
// Nine live plank variants carry no SKU at all - the catalogue lost it - so a
// SKU-only rule would file them as "unknown" and let them share a bed with
// anything else unknown. Matched as a substring of the lower-cased name,
// longest pattern first, because the storefront reorders words around a
// product's own name freely.
var familyByName = []struct{ pattern, family string }{
	// Longest first: "personalized wall diya holder" must win before any
	// shorter pattern could claim it.
	{"personalized wall diya holder", "PWDH"},
	{"personalized diya holder", "PDHR"},
	{"dual name & photo frame", "DNPF"},
	{"dual name plank", "DNP"},
	{"soulmate combo", "SC"},
}

// ProductFamily is the family key a job batches within, or "" when nothing
// names it.
//
// EMPTY IS NOT AN ERROR, and it does not force its own bucket per product: a
// job Tensor cannot place in a family keeps today's behaviour exactly, sharing
// a bed with anything else of its colour. Only the families named above are
// separated, so this can never split work that currently batches together -
// it can only stop a named family mixing with something that is not it.
func ProductFamily(sku, productName string) string {
	for _, segment := range strings.Split(strings.ToUpper(strings.TrimSpace(sku)), "-") {
		if family, ok := familyBySegment[segment]; ok {
			return family
		}
	}
	name := strings.ToLower(strings.TrimSpace(productName))
	if name == "" {
		return ""
	}
	for _, candidate := range familyByName {
		if strings.Contains(name, candidate.pattern) {
			return candidate.family
		}
	}
	return ""
}
