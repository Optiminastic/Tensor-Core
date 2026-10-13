package httpapi

// The token that stands between a customer's order and everybody else's.
//
// The whole credential is the URL. There is no password behind it and no
// session in front of it, which is the point - the customer taps a link in
// WhatsApp and the page opens - and it means the token carries the entire
// weight of keeping one person's names, products and models away from
// strangers.
//
// Three decisions follow from that, all copied from internal/auth/invites.go
// because they were already thought through there:
//
//  1. 32 bytes from crypto/rand. Not a UUID, which is 122 bits with structure,
//     and emphatically not the order number, which is sequential - the whole
//     attack is to count.
//  2. ONLY THE HASH IS STORED. A database dump, a log line, a backup, a
//     screenshot of a row: none of them open anybody's link. The raw token
//     exists in exactly two places, the customer's message and the few
//     microseconds between minting it and handing it to the sender.
//  3. Every refusal is the SAME refusal. Expired, confirmed, never existed -
//     one sentence. A different message for "expired" tells somebody probing
//     that the token they guessed was real.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// personalisationTokenBytes is 256 bits of entropy.
//
// The arithmetic that makes this uninteresting to attack: at a million guesses
// a second against every link ever issued, the expected time to find one is
// longer than the universe has existed. The practical defence is still rate
// limiting - this number only means the attacker cannot win by guessing.
const personalisationTokenBytes = 32

// invalidLinkMessage is said for every refusal, whatever the real reason.
//
// Deliberately uninformative, and deliberately not an apology: the customer
// who reads it has usually just come back to a link that timed out, and the
// one thing they need is what to do now.
const invalidLinkMessage = "This link is no longer available. " +
	"Your order is being made with the details you gave at checkout - " +
	"message the shop if something needs changing."

// newPersonalisationToken mints a token and the hash to store beside it.
//
// Returns the raw token first and the hash second, in that order, so a caller
// that mixes them up writes a 43-character token into a 64-character column
// and fails loudly rather than storing the secret it meant to hash.
func newPersonalisationToken() (token, hash string, err error) {
	raw := make([]byte, personalisationTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		// Never silently fall back to a weaker source. A predictable token
		// here is every customer's order readable by anyone who can count.
		return "", "", fmt.Errorf("could not generate a link token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashPersonalisationToken(token), nil
}

// hashPersonalisationToken is what the database holds.
//
// Plain SHA-256, not bcrypt or argon2, and that is correct here rather than
// lazy: those exist to make LOW-entropy secrets expensive to brute-force, and
// this secret has 256 bits of entropy from a CSPRNG. There is nothing to
// slow down. A fast hash also keeps the public page's lookup fast, which is
// the request an attacker would use to load the server.
func hashPersonalisationToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// personalisationLinkURL is the address the customer is sent.
//
// Path, never query string. A token in `?t=` reaches analytics, server access
// logs and the Referer header of every outbound link on the page; in the path
// it still reaches logs, but it is no longer handed to third parties by the
// browser - and the page sets Referrer-Policy to close that too.
func personalisationLinkURL(frontendURL, token string) string {
	base := strings.TrimRight(strings.TrimSpace(frontendURL), "/")
	if base == "" || strings.TrimSpace(token) == "" {
		return ""
	}
	return base + "/p/" + token
}
