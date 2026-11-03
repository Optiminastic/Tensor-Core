package httpapi

// Noticing that a mapping has stopped matching the orders.
//
// The readiness panel checks three things today: that a SKU has a file, that
// it has a mapping, and that the file declares the variables the mapping
// names. All three ask whether the configuration is internally consistent.
// None of them asks whether it still describes reality.
//
// It stops describing reality on its own, without anybody touching Tensor.
// The join between an order and a .scad variable is a label somebody typed
// into the storefront - and personalise.NormaliseKey absorbs case and
// punctuation, so "STEP 4-First Name-:" survives a theme edit, but a WORDING
// change does not. "First Name" becoming "Your First Name" is a different
// key, and what happens next is decided by one dropdown:
//
//   - REQUIRED: the order is held and the field is named. Loud, and correct.
//   - OPTIONAL: the variable is not passed, the template's own default stands,
//     and the model renders, slices and prints WITH NO NAME ON IT. OpenSCAD
//     exits 0. Nothing downstream can tell it from a customer who left the
//     box blank.
//
// That second case is the whole reason this file exists. The fix is cheap
// because both halves are already read for other purposes: the mapping, and
// the labels this product's own orders carry.
//
// REPORTED, NEVER ENFORCED. An unmatched key is not grounds to refuse a save
// or hold an order: the commonest reason for one is a product configured
// before its first sale, which is exactly the workflow this release is for.
// So it is a sentence on a panel, next to the person who can go and look.

import (
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// mappingDrift is what a product's mapping and its orders disagree about.
//
// Stale is split by what the mapping does when the answer is absent, because
// the two consequences are not comparable and one sentence covering both
// would have to describe the milder one. A required field holds the order; an
// optional one prints a blank plank.
type mappingDrift struct {
	// StaleRequired are mapped keys no recent line carried, on rows that hold
	// the order when unanswered. Every such order stops, visibly.
	StaleRequired []string
	// StaleOptional are the same on rows that let the order through. These
	// are the expensive ones: the model renders without the field and exits 0.
	StaleOptional []string
	// Unmapped are labels the customers are sending that nothing reads. Less
	// urgent - an unmapped answer is an answer nobody asked to use - but it
	// is how a new storefront field announces itself.
	Unmapped []string
	// Renamed pairs a stale key with the label that has probably replaced it.
	//
	// A REWORDED FIELD SHOWS UP TWICE: the old key stops being carried and a
	// new one starts, and the two halves are the same event. Pairing them is
	// what turns "something is wrong" into the fix - "first name" is now
	// "Your First Name", re-map it - and it costs nothing, because the words
	// that pair the two are the ones registry_field_match.go already scores.
	Renamed map[string]string
}

// Any reports whether there is anything to say.
func (d mappingDrift) Any() bool {
	return len(d.StaleRequired) > 0 || len(d.StaleOptional) > 0 || len(d.Unmapped) > 0
}

// driftOf compares one SKU's mapping against what its orders carry.
//
// PURE, so the wording below can be tested without a product or a database.
//
// An empty observed list yields NO drift at all, which is deliberate and is
// the difference between a useful check and a panel nobody reads. A product
// that has not sold yet has no evidence either way, and reporting every one
// of its mapped fields as stale would put a red line against every product
// somebody is in the middle of setting up.
func driftOf(maps []gen.ProductFieldMap, observed []observedPropertyResponse) mappingDrift {
	if len(observed) == 0 {
		return mappingDrift{}
	}

	carried := make(map[string]bool, len(observed))
	for _, p := range observed {
		carried[p.Key] = true
	}

	var out mappingDrift
	read := map[string]bool{}
	for _, m := range maps {
		key := strings.TrimSpace(m.PropertyKey)
		// A fixed row does not read the order at all, so it cannot drift
		// from it - OUT_X=200 is the product's size, not the customer's.
		if key == "" || deref(m.FixedValue) != "" {
			continue
		}
		read[key] = true
		if carried[key] {
			continue
		}
		if m.Required {
			out.StaleRequired = append(out.StaleRequired, key)
			continue
		}
		out.StaleOptional = append(out.StaleOptional, key)
	}
	var spare []observedPropertyResponse
	for _, p := range observed {
		if !read[p.Key] && !contactProperty(p.Label) {
			spare = append(spare, p)
		}
	}

	// Pair each stale key with the unanswered label that best explains it,
	// before the rest are reported as simply unmapped: a reworded field is
	// one event, and reporting its two halves separately would ask the reader
	// to join them up.
	out.Renamed = map[string]string{}
	claimed := map[string]bool{}
	for _, stale := range append(append([]string{}, out.StaleOptional...), out.StaleRequired...) {
		if label, key, ok := likelyRename(stale, spare, claimed); ok {
			out.Renamed[stale] = label
			claimed[key] = true
		}
	}
	for _, p := range spare {
		if !claimed[p.Key] {
			out.Unmapped = append(out.Unmapped, p.Label)
		}
	}
	return out
}

// likelyRename is the unanswered label that best accounts for a stale key.
//
// Scored with the same word rules that propose a mapping in the first place,
// so the two cannot disagree about whether "first name" and "your first name"
// are the same field. The ordinal rule carries over too, which is what stops
// a reworded "second name" being offered as the new wording of "first name" -
// re-mapping on that advice would swap the plank.
func likelyRename(
	stale string, spare []observedPropertyResponse, claimed map[string]bool,
) (label, key string, ok bool) {
	words := wordsOfPropertyKey(stale)
	best := 0.0
	for _, p := range spare {
		if claimed[p.Key] {
			continue
		}
		score, strength, matched := scoreMatch(words, wordsOfPropertyKey(p.Key))
		// LIKELY ONLY. A partial match is a guess, and a guess here reads as
		// an instruction to go and re-map a field - which is worse than
		// saying nothing, because acting on it is a wrong mapping somebody
		// made on Tensor's advice.
		if !matched || strength != strengthLikely || score <= best {
			continue
		}
		best, label, key, ok = score, p.Label, p.Key, true
	}
	return label, key, ok
}

// staleWording is the sentence the panel shows.
//
// It names the keys and the likeliest cause, because "a field is missing"
// sends somebody to the logs while "the storefront wording may have changed"
// sends them to the product page - which is where the fix actually is.
func staleWording(stale []string, renamed map[string]string, optional bool) string {
	it := itOrThem(len(stale))
	consequence := "so every order of " + it + " is held until somebody notices."
	if optional {
		// The expensive case, said in full. A panel that described this the
		// same way it describes a held order would be hiding the difference
		// that matters.
		consequence = "so the model renders WITHOUT " + it + " and the order is " +
			"never held - nothing downstream can tell it from a customer " +
			"who left the box blank."
	}

	// WHEN THE REPLACEMENT IS KNOWN, SAY IT. "The wording may have changed"
	// sends somebody to go and compare two lists; naming the new label is the
	// fix itself, and it is the commonest case by far - a field does not
	// usually stop being asked, it gets reworded.
	if now := renamedList(stale, renamed); now != "" {
		return "The mapped " + plural(len(stale), "field") + " " +
			quotedList(namedFew(stale)) + " stopped arriving, and customers " +
			"are now sending " + now + " instead. Re-map " + it +
			"; until then " + consequence
	}
	return "No recent order carried the mapped " +
		plural(len(stale), "field") + " " + quotedList(namedFew(stale)) +
		". The storefront wording may have changed, " + consequence
}

// renamedList is the new wording of each of these keys, when every one of
// them has one.
//
// ALL OR NOTHING. A sentence naming replacements for two of three fields
// would read as naming them for all three, and somebody would re-map the
// third to a label that is not there.
func renamedList(stale []string, renamed map[string]string) string {
	now := make([]string, 0, len(stale))
	for _, key := range stale {
		label, ok := renamed[key]
		if !ok {
			return ""
		}
		now = append(now, label)
	}
	return quotedList(namedFew(now))
}

// itOrThem keeps a sentence agreeing with itself.
//
// "Customers are sending fields nothing reads... Map it" is what the panel
// actually said on live data, and a panel that cannot write a sentence is one
// people stop reading.
func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// unmappedWording names answers the customers are sending that nothing reads.
//
// A NOTE and not a problem. The commonest reason is a field that is genuinely
// not for the model - a WhatsApp number, a gift message - and a panel that
// called those faults would train people to skip it.
//
// It found a real one on its first run: the Single Name Plank has a design
// file and NO mapping, so all fourteen of its SKUs render the same model
// while its orders carry "4 - Name on Plank" and "3 - Red Heart".
func unmappedWording(unmapped []string) string {
	them := itOrThem(len(unmapped))
	return "Customers are sending " + plural(len(unmapped), "field") +
		" nothing reads: " + quotedList(namedFew(unmapped)) +
		". Map " + them + " if the design should use " + them + "."
}

// quotedList joins names so a label with a space in it still reads as one
// thing - and every label in this shop has a space in it.
func quotedList(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		// namedFew's tail ("and 4 others") is a count, not a name, so it is
		// not quoted - quoting it would read as a field called that.
		if strings.HasPrefix(n, "and ") {
			out = append(out, n)
			continue
		}
		out = append(out, `"`+n+`"`)
	}
	return strings.Join(out, ", ")
}

