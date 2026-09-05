package httpapi

// Turning the colour a customer chose into something a 3MF can carry.
//
// The storefront sells a colour by name - "SKY BLUE", "BABY PINK" - and a 3MF
// wants "#87CEEB". Nothing in the order carries a hex, so it has to be looked
// up, and getting it wrong prints the wrong product: colour is the one
// customisation on a plank that cannot be corrected after the fact.

import (
	"context"
	"errors"
	"strings"
)

// BasePlateColour is the plank's body, white on every Dual Name Plank
// regardless of what the customer chose. Only the lettering takes their colour.
const BasePlateColour = "#FFFFFF"

// fallbackColours covers names the filament shelf does not.
//
// The shelf is the better source - it is what will physically be loaded, and
// the sync keeps it current - but it only holds what is in stock right now,
// and customers order colours that are momentarily out. Rather than fail a
// render for a colour the shop plainly sells, these fill the gaps.
//
// Deliberately small: every entry is a colour seen in real orders and absent
// from the shelf. It is a stopgap for stock going in and out, not a second
// colour system to maintain - anything unrecognised still fails loudly.
var fallbackColours = map[string]string{
	"white":     "#FFFFFF",
	"red":       "#E4002B",
	"baby pink": "#F4C2C2",
	"pink":      "#FF69B4",
	"silver":    "#C0C0C0",
	"grey":      "#808080",
	"gray":      "#808080",
	"brown":     "#8B4513",
}

// errUnknownColour means the colour cannot be resolved to a swatch. The job is
// held rather than printed in a guess.
var errUnknownColour = errors.New("unknown colour")

// resolveColourHex maps a colour name to "#RRGGBB".
//
// The filament shelf wins, because it is the colour that will actually be
// loaded into the printer. The built-in table is consulted only when the shelf
// has nothing, and an unrecognised name is an error rather than a default -
// silently printing black lettering on a plank somebody ordered in pink is the
// failure this exists to prevent.
func (s *Server) resolveColourHex(ctx context.Context, colour string) (string, error) {
	name := strings.TrimSpace(colour)
	if name == "" {
		return "", errUnknownColour
	}

	if hex, err := s.store.Q.GetColourHexByName(ctx, name); err == nil && hex != nil {
		if normalised, ok := normaliseHex(*hex); ok {
			return normalised, nil
		}
	} else if err != nil && !isNoRows(err) {
		return "", err
	}

	if hex, ok := fallbackColours[strings.ToLower(name)]; ok {
		return hex, nil
	}
	return "", errUnknownColour
}

// normaliseHex accepts the forms BambuBuddy stores and returns "#RRGGBB".
//
// It stores "#1A1A1A", but also sometimes an 8-digit value with alpha, and
// occasionally without the leading hash. Rejecting those outright would fail
// renders for colours the shop genuinely has.
func normaliseHex(raw string) (string, bool) {
	h := strings.TrimSpace(raw)
	h = strings.TrimPrefix(h, "#")
	// Alpha is dropped: a printer has no use for it, and 3MF takes opacity
	// separately.
	if len(h) == 8 {
		h = h[:6]
	}
	if len(h) != 6 {
		return "", false
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return "", false
		}
	}
	return "#" + strings.ToUpper(h), true
}
