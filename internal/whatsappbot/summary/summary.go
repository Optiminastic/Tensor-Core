// Package summary turns an order's line-item custom attributes into the single
// line of text the customer confirms on WhatsApp. It is generic on purpose: every
// product names its steps differently ("STEP 4-First Name-", "First Name on
// Plank", "STEP 4 - Name On 3D Rose"), so no product needs its own code.
package summary

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLength keeps the summary inside a WhatsApp template parameter with room to
// spare for the rest of the template body (1024 characters in total).
const MaxLength = 900

const (
	itemSeparator  = " | "
	fieldSeparator = ", "
	ellipsis       = "..."
)

// stepPrefix matches the storefront's "STEP 4-" / "STEP 2 - " key prefixes.
var stepPrefix = regexp.MustCompile(`(?i)^\s*step\s*\d+\s*-?\s*`)

type Attribute struct {
	Key   string
	Value string
}

type LineItem struct {
	Title        string
	VariantTitle string
	Attributes   []Attribute
}

type Result struct {
	Text               string
	Warnings           []string
	HasPersonalisation bool
}

// Build summarises every personalised line item. Items without customer-entered
// attributes (a plain moon lamp) are left out.
func Build(items []LineItem) Result {
	var parts, warnings []string
	for _, item := range items {
		fields, itemWarnings := personalFields(item.Attributes)
		if len(fields) == 0 {
			continue
		}
		parts = append(parts, itemHeading(item)+": "+strings.Join(fields, fieldSeparator))
		warnings = append(warnings, itemWarnings...)
	}
	if len(parts) == 0 {
		return Result{}
	}

	text := strings.Join(parts, itemSeparator)
	if len(warnings) > 0 {
		text += itemSeparator + "Please check: " + strings.Join(warnings, "; ")
	}
	return Result{Text: truncate(singleLine(text)), Warnings: warnings, HasPersonalisation: true}
}

// WhatsAppNumber returns the number a customer typed into a "WhatsApp Number"
// step, or "" when the product has no such step.
func WhatsAppNumber(items []LineItem) string {
	for _, item := range items {
		for _, a := range item.Attributes {
			if isWhatsAppKey(a.Key) && strings.TrimSpace(a.Value) != "" {
				return strings.TrimSpace(a.Value)
			}
		}
	}
	return ""
}

func personalFields(attrs []Attribute) (fields, warnings []string) {
	for _, a := range attrs {
		if isHidden(a.Key) {
			continue
		}
		value := singleLine(a.Value)
		if value == "" {
			continue
		}
		key := cleanKey(a.Key)
		if key == "" {
			fields = append(fields, value)
		} else {
			fields = append(fields, key+" "+value)
		}
		if isNameKey(key) {
			warnings = append(warnings, nameWarnings(value)...)
		}
	}
	return fields, warnings
}

func nameWarnings(value string) []string {
	var out []string
	if strings.Contains(value, " ") {
		out = append(out, fmt.Sprintf("%q has a space", value))
	}
	if hasNonASCIILetter(value) {
		out = append(out, fmt.Sprintf("%q has special characters", value))
	}
	return out
}

func itemHeading(item LineItem) string {
	title := singleLine(item.Title)
	if variant := singleLine(item.VariantTitle); variant != "" {
		return title + " (" + variant + ")"
	}
	return title
}

// isHidden drops app-internal keys ("_has_gpo") and the customer's phone number,
// which is personal data and must never be echoed into an order summary.
func isHidden(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "_") || isWhatsAppKey(key)
}

func isWhatsAppKey(key string) bool {
	return strings.Contains(strings.ToLower(key), "whatsapp")
}

func isNameKey(key string) bool {
	return strings.Contains(strings.ToLower(key), "name")
}

func cleanKey(key string) string {
	key = stepPrefix.ReplaceAllString(key, "")
	return singleLine(strings.Trim(key, " -"))
}

// singleLine collapses all whitespace runs to one space: template parameters
// reject newlines, tabs and more than four consecutive spaces.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func hasNonASCIILetter(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func truncate(s string) string {
	if utf8.RuneCountInString(s) <= MaxLength {
		return s
	}
	runes := []rune(s)
	return string(runes[:MaxLength-len(ellipsis)]) + ellipsis
}
