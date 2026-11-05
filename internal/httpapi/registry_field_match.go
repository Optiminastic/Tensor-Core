package httpapi

// Guessing which order field feeds which variable, so a person reviews a
// mapping instead of authoring one.
//
// The shop's own labels are why this exists. The same first name is called
// "STEP 4-First Name-", "first name on plank", "name-1", "left name" and
// "top name" across five live products, and the sixth product will invent a
// sixth wording - so there is no convention to read and every new design is
// somebody sitting down with a list of its own orders. The variable names are
// no better behaved: NAME_L, name1 and TEXT all mean the first line of text.
//
// What the two sides DO share is the words in them. "first name on plank" and
// NAME_L both say name, and both say first - one as a word and one as a
// letter. That is enough to propose a pairing, and a proposal is all this is.
//
// A GUESS IS NEVER APPLIED SILENTLY. The failure that matters here is not a
// missed match - a person then picks it from the dropdown as they do today -
// but a CONFIDENT WRONG ONE. Pairing NAME_L with "second name" would render
// every plank with the names swapped: it renders, it slices, it prints, and
// nothing downstream can tell. So an ordinal that disagrees is a refusal
// rather than a lower score, and no key is ever proposed for two variables.

import (
	"strconv"
	"strings"
)

// matchStrength is how much of the variable's own wording the key accounted
// for. Shown to the reviewer, because "likely" and "possible" deserve
// different amounts of attention and an undifferentiated list of guesses gets
// none.
type matchStrength string

const (
	// strengthLikely means every word in the variable's name was accounted
	// for and the two sides agree about position.
	strengthLikely matchStrength = "likely"
	// strengthPossible means some words matched. Worth offering, and worth
	// reading before saving.
	strengthPossible matchStrength = "possible"
)

// fieldWords is one side of a pairing reduced to what it says.
type fieldWords struct {
	// Words are the meaningful tokens, stemmed and de-synonymised, with
	// ordinals and noise removed.
	Words []string
	// Ordinal is which one of a series this is: 1 for left/first/top/1, 2 for
	// right/second/bottom/2, 0 when the wording names no position.
	Ordinal int
}

// noiseWords carry no information about WHICH field this is.
//
// "step" and the number after it are the storefront's form layout, not the
// field: "STEP 4-First Name-" and "STEP 5-Second Name" differ in the step
// number AND in first/second, and reading 4 and 5 as ordinals would make the
// two disagree with every variable in every file.
var noiseWords = map[string]bool{
	"step": true, "on": true, "the": true, "your": true, "for": true,
	"in": true, "of": true, "and": true, "to": true, "please": true,
	"enter": true, "type": true, "write": true, "add": true, "custom": true,
	// Product words. "first name on plank" is a first name; the plank is what
	// every field on that page has in common and so distinguishes none of them.
	"plank": true, "combo": true, "product": true, "item": true,
}

// wordSynonyms collapse the wordings that mean the same thing.
//
// Every entry is drawn from a label or a variable that exists in this shop
// today, not from a general thesaurus: a guess about English is still a
// guess, and one that paired "text" with "note" would invent a match nobody
// can audit.
var wordSynonyms = map[string]string{
	"color": "colour", "colours": "colour", "colors": "colour",
	"image": "photo", "picture": "photo", "img": "photo", "photos": "photo",
	// NAME AND TEXT ARE ONE WORD HERE, found on live data: the Royal diya
	// holder declares TEXT and its orders carry "Name", so without this the
	// one pairing that product needs is the one pairing that is missed.
	//
	// Safe because position outranks wording. On a plank declaring NAME_L,
	// NAME_R and a TEXT, "first name" pairs with NAME_L - which agrees about
	// position and so scores a whole point higher - and TEXT takes what is
	// left rather than stealing it.
	"name": "text", "names": "text", "nam": "text",
	"message": "text", "engraving": "text", "msg": "text", "label": "text",
	"texts": "text", "txt": "text",
	"hearts": "heart", "roses": "rose", "charms": "charm",
	"number": "num", "no": "num", "qty": "num", "quantity": "num",
	"dates": "date", "dob": "date",
	"fonts": "font", "typeface": "font",
	"phone": "whatsapp", "mobile": "whatsapp", "contact": "whatsapp",
}

// ordinalWords place a field in a series.
//
// Left and top are both "the first one", because that is what they mean on
// these products: the plank's two names sit left and right, the holder's two
// sit top and bottom, and the storefront calls the same pair first and second.
var ordinalWords = map[string]int{
	"l": 1, "left": 1, "first": 1, "one": 1, "1": 1, "top": 1,
	"r": 2, "right": 2, "second": 2, "two": 2, "2": 2, "bottom": 2,
	"3": 3, "third": 3, "three": 3,
	"4": 4, "fourth": 4, "four": 4,
}

