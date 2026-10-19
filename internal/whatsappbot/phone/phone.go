// Package phone normalises, compares and masks phone numbers. The canonical form
// is digits only with the country code and no "+", which is exactly what Meta
// uses as a WhatsApp ID (wa_id), e.g. 919113994682.
package phone

import "strings"

const (
	defaultCountryCode = "91"
	nationalLength     = 10
	minE164Length      = 11
	maxE164Length      = 15
	maskKeepTail       = 4
)

// Normalize turns any human-entered number into canonical form. Bare 10-digit
// numbers and numbers with a single trunk "0" are treated as Indian.
func Normalize(raw string) (string, bool) {
	digits := onlyDigits(raw)
	digits = strings.TrimPrefix(digits, "00")

	switch {
	case len(digits) == nationalLength:
		return defaultCountryCode + digits, true
	case len(digits) == nationalLength+1 && digits[0] == '0':
		return defaultCountryCode + digits[1:], true
	case len(digits) >= minE164Length && len(digits) <= maxE164Length:
		return digits, true
	}
	return "", false
}

// Last10 returns the last ten digits, the part that identifies an Indian mobile
// regardless of how the country code was written.
func Last10(raw string) string {
	digits := onlyDigits(raw)
	if len(digits) < nationalLength {
		return ""
	}
	return digits[len(digits)-nationalLength:]
}

// Match reports whether two numbers refer to the same subscriber.
func Match(a, b string) bool {
	la, lb := Last10(a), Last10(b)
	return la != "" && la == lb
}

// Mask hides all but the country code and last four digits, for logs.
func Mask(canonical string) string {
	if len(canonical) <= maskKeepTail+len(defaultCountryCode) {
		return strings.Repeat("*", len(canonical))
	}
	head := canonical[:len(defaultCountryCode)]
	tail := canonical[len(canonical)-maskKeepTail:]
	hidden := len(canonical) - len(head) - len(tail)
	return "+" + head + strings.Repeat("*", hidden) + tail
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