// maxDriftNamed bounds how many keys one sentence lists.
//
// A product with eleven unmapped fields produces a sentence nobody finishes
// reading, and the first three carry the information: that there are some,
// and roughly what they look like.
const maxDriftNamed = 3

// namedFew is the first few, with a count for the rest.
func namedFew(all []string) []string {
	if len(all) <= maxDriftNamed {
		return all
	}
	out := append([]string{}, all[:maxDriftNamed]...)
	rest := len(all) - maxDriftNamed
	return append(out, "and "+strconv.Itoa(rest)+" "+plural(rest, "other"))
}

// noteDrift puts one SKU's drift on its readiness row.
//
// A STALE FIELD IS A PROBLEM AND AN UNMAPPED ONE IS A NOTE, and the split is
// the contract this panel states for itself. A stale field is already wrong -
// orders are being held or planks are printing blank - while an unmapped one
// is usually a field that was never for the model at all, like a gift message.
// A panel that called the second a fault would train people to skip both.
//
// Ready is not touched here. readinessOf settles it from Problems once every
// check has run, so a stale field clears it by being a Problem and setting it
// here as well would be a second rule to keep in step with that one.
func noteDrift(out *skuReadiness, drift mappingDrift) {
	if !drift.Any() {
		return
	}
	// The expensive case first, because a panel is read from the top and this
	// is the one that is already printing blank planks.
	if len(drift.StaleOptional) > 0 {
		out.Problems = append(out.Problems,
			staleWording(drift.StaleOptional, drift.Renamed, true))
	}
	if len(drift.StaleRequired) > 0 {
		out.Problems = append(out.Problems,
			staleWording(drift.StaleRequired, drift.Renamed, false))
	}
	if len(drift.Unmapped) > 0 {
		out.Notes = append(out.Notes, unmappedWording(drift.Unmapped))
	}
}
