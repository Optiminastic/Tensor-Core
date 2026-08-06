package production

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PersonalisationRules defines what personalization a product (design) offers:
// which fields, the allowed values, name length limits, and an optional placement
// zone. Stored as jsonb on designs.personalisation_rules; a disabled (or zero)
// rule set means the product is not personalizable.
type PersonalisationRules struct {
	Enabled  bool       `json:"enabled"`
	Name     NameRule   `json:"name"`
	Font     EnumRule   `json:"font"`
	Colour   EnumRule   `json:"colour"`
	Variant  EnumRule   `json:"variant"`
	Photo    ToggleRule `json:"photo"`
	Approval ToggleRule `json:"approval"`
	Zone     *ZoneRule  `json:"zone,omitempty"`
}

// NameRule bounds a free-text name/engraving.
type NameRule struct {
	Required  bool `json:"required"`
	MinLength int  `json:"min_length"`
	MaxLength int  `json:"max_length"`
}

// EnumRule is a field the product offers a fixed set of choices for (font,
// colour, variant). An empty Allowed means "any value".
type EnumRule struct {
	Required bool     `json:"required"`
	Allowed  []string `json:"allowed"`
}

// ToggleRule is a required-or-not flag (photo, customer approval).
type ToggleRule struct {
	Required bool `json:"required"`
}

// ZoneRule is the printable area the personalisation must fit within.
type ZoneRule struct {
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
}

// approxCharWidthMM is a rough per-character advance used only for the advisory
// "may not fit the zone" hint. The real fit is judged from the sliced preview.
const approxCharWidthMM = 6.0

// ValidateAgainstRules validates a line item's personalisation against the
// product's rules, returning the resulting status, per-field confirmations, and
// human-readable issues for anything that fails a rule. A disabled rule set means
// the product does not offer personalisation (not_required). It replaces the
// generic AutoValidatePersonalisation when a product defines rules.
func ValidateAgainstRules(rules PersonalisationRules, li LineItem) (string, Confirms, []string) {
	if !rules.Enabled {
		return PersonalisationNotRequired, Confirms{}, nil
	}
	var issues []string
	c := Confirms{
		Name:     validateName(rules.Name, li.PersonalisationName, &issues),
		Font:     validateEnum("Font", rules.Font, li.PersonalisationFont, &issues),
		Colour:   validateEnum("Colour", rules.Colour, li.PersonalisationColour, &issues),
		Variant:  validateEnum("Variant", rules.Variant, li.PersonalisationVariant, &issues),
		Photo:    validateToggle("Photo", rules.Photo, nonEmpty(li.PersonalisationPhotoURL), &issues),
		Approval: validateToggle("Customer approval", rules.Approval, li.CustomerApprovalReceived, &issues),
	}
	if rules.Zone != nil {
		checkZoneFit(*rules.Zone, li.PersonalisationName, &issues)
	}
	return StatusFor(c), c, issues
}

func validateName(rule NameRule, value *string, issues *[]string) bool {
	name := strings.TrimSpace(strOrEmpty(value))
	if name == "" {
		if rule.Required {
			*issues = append(*issues, "Name is required")
			return false
		}
		return true
	}
	n := utf8.RuneCountInString(name)
	if rule.MaxLength > 0 && n > rule.MaxLength {
		*issues = append(*issues, fmt.Sprintf("Name is %d characters; the limit is %d", n, rule.MaxLength))
		return false
	}
	if rule.MinLength > 0 && n < rule.MinLength {
		*issues = append(*issues, fmt.Sprintf("Name must be at least %d characters", rule.MinLength))
		return false
	}
	return true
}

func validateEnum(label string, rule EnumRule, value *string, issues *[]string) bool {
	v := strings.TrimSpace(strOrEmpty(value))
	if v == "" {
		if rule.Required {
			*issues = append(*issues, label+" is required")
			return false
		}
		return true
	}
	if len(rule.Allowed) > 0 && !containsFold(rule.Allowed, v) {
		*issues = append(*issues, fmt.Sprintf("%s %q is not offered for this product", label, v))
		return false
	}
	return true
}

func validateToggle(label string, rule ToggleRule, satisfied bool, issues *[]string) bool {
	if rule.Required && !satisfied {
		*issues = append(*issues, label+" is required but missing")
		return false
	}
	return true
}

// checkZoneFit adds an advisory issue when the name is likely too wide for the
// personalisation zone. It never fails a confirmation (the real fit comes from
// the sliced preview) - it only surfaces a heads-up for the operator.
func checkZoneFit(zone ZoneRule, name *string, issues *[]string) {
	n := utf8.RuneCountInString(strings.TrimSpace(strOrEmpty(name)))
	if zone.WidthMM <= 0 || n == 0 {
		return
	}
	if float64(n)*approxCharWidthMM > zone.WidthMM {
		*issues = append(*issues, fmt.Sprintf(
			"Name may not fit the %.0fx%.0fmm zone at a normal size", zone.WidthMM, zone.HeightMM))
	}
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
