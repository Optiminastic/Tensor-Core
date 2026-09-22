package httpapi

// Naming a spool from the manufacturer's own catalogue.
//
// An AMS reports a colour as a bare hex and nothing else - across this whole
// fleet every tray answers tray_type "PLA", an empty tray_sub_brands and the
// catch-all filament id GFL99, because the spools are generic. So the printer
// cannot say what it is holding.
//
// BambuBuddy can, for some of them. It ships a catalogue of manufacturers'
// colours, and an exact hex match there is a real name from whoever made the
// filament: #D3B7A7 is Bambu Lab's "Latte Brown". Six of this fleet's fifteen
// loaded hexes are named that way, which is six fewer an operator has to invent
// a word for.
//
// It remains a SUGGESTION. The catalogue says what a manufacturer calls a
// colour, not what this shop calls it - a shop that has always called the
// brown-ish spool GOLD is not wrong, and the map records the shop's language.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// catalogueName is what a manufacturer calls one hex.
type catalogueName struct {
	Name  string
	Brand string
}

// colourCatalogue caches BambuBuddy's colour list.
//
// Cached for an hour because it is a shipped reference table that changes when
// BambuBuddy is upgraded, not while somebody is filling in a form - and it is
// ~100KB, which is not something to re-fetch every time the Inventory page is
// opened.
type colourCatalogue struct {
	mu     sync.Mutex
	ttl    time.Duration
	byHex  map[string]catalogueName
	expiry time.Time
}

func newColourCatalogue(ttl time.Duration) *colourCatalogue {
	return &colourCatalogue{ttl: ttl}
}

// lookup returns hex -> manufacturer's name, empty when unavailable.
//
// A failure returns an empty map rather than an error: the catalogue only ever
// improves a suggestion, so losing it costs a better default and nothing else.
func (c *colourCatalogue) lookup(
	ctx context.Context, load func(context.Context) ([]bambubuddy.CatalogueColour, error),
) map[string]catalogueName {
	c.mu.Lock()
	if c.byHex != nil && time.Now().Before(c.expiry) {
		defer c.mu.Unlock()
		return c.byHex
	}
	c.mu.Unlock()

	colours, err := load(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("colour catalogue unavailable; suggesting from known colours only",
			"error", err)
		return map[string]catalogueName{}
	}
	built := catalogueByHex(colours)

	c.mu.Lock()
	c.byHex, c.expiry = built, time.Now().Add(c.ttl)
	c.mu.Unlock()
	return built
}

// catalogueByHex indexes the catalogue, one name per hex.
//
// A hex can carry several names - #FFFFFF is Ivory White, Jade White and
// Pristine White depending on the brand and the plastic - and there is no way
// to tell which spool is in the tray. The default entry wins where one is
// marked, then the alphabetically first, so the same fleet always suggests the
// same word rather than whichever the map iterated to first.
func catalogueByHex(colours []bambubuddy.CatalogueColour) map[string]catalogueName {
	out := make(map[string]catalogueName, len(colours))
	best := make(map[string]bambubuddy.CatalogueColour, len(colours))
	for _, c := range colours {
		hex, ok := normaliseHex(c.HexColor)
		if !ok || strings.TrimSpace(c.ColorName) == "" {
			continue
		}
		if incumbent, seen := best[hex]; seen && !preferredCatalogue(c, incumbent) {
			continue
		}
		best[hex] = c
	}
	for hex, c := range best {
		out[hex] = catalogueName{
			Name:  strings.TrimSpace(c.ColorName),
			Brand: strings.TrimSpace(c.Manufacturer),
		}
	}
	return out
}

func preferredCatalogue(candidate, incumbent bambubuddy.CatalogueColour) bool {
	if candidate.IsDefault != incumbent.IsDefault {
		return candidate.IsDefault
	}
	return strings.ToUpper(candidate.ColorName) < strings.ToUpper(incumbent.ColorName)
}