// wordsOfVariable reads a .scad variable name.
//
// NAME_L, name1, sideHearts and TEXT_COLOR are four conventions in four files
// by three authors, so all four are split: on underscores, on a case change,
// and between letters and digits.
func wordsOfVariable(name string) fieldWords {
	return readWords(splitIdentifier(name))
}

// wordsOfPropertyKey reads a normalised storefront label.
//
// The key arrives already lower-cased and punctuation-free from
// personalise.NormaliseKey - "STEP 4-First Name-:" is "step 4 first name" by
// the time it is stored - so splitting on spaces is the whole job.
func wordsOfPropertyKey(key string) fieldWords {
	return readWords(strings.Fields(key))
}

// splitIdentifier breaks an identifier into its words.
func splitIdentifier(name string) []string {
	var (
		out  []string
		cur  strings.Builder
		prev rune
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == ' ':
			flush()
		case prev != 0 && isASCIIDigit(r) != isASCIIDigit(prev):
			flush()
			cur.WriteRune(r)
		case r >= 'A' && r <= 'Z' && prev >= 'a' && prev <= 'z':
			// camelCase, but NOT an all-caps run: splitting NAME_L on every
			// capital would yield n, a, m, e.
			flush()
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
		prev = r
	}
	flush()
	lowered := make([]string, 0, len(out))
	for _, w := range out {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			lowered = append(lowered, w)
		}
	}
	return lowered
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// readWords turns raw tokens into the comparable form.
func readWords(tokens []string) fieldWords {
	out := fieldWords{Words: make([]string, 0, len(tokens))}
	dropNumber := false
	for _, raw := range tokens {
		word := canonicalWord(raw)
		if dropNumber {
			dropNumber = false
			// The number after "step" is the form's layout. Dropped here
			// rather than read as an ordinal - see noiseWords.
			if _, err := strconv.Atoi(word); err == nil {
				continue
			}
		}
		if word == "step" {
			dropNumber = true
			continue
		}
		if noiseWords[word] {
			continue
		}
		if n, ok := ordinalWords[word]; ok {
			if out.Ordinal == 0 {
				out.Ordinal = n
			}
			continue
		}
		out.Words = append(out.Words, word)
	}
	return out
}

// canonicalWord stems and de-synonymises one token.
func canonicalWord(raw string) string {
	word := strings.ToLower(strings.TrimSpace(raw))
	if to, ok := wordSynonyms[word]; ok {
		return to
	}
	// A bare plural, after the table has had its say: "holders" becomes
	// "holder" without anybody listing it. Only past four characters, so
	// "has" does not become "ha".
	if len(word) > 4 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
		stem := strings.TrimSuffix(word, "s")
		if to, ok := wordSynonyms[stem]; ok {
			return to
		}
		return stem
	}
	return word
}

// scoreMatch rates one variable against one property key.
//
// ok is false when the pairing must not be offered at all - a REFUSAL and not
// a low score, because what is being prevented is a confident wrong answer
// rather than a missing one.
func scoreMatch(variable, key fieldWords) (score float64, strength matchStrength, ok bool) {
	if len(variable.Words) == 0 || len(key.Words) == 0 {
		return 0, "", false
	}
	// THE ONE HARD RULE. Two names that disagree about which one they are
	// would render the plank with them swapped - successfully, so nothing
	// downstream can tell. Both sides must be silent about position, or agree.
	if variable.Ordinal != 0 && key.Ordinal != 0 && variable.Ordinal != key.Ordinal {
		return 0, "", false
	}

	have := make(map[string]bool, len(key.Words))
	for _, w := range key.Words {
		have[w] = true
	}
	shared := 0
	for _, w := range variable.Words {
		if have[w] {
			shared++
		}
	}
	if shared == 0 {
		return 0, "", false
	}

	// Measured against the variable's own words, since that is the side with
	// a fixed meaning: the key is a sentence the storefront wrote and carries
	// words about the form as well as about the field.
	score = float64(shared) / float64(len(variable.Words))
	// An agreed position is real evidence and is what breaks the tie between
	// two names, so it is worth more than one shared word.
	if variable.Ordinal != 0 && variable.Ordinal == key.Ordinal {
		score += 1
	}
	// A key that says nothing beyond what matched is a tighter fit than one
	// that buried the match among six other words.
	score += 1 / float64(1+len(key.Words)-shared)

	strength = strengthPossible
	if shared == len(variable.Words) && (variable.Ordinal == 0 || variable.Ordinal == key.Ordinal) {
		strength = strengthLikely
	}
	return score, strength, true
}
