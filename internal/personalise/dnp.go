package personalise

// Dual Name Plank: an order's two names and heart count turned into render
// parameters.
//
// The product is a plank whose lettering reads as one name from the left and a
// different name from the right, the two intersected into a single solid. The
// heart count is not a decoration setting - it selects a different template,
// because the three variants differ in margins, plate thickness and whether the
// Fusion shear is applied, and those are not values that can be layered on top
// of one another.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// The three templates, one per heart count. They are not cosmetic variants of
// each other: dual_name.scad renders with FUSION_MATCH on (reproducing the
// 58.43%/70.11% shear from the Fusion master) while the other two do not, and
// the margins differ - 40/40, 9/35 and 9/9 respectively.
const (
	templateTwoHeart = "dnp_two_heart"
	templateOneHeart = "dual_one_heart"
	templateNoHeart  = "dnp_with_no_heart"
)

// The finished product size, in millimetres. Every plank ships at exactly this
// size regardless of how long the names are.
//
// The scale is deliberately NON-UNIFORM, and it distorts the lettering. That is
// a decision, not an oversight, so do not "fix" it without asking:
//
// The template draws letters at the font's natural width, so the plank grows
// with the text - "MANOHAR"/"APARNA" measures 317mm before scaling, and a
// twelve-letter pair 427mm. Forcing 317 into 200 scales X by 0.63 while
// stretching Z by 1.61, which thins every stroke: a stroke-to-cap ratio of
// 0.118 against the 0.302 measured off the Fusion master. The letters come out
// visibly more spindly the longer the name.
//
// Measured alternatives, both rejected: letting the plank grow keeps the 0.302
// ratio but gives every order a different length, and a uniform scale into
// 200mm keeps the ratio but leaves the plank 32x16 instead of 50x40. Exact
// product dimensions were judged to matter more than letter weight.
const (
	OutputXMM = 200
	OutputYMM = 50
	OutputZMM = 40
)

// Margins are NOT set here in the general case. They live in each template,
// where the shop edits them directly - 60/60, 12/50 and 12/12 at the time of
// writing. Overriding them wholesale meant a change to the .scad quietly did
// nothing, which is a worse failure than a wrong number: the file says one
// thing and the printer does another.
//
// The two-heart plank is the one exception, below.

// A long name on the two-heart plank needs wider margins.
//
// Two hearts already consume padding slots, so a long name on top of them runs
// the glyphs into the plate edge. Widening the margins to 70mm shortens the
// zone the letters are squeezed into, which the template handles by squeezing
// harder rather than by overflowing.
//
// Only the two-heart template, and only past the threshold: below it the
// file's own 60/60 stands, so the shop can still tune the normal case by
// editing the .scad.
const (
	wideMarginNameLetters = 7
	wideMarginMM          = 70
)

// needsWideMargins reports whether this plank is a long-named two-heart one.
//
// Either name counts. The two are laid out as one intersected run, so the
// longer of them sets the width whichever side it came from.
func (p Params) needsWideMargins() bool {
	if p.Template != templateTwoHeart {
		return false
	}
	// Runes, not bytes: a name is counted in letters as a person would count
	// them, and a multi-byte character must not count double.
	return len([]rune(p.NameLeft)) > wideMarginNameLetters ||
		len([]rune(p.NameRight)) > wideMarginNameLetters
}

// maxNameLetters is the longest name the template lays out sensibly.
//
// MAX_LETTERS in the template is 12; past that the glyph run is squeezed hard
// enough that the letters stop being readable from either side, which is the
// whole point of the product. Better to hold the job than print something
// nobody can read.
const maxNameLetters = 12

// Params is one plank's render inputs, resolved from an order line.
type Params struct {
	// Template is which of the three .scad files to render.
	Template string
	// NameLeft reads from the left of the finished plank, NameRight from the
	// right. Order matters: they are different faces of the same solid.
	NameLeft  string
	NameRight string
	// Hearts is what the customer asked for, kept for the operator's benefit
	// even though Template already encodes it.
	Hearts int
}

