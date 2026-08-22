package personalise

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// The store names its storefront fields per product ("STEP 4-First Name-",
// "LEFT NAME", "NAME on PLANK"), so a personalisation is read by what a label
// means rather than by an exact key. These are the meanings the plank template
// needs; anything else on the line is carried through to the operator but does
// not change the geometry.
var (
	firstNamePattern  = regexp.MustCompile(`(?i)(first|left|1st)\s*name|^name\b|name on`)
	secondNamePattern = regexp.MustCompile(`(?i)(second|right|2nd)\s*name`)
	heartPattern      = regexp.MustCompile(`(?i)heart`)
	leadingCount      = regexp.MustCompile(`^\s*(\d+)`)
)

// TemplateForProduct maps a product to the template that can build it. Empty
// means the product is not automatable yet - the caller reports that rather
// than guessing with the wrong geometry.
func TemplateForProduct(productName string) string {
	name := strings.ToLower(productName)
	switch {
	case strings.Contains(name, "dual name plank"):
		return "dual_name_plank"
	case strings.Contains(name, "single name plank"), strings.Contains(name, "desk name plank"):
		return "dual_name_plank" // one name, second line left empty
	default:
		return ""
	}
}

// PlankParams reads a line item's options into template parameters. The line's
// own options are the source of truth: they are what the customer typed and
// what the operator sees on the order.
func PlankParams(item production.LineItem, heartSTL string) map[string]string {
	first, second := "", ""
	hearts := 0
	for _, option := range item.Options {
		switch {
		case secondNamePattern.MatchString(option.Name):
			second = strings.TrimSpace(option.Value)
		case firstNamePattern.MatchString(option.Name):
			if first == "" {
				first = strings.TrimSpace(option.Value)
			}
		case heartPattern.MatchString(option.Name), heartPattern.MatchString(option.Value):
			hearts = countPrefix(option.Value)
		}
	}
	// Fall back to the typed field, which the import fills from the same
	// options - a line whose labels this file does not recognise still prints
	// the right name.
	if first == "" && item.PersonalisationName != nil {
		names := strings.SplitN(*item.PersonalisationName, " / ", 2)
		first = strings.TrimSpace(names[0])
		if len(names) == 2 {
			second = strings.TrimSpace(names[1])
		}
	}

	params := map[string]string{
		"name_one": Quote(strings.ToUpper(first)),
		"name_two": Quote(strings.ToUpper(second)),
		"hearts":   strconv.Itoa(hearts),
	}
	if hearts > 0 && heartSTL != "" {
		params["heart_stl"] = Quote(heartSTL)
	}
	return params
}

// countPrefix reads the count a storefront option leads with - "2 RED HEART"
// is two hearts. A value with no number ("RED HEART") means one.
func countPrefix(value string) int {
	if m := leadingCount.FindStringSubmatch(value); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil && n >= 0 {
			return n
		}
	}
	if strings.TrimSpace(value) == "" {
		return 0
	}
	return 1
}

// RenderLineItem builds the printable model for one personalised line item.
// Returns ErrNoTemplate when the product has no template yet.
func (r *Renderer) RenderLineItem(ctx context.Context, item production.LineItem, heartAsset string) ([]byte, error) {
	template, params, err := r.plan(item, heartAsset)
	if err != nil {
		return nil, err
	}
	return r.RenderSTL(ctx, template, params)
}

// PreviewLineItem renders the same model as a picture, for the order page.
func (r *Renderer) PreviewLineItem(ctx context.Context, item production.LineItem, heartAsset string, width, height int) ([]byte, error) {
	template, params, err := r.plan(item, heartAsset)
	if err != nil {
		return nil, err
	}
	return r.RenderPNG(ctx, template, params, width, height)
}

func (r *Renderer) plan(item production.LineItem, heartAsset string) (string, map[string]string, error) {
	template := TemplateForProduct(item.ProductName)
	if template == "" {
		return "", nil, ErrNoTemplate
	}
	params := PlankParams(item, r.AssetPath(heartAsset))
	if item.Colour != nil {
		if css := CSSColour(*item.Colour); css != "" {
			params["model_colour"] = Quote(css)
		}
	}
	return template, params, nil
}

// filamentColours maps what the storefront calls a filament to something a
// renderer understands. Only the preview picture uses it: an STL has no colour,
// so a name missing from this map costs a grey preview, never a wrong print.
var filamentColours = map[string]string{
	"baby pink": "#f4c2c2", "pink": "#ff9ec7", "red": "#d62828", "blue": "#1d4ed8",
	"sky blue": "#7dd3fc", "black": "#1f2937", "white": "#f8fafc", "yellow": "#facc15",
	"gold": "#d4af37", "silver": "#c0c0c0", "purple": "#7c3aed", "green": "#16a34a",
	"orange": "#f97316", "grey": "#9ca3af", "gray": "#9ca3af", "brown": "#8b5e3c",
	"beige": "#e8d8c3", "transparent": "#e5e7eb", "glow": "#c7f9cc",
}

// CSSColour resolves a storefront colour name. Returns empty when the shop uses
// a name this does not know, which leaves the preview in its default grey
// rather than showing a colour nobody ordered.
func CSSColour(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if css, ok := filamentColours[key]; ok {
		return css
	}
	// "BABY PINK / NO LIGHT" and similar: try the leading segment.
	if head, _, found := strings.Cut(key, "/"); found {
		if css, ok := filamentColours[strings.TrimSpace(head)]; ok {
			return css
		}
	}
	return ""
}
