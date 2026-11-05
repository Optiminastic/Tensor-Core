package personalise

// A design file saying for itself which order field feeds which variable.
//
// The mapping is configuration, and configuration is typed by a person. For
// one product that is fine. For a catalogue it is the thing that makes every
// new design wait on somebody who knows what NAME_L is - and the shop's own
// labels make that worse than it sounds: the same first name is called
// "STEP 4-First Name-", "first name on plank", "name-1", "left name" and
// "top name" across five live products, so there is nothing to generalise
// from and every product is a fresh reading of its own orders.
//
// The person who draws the file already knows. They named the variable, they
// built the storefront field, and they are the only one who can say the two
// are the same thing. So let them say it, where they are already working:
//
//	NAME_L = "";  // The left name. @from Name 1 @max 20
//	NAME_R = "";  // @from Name 2 @optional
//	HEARTS = 1;   // @from Heart @count @default 0
//	OUT_X  = 200; // @fixed
//
// This needs no parser of its own. DeclaredParams already reads the trailing
// comment off every top-level assignment and carries it through as Param.Note,
// because that comment was the only documentation these files had. It is now
// also where they can be configured.
//
// A SUGGESTION, NEVER THE LIVE MAPPING. The registry row stays the source of
// truth and a person still presses save. A file that configured a product on
// upload would mean re-uploading a corrected drawing silently re-pointed a
// live product's fields - and the first sign of it would be a batch of planks
// with the names swapped.

import (
	"strconv"
	"strings"
)

// Annotation is what one declared variable says about how it should be filled.
//
// Every field is optional. A note with no directives at all yields ok=false
// from Annotate, which is the common case: most of these comments are prose
// written for a human and must stay readable as prose.
type Annotation struct {
	// FromRaw is the storefront label as the author wrote it, kept for
	// showing back - "STEP 4-First Name-" is what somebody recognises.
	FromRaw string
	// From is FromRaw normalised, which is what matching actually uses.
	From string
	// Fixed means this variable carries a written value rather than reading
	// one from the order. Not every variable a template needs is the
	// customer's: a plank needs OUT_X=200 to come out the size it is sold at.
	Fixed bool
	// FixedValue is that value. Empty with Fixed set means "whatever the file
	// already declares", which is the useful reading for OUT_X = 200: the
	// author states the size once, in the file, and does not repeat it.
	FixedValue string
	// Optional lets an order not answer. Absent means required, matching the
	// registry's own default - a blank name on a plank is scrap, and holding
	// the order is the right answer unless the author says otherwise.
	Optional bool
	// Default is what an unanswered optional field passes instead of letting
	// the template's own default stand.
	Default string
	// Kind is how the answer is read. KindStated says whether the author
	// chose it; when they did not, the caller infers it from the declared
	// type, which is better evidence than a guess here.
	Kind       ValueKind
	KindStated bool
	// Max is the character limit the design's text box fits, 0 when unstated.
	Max int
}

// maxAnnotatedRunes bounds @max, matching the product_field_maps check
// constraint. A file asking for 500 is refused rather than stored and later
// rejected by Postgres.
const maxAnnotatedRunes = 120

// Annotate reads the directives out of one declared variable's comment.
//
// ok is false when the comment carries no directive, so a caller can tell
// "the author said nothing" from "the author said required with no limit".
func Annotate(p Param) (Annotation, bool) {
	note := strings.TrimSpace(p.Note)
	if note == "" || !strings.Contains(note, "@") {
		return Annotation{}, false
	}

	var (
		out  Annotation
		any  bool
		rest = note
	)
	for {
		at := strings.Index(rest, "@")
		if at < 0 {
			break
		}
		word, value, next := directiveAt(rest[at+1:])
		rest = next
		if applyDirective(&out, word, value) {
			any = true
		}
	}
	if !any {
		return Annotation{}, false
	}
	return out, true
}

// directiveAt splits "from Name 1 @max 20" into "from", "Name 1" and the
// remainder beginning at the next @.
//
// The value runs to the next directive rather than to the next space, because
// these labels have spaces in them - "Name 1" and "name on 3d rose" are two
// real ones - and a one-word reading would map half a label and match nothing.
func directiveAt(s string) (word, value, rest string) {
	end := strings.Index(s, "@")
	if end < 0 {
		end = len(s)
	}
	body := s[:end]
	rest = s[end:]

	space := strings.IndexFunc(body, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '='
	})
	if space < 0 {
		return strings.ToLower(strings.TrimSpace(body)), "", rest
	}
	word = strings.ToLower(strings.TrimSpace(body[:space]))
	value = strings.Trim(strings.TrimSpace(endOfSentence(body[space+1:])), ".,;:")
	return word, value, rest
}

// endOfSentence cuts a directive's value where the prose resumes.
//
// The rule a person has to hold is "prose first, directives last", and a
// value otherwise running to the next @ is what lets a label keep its spaces -
// "STEP 4 - Name on 3D Rose" is a real one. But "@from Name 1. Keep it short"
// is the mistake somebody will make, and mapping "name 1 keep it short" would
// match nothing in the way that looks exactly like an order carrying no
// personalisation at all.
//
// Only ". " and ";" cut, both unambiguous: no storefront label in this shop
// contains either, while every one of them contains spaces, digits or dashes.
// A comma does NOT cut - a label is likelier to carry one than a directive is
// to be followed by a clause beginning with it.
func endOfSentence(s string) string {
	cut := len(s)
	if i := strings.Index(s, ". "); i >= 0 {
		cut = i
	}
	if i := strings.Index(s, ";"); i >= 0 && i < cut {
		cut = i
	}
	return s[:cut]
}

// applyDirective records one directive, reporting whether it was recognised.
//
// An unrecognised word is IGNORED rather than refused. These comments are
// prose first: "@see the lotus file" is a sentence somebody wrote for a
// colleague, and refusing the upload over it would make the feature a
// liability on files that never asked to use it.
func applyDirective(out *Annotation, word, value string) bool {
	switch word {
	case "from":
		if value == "" {
			return false
		}
		out.FromRaw = value
		out.From = NormaliseKey(value)
		return out.From != ""
	case "fixed":
		out.Fixed = true
		out.FixedValue = value
		return true
	case "optional":
		out.Optional = true
		return true
	case "required":
		out.Optional = false
		return true
	case "default":
		out.Default = value
		return true
	case "max", "maxchars", "max_chars":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 || n > maxAnnotatedRunes {
			return false
		}
		out.Max = n
		return true
	case "text", "string":
		out.Kind, out.KindStated = ValueString, true
		return true
	case "number":
		out.Kind, out.KindStated = ValueNumber, true
		return true
	case "count":
		out.Kind, out.KindStated = ValueCount, true
		return true
	}
	return false
}

// AnnotatedParams is every variable in a file that configures itself.
//
// In file order, like DeclaredParams, so the author's own grouping survives.
func AnnotatedParams(source []byte) map[string]Annotation {
	out := map[string]Annotation{}
	for _, p := range DeclaredParams(source) {
		if a, ok := Annotate(p); ok {
			out[p.Name] = a
		}
	}
	return out
}
