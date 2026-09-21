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

	"github.com/Optiminastic/tensor-core/internal/production"
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
// The hexes below the first group are the values the filament sync itself
// recorded from the AMS trays on an instance where BambuBuddy was reachable -
// the same numbers the shelf would hold, not a guess at what "gold" looks like.
// They are here because an unresolved colour is not a cosmetic failure:
// renderColouredPlank falls back to a single UNCOLOURED model, so a shelf that
// has never been synced silently produces plain planks. That is what happened
// in production, where 103 of 141 planks built with no colour at all.
var fallbackColours = map[string]string{
	"white":     "#FFFFFF",
	"red":       "#E4002B",
	"baby pink": "#F4C2C2",
	"pink":      "#FF69B4",
	"silver":    "#C0C0C0",
	"grey":      "#808080",
	"gray":      "#808080",
	"brown":     "#8B4513",

	"black":  "#1A1A1A",
	"blue":   "#1560BD",
	"gold":   "#D4AF37",
	"green":  "#2E9B3F",
	"ivory":  "#FFFFF0",
	"orange": "#FF7518",
	"purple": "#7D3CB5",
	"yellow": "#FFD400",
}

// errUnknownColour means the colour cannot be resolved to a swatch. The job is
// held rather than printed in a guess.
var errUnknownColour = errors.New("unknown colour")

// resolveColourHex maps a colour name to "#RRGGBB".
//
// The colour map wins, because it is the only source that describes the spool
// PHYSICALLY IN A MACHINE: an operator stood in front of the printer and
// confirmed that this hex is what the shop calls blue. The filament shelf comes
// next, then the built-in table, and an unrecognised name is an error rather
// than a default - silently printing black lettering on a plank somebody
// ordered in pink is the failure this exists to prevent.
//
// The map outranks the SHELF, not merely the built-in table, and that ordering
// is the point of it. The shelf's colour_hex is BambuBuddy's catalogue swatch,
// and the catalogue is exactly what these printers disagree with: it calls blue
// #1560BD while every blue spool in this fleet reports #2850E0. Declaring the
// catalogue value is what made BambuBuddy refuse plate after plate.
func (s *Server) resolveColourHex(ctx context.Context, colour string) (string, error) {
	// Canonicalised first, so a colour the shop prints from another spool
	// resolves to THAT spool's swatch: sky blue is the blue filament, and a
	// model painted #87CEEB would describe a plank the shelf cannot print.
	name := production.CanonicalColourName(colour)
	if name == "" {
		return "", errUnknownColour
	}

	mapped, err := s.mappedColourHex(ctx, name)
	if err != nil {
		return "", err
	}
	shelf, err := s.shelfColourHex(ctx, name)
	if err != nil {
		return "", err
	}
	if hex, ok := pickColourHex(mapped, shelf, name); ok {
		return hex, nil
	}
	return "", errUnknownColour
}

// pickColourHex is the precedence rule, kept pure so it can be tested without a
// database: operator-confirmed spool, then the synced shelf, then the built-in
// table.
//
// A candidate that is present but unreadable falls through to the next source
// rather than failing the render. A malformed row is a data-entry problem, and
// holding a job over one when a perfectly good answer sits behind it would turn
// a typo into a stopped bed.
func pickColourHex(mapped, shelf *string, canonicalName string) (string, bool) {
	for _, candidate := range []*string{mapped, shelf} {
		if candidate == nil {
			continue
		}
		if normalised, ok := normaliseHex(*candidate); ok {
			return normalised, true
		}
	}
	if hex, ok := fallbackColours[strings.ToLower(canonicalName)]; ok {
		return hex, true
	}
	return "", false
}

// mappedColourHex is the operator-confirmed swatch for a canonical colour name,
// or nil when nobody has recorded one.
func (s *Server) mappedColourHex(ctx context.Context, name string) (*string, error) {
	hex, err := s.store.Q.GetColourMapPrimaryHex(ctx, name)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &hex, nil
}

// shelfColourHex is the synced filament shelf's swatch, or nil when the shelf
// has no row or no hex for this colour.
func (s *Server) shelfColourHex(ctx context.Context, name string) (*string, error) {
	hex, err := s.store.Q.GetColourHexByName(ctx, name)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return hex, nil
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
