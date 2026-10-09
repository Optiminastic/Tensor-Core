package httpapi

// Naming the win-back discount code.
//
// The customer's first name and the discount: "Seetha Chinna" becomes
// SEETHA10. It reads as meant for them rather than as a coupon off the
// internet, which is the whole reason for making one per person.
//
// A NAME IS NOT A KEY. Two customers called Seetha, or the same Seetha
// abandoning a second cart, both want SEETHA10 - and Shopify holds a code name
// forever, including after the discount expires, so the second attempt is
// refused. The caller retries with a suffixed candidate, which is why this
// returns a SEQUENCE rather than a single name.

import (
	"crypto/rand"
	"strings"
	"unicode"
)

// winbackCodeSuffix is the discount, as it reads in the code, and
// winbackDiscountFraction is the same number as Shopify wants it: a
// FRACTION, where 0.10 is ten per cent. They are declared together so a
// change to one is an obvious prompt to change the other - a code reading
// SEETHA10 that takes off fifteen per cent is a support ticket.
const (
	winbackCodeSuffix       = "10"
	winbackDiscountFraction = 0.10
)

// maxCodeNameLen bounds the name part. Shopify accepts long codes, but a
// customer reading one aloud, or typing it because the link failed, should not
// be given forty characters.
const maxCodeNameLen = 12

// codeAlphabet excludes the characters people misread when a code is spoken or
// copied by hand: O and 0, I and 1, S and 5. A win-back code is read off a
// phone screen by somebody who is already half-decided.
const codeAlphabet = "ABCDEFGHJKLMNPQRTUVWXYZ2346789"

// winbackCodeCandidates is the codes to try for this customer, in order.
//
// The first is the clean one. Each fallback adds randomness, because the
// collision is not a race this process can win by waiting - the name is taken
// by a row that may be months old.
//
// An unnamed customer gets no clean candidate at all: there is no first name
// to personalise with, so every candidate is unique from the start. Shopify
// gives us whatever the shopper typed, and somebody who reached the contact
// step with a phone number alone has no name to use.
func winbackCodeCandidates(customerName string, attempts int) []string {
	if attempts < 1 {
		attempts = 1
	}
	out := make([]string, 0, attempts)

	if first := firstNameForCode(customerName); first != "" {
		out = append(out, first+winbackCodeSuffix)
		for len(out) < attempts {
			out = append(out, first+winbackCodeSuffix+randomCode(4))
		}
		return out
	}

	for len(out) < attempts {
		// No name to lead with, so the code says what it is instead.
		out = append(out, "SAVE"+winbackCodeSuffix+randomCode(5))
	}
	return out
}

// firstNameForCode is the leading word of a name, upper-cased, letters only.
//
// Letters only because a code is typed and spoken: an apostrophe or a hyphen
// in "D'Souza" or "Anne-Marie" survives Shopify but not a phone call, and a
// name in Devanagari cannot be typed on a Latin keyboard at all - which is why
// a name that reduces to nothing falls through to the unnamed path rather than
// producing a code like "10".
func firstNameForCode(name string) string {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range fields[0] {
		if unicode.IsLetter(r) && r < unicode.MaxASCII {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	out := b.String()
	if len(out) > maxCodeNameLen {
		out = out[:maxCodeNameLen]
	}
	// A single letter is not a name worth personalising with - "A10" reads as
	// a serial number, not as somebody's code.
	if len(out) < 2 {
		return ""
	}
	return out
}

// randomCode is n characters of unambiguous randomness.
//
// crypto/rand, not math/rand: these are single-use money-off codes, and a
// predictable sequence is a code somebody else can guess before its owner
// uses it. Falls back to a fixed filler only if the system's randomness is
// unavailable, which would be a far larger problem than this.
func randomCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("X", n)
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = codeAlphabet[int(v)%len(codeAlphabet)]
	}
	return string(out)
}
