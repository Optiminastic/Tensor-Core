package production

import "strings"

// colourAliases are the storefront colour names that mean one filament.
//
// The shop sells "SKY BLUE" as its own option, but there is one blue spool on
// the shelf and both names are printed from it. Left unaliased they are two
// colours everywhere it matters: the planner beds them apart, so a lone sky
// blue job waits for three more of a colour that will never arrive; filament is
// reserved against a bucket that does not exist; and the model is painted a
// swatch no spool matches.
//
// Written in the canonical form CanonicalColourName returns, so an entry reads
// as "this name IS that colour".
//
// Deliberately small, for the same reason fallbackColours is: an alias is a
// claim about what will physically be loaded, and a wrong one prints the wrong
// product. Two names belong here only when the shop confirms one spool serves
// both.
var colourAliases = map[string]string{
	"SKY BLUE": "BLUE",
}

// CanonicalColourName is the one spelling of a colour: trimmed, inner runs of
// whitespace collapsed to single spaces, uppercased, then resolved through
// colourAliases.
//
// Exported because every path that compares colours has to reach the same
// answer - grouping jobs onto a bed, reserving the filament for them, and
// resolving the swatch the model is painted with. Two of those disagreeing is
// how a plate gets batched as BLUE while its stock is debited from SKY BLUE.
func CanonicalColourName(raw string) string {
	name := strings.ToUpper(strings.Join(strings.Fields(raw), " "))
	if canonical, ok := colourAliases[name]; ok {
		return canonical
	}
	return name
}