// Args renders Params as OpenSCAD -D parameters.
//
// The output size is included on every render. Leaving it out would produce a
// correct-looking model at the template's natural size, which is the exact
// mistake this package exists to stop.
// PartAll renders base and lettering as one solid; PartBase and PartText
// render them separately so each can be given its own colour in a 3MF.
//
// The two separate renders share a coordinate space: OUT_X/Y/Z is computed from
// the letter run, not from whichever part is being drawn, so both come out at
// the same scale and drop straight into the same model without a transform.
const (
	PartAll  = "all"
	PartBase = "base"
	PartText = "text"
)

// ArgsForPart is Args with PART set, for the two-colour render.
func (p Params) ArgsForPart(part string) map[string]string {
	args := p.Args()
	args["PART"] = Quote(part)
	return args
}

func (p Params) Args() map[string]string {
	args := map[string]string{
		"NAME_L": Quote(p.NameLeft),
		"NAME_R": Quote(p.NameRight),
		// Scales the finished model to the product size. See the note on
		// OutputXMM: this is a non-uniform scale and it thins the lettering on
		// a long name, which is an open question rather than a settled one.
		"OUT_X": strconv.Itoa(OutputXMM),
		"OUT_Y": strconv.Itoa(OutputYMM),
		"OUT_Z": strconv.Itoa(OutputZMM),
	}
	if p.needsWideMargins() {
		args["MARGIN_L"] = strconv.Itoa(wideMarginMM)
		args["MARGIN_R"] = strconv.Itoa(wideMarginMM)
	}
	return args
}

// The storefront's own wording, e.g. "STEP 4-First Name-:". Matched on the
// normalised key the importer already produces, so punctuation and case do not
// matter - see normalisePropKey in httpapi/shopify_import.go.
var (
	keyFirstName  = []string{"step 4 first name", "first name", "personalisation name", "custom name"}
	keySecondName = []string{"step 5 second name", "second name"}
	keyHearts     = []string{"step 3", "step 3 -", "hearts", "heart"}
)

// digits finds the first run of digits in a value, which is how a heart count
// is read out of "1 RED HEART".
var digits = regexp.MustCompile(`\d+`)

// ParamsFromProperties resolves a plank's render inputs from one order line's
// custom attributes.
//
// It reads the RAW properties rather than the job's typed personalisation
// fields on purpose. The importer joins the two names into a single
// "VASU & PADMANABH" string, and splitting that back apart would break on any
// name containing an ampersand - which is exactly the character the join uses.
// The individual names only survive here.
func ParamsFromProperties(props []production.LineProp) (Params, error) {
	left := strings.TrimSpace(lookup(props, keyFirstName...))
	right := strings.TrimSpace(lookup(props, keySecondName...))

	if left == "" || right == "" {
		return Params{}, fmt.Errorf(
			"a plank needs both names; got first=%q second=%q", left, right)
	}
	if n := len([]rune(left)); n > maxNameLetters {
		return Params{}, fmt.Errorf("first name is %d letters; the plank fits %d", n, maxNameLetters)
	}
	if n := len([]rune(right)); n > maxNameLetters {
		return Params{}, fmt.Errorf("second name is %d letters; the plank fits %d", n, maxNameLetters)
	}

	hearts, ok := heartsFromProperties(props)
	if !ok {
		// Not defaulted to zero. "The customer chose no hearts" and "the
		// property did not import" are indistinguishable from here, and
		// guessing wrong prints the wrong product - the plank would come out
		// with 9mm margins instead of 40mm.
		return Params{}, fmt.Errorf("this order does not say how many hearts to put on the plank")
	}

	return Params{
		Template:  templateForHearts(hearts),
		NameLeft:  strings.ToUpper(left),
		NameRight: strings.ToUpper(right),
		Hearts:    hearts,
	}, nil
}

// heartsFromProperties finds the heart count among a line's options.
//
// Every candidate label is tried and the first whose VALUE actually reads as a
// count wins. Matching on the label alone is not enough: the storefront's step
// numbers move between products, so "STEP 3" is the heart option on one product
// and the second name on another, and a combo product carries "STEP 5 - Name On
// Heart Keychain" - a field that mentions hearts and holds a name. Taking the
// first label that mentioned hearts turned an order for a rose and a keychain
// into "could not read a heart count from \"PAVI\"".
//
// Reporting nothing found is the right answer for those orders. They are a
// different product, and a person has to look at them - which is better than a
// plank printed with a heart count invented from a name.
func heartsFromProperties(props []production.LineProp) (int, bool) {
	for _, raw := range lookupAll(props, keyHearts...) {
		if n, err := heartCount(raw); err == nil {
			return n, true
		}
	}
	return 0, false
}

// heartCount reads a count out of the storefront's wording.
//
// A number anywhere in the string wins, so the store can rename "2 RED HEART"
// to "2 PINK HEARTS" without breaking this. Wording that names no number is
// only read as zero when it actually says so - anything else is an error
// rather than a guess.
func heartCount(value string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return 0, fmt.Errorf("the heart option is blank")
	}
	if m := digits.FindString(v); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil {
			return 0, fmt.Errorf("could not read a heart count from %q", value)
		}
		if n < 0 || n > 2 {
			return 0, fmt.Errorf("a plank takes 0, 1 or 2 hearts; this order asks for %d", n)
		}
		return n, nil
	}
	for _, none := range []string{"no heart", "none", "without heart", "no hearts"} {
		if strings.Contains(v, none) {
			return 0, nil
		}
	}
	return 0, fmt.Errorf("could not read a heart count from %q", value)
}

// templateForHearts picks the .scad file for a heart count.
func templateForHearts(hearts int) string {
	switch hearts {
	case 2:
		return templateTwoHeart
	case 1:
		return templateOneHeart
	default:
		return templateNoHeart
	}
}

// lookup returns the first non-empty value among the given keys.
func lookup(props []production.LineProp, keys ...string) string {
	v, _ := lookupRaw(props, keys...)
	return v
}

// lookupRaw returns a value and whether the key was present at all, so an
// absent option can be told apart from a blank one.
//
// Two passes, and the order matters. An EXACT key wins outright; only then does
// a property whose label merely CONTAINS the phrase count. So a store with both
// "First Name" and "First Name on Plank" resolves to the precise one, and a
// store with only the longer label still works.
//
// The second pass exists because the storefront renames these fields and Tensor
// had no idea. Nine live orders carried "First Name on Plank" / "Second Name on
// Plank" and every one of them failed to render with "a plank needs both names;
// got first=\"\" second=\"\"" - the names were right there in the order, under a
// label one word longer than the list this matched against.
func lookupRaw(props []production.LineProp, keys ...string) (string, bool) {
	for _, key := range keys {
		for _, p := range props {
			if normaliseKey(p.Name) == key {
				return p.Value, true
			}
		}
	}
	for _, key := range keys {
		for _, p := range props {
			if containsPhrase(normaliseKey(p.Name), key) {
				return p.Value, true
			}
		}
	}
	return "", false
}

// lookupAll returns every property value matching any of the keys, exact
// matches first and phrase matches after, so a caller that can validate a value
// gets the candidates in the order it should try them.
func lookupAll(props []production.LineProp, keys ...string) []string {
	var out []string
	seen := map[int]bool{}
	for _, key := range keys {
		for i, p := range props {
			if !seen[i] && normaliseKey(p.Name) == key {
				seen[i] = true
				out = append(out, p.Value)
			}
		}
	}
	for _, key := range keys {
		for i, p := range props {
			if !seen[i] && containsPhrase(normaliseKey(p.Name), key) {
				seen[i] = true
				out = append(out, p.Value)
			}
		}
	}
	return out
}

// containsPhrase reports whether label contains key as a run of WHOLE words.
//
// Whole words, not a substring: "heartfelt message" must not answer to "heart",
// and a plank's heart count is not something to get wrong by matching the
// middle of an unrelated label.
func containsPhrase(label, key string) bool {
	if label == "" || key == "" {
		return false
	}
	labelWords := strings.Fields(label)
	keyWords := strings.Fields(key)
	if len(keyWords) == 0 || len(keyWords) > len(labelWords) {
		return false
	}
	for i := 0; i+len(keyWords) <= len(labelWords); i++ {
		matched := true
		for j, w := range keyWords {
			if labelWords[i+j] != w {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// normaliseKey reduces a storefront label to lowercase words separated by
// single spaces, so "STEP 4-First Name-:" matches "step 4 first name".
//
// Deliberately the same reduction as the importer's normalisePropKey. It is
// duplicated rather than shared because that one is unexported in httpapi and
// this package must not depend on the HTTP layer.
func normaliseKey(key string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
