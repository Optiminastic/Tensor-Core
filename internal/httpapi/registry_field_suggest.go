package httpapi

// A mapping proposed for review, rather than authored from nothing.
//
// Configuring a product means answering "which of the customer's answers
// feeds which variable in this file", once per variable. Both halves are
// knowable without asking anybody: the variables are declared in the .scad
// and read back by personalise.DeclaredParams, and the answers are on this
// product's own orders. What was missing is the pairing, and the pairing is
// what somebody has been sitting down and typing for every new design.
//
// Two sources, in this order:
//
//  1. THE FILE, when its author annotated it - `// @from Name 1 @max 20`.
//     Authoritative, because the person who drew the file and built the
//     storefront field is the only one who knows they are the same thing.
//  2. THE WORDS, otherwise - registry_field_match.go pairs what is left by
//     what the two names say. A guess, offered as a guess.
//
// NOTHING HERE WRITES ANYTHING. The registry row stays the source of truth
// and a person still presses save on the mapping editor. That is not caution
// for its own sake: a mapping applied on upload would mean re-uploading a
// corrected drawing silently re-pointed a live product's fields, and the
// first sign of it would be a batch of planks with the names swapped.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// fieldSuggestionResponse is one proposed row of the mapping editor.
//
// Shaped like fieldMapWriteRequest's rows deliberately, so the editor fills
// its own form from this and the person edits what it filled - rather than
// reading a recommendation on one screen and retyping it on another.
type fieldSuggestionResponse struct {
	ScadVariable string `json:"scad_variable"`
	PropertyKey  string `json:"property_key,omitempty"`
	// PropertyLabel is the storefront's own wording for that key, which is
	// what a reviewer recognises. The key is what matches.
	PropertyLabel string `json:"property_label,omitempty"`
	ValueType     string `json:"value_type"`
	Optional      bool   `json:"optional"`
	FixedValue    string `json:"fixed_value,omitempty"`
	DefaultValue  string `json:"default_value,omitempty"`
	MaxRunes      *int32 `json:"max_runes,omitempty"`
	// Source is "file" when the design said so and "match" when Tensor paired
	// the words. The difference decides how hard the reviewer should look.
	Source string `json:"source"`
	// Strength is "stated", "likely" or "possible".
	Strength string `json:"strength"`
	// Why is the one sentence the reviewer needs: what matched, or what the
	// file declared. Never a score - a number here would read as precision
	// this does not have.
	Why string `json:"why"`
	// Sample is one real answer from a recent order, which settles "is this
	// the name or the phone number?" faster than any explanation.
	Sample string `json:"sample,omitempty"`
	// Note is the author's own comment on the variable, verbatim. It is the
	// only documentation these files have.
	Note string `json:"note,omitempty"`
}

// sourceFile and sourceMatch name where a suggestion came from.
const (
	sourceFile     = "file"
	sourceMatch    = "match"
	strengthStated = "stated"
)

// unmappableParams are variables no order can fill.
//
// PART and export_part are the renderer's own: it runs the file once per
// coloured piece and sets them itself, and a mapping that overwrote PART
// would draw the same piece for every pass - the 3MF then holds overlapping
// copies of the model in different colours. It slices, it prints, and it is
// wrong.
var unmappableParams = map[string]bool{
	"part": true, "export_part": true,
}

func (s *Server) registerRegistryFieldSuggest(g *gin.RouterGroup, read gin.HandlerFunc) {
	g.GET("/products/:code/field-suggestions", read, s.fieldSuggestions)
}

// fieldSuggestions proposes a mapping for one of a product's design files.
//
// Per SKU when the caller names one, because a product whose child SKUs carry
// their own .scad files has a different set of variables per SKU - which is
// the case this whole feature is for.
func (s *Server) fieldSuggestions(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	role := strings.TrimSpace(c.Query("role"))
	if role == "" {
		role = designRoleBody
	}

	key, found := s.templateForVariant(ctx, product.ID, strings.TrimSpace(c.Query("sku")), role)
	if !found {
		detail(c, http.StatusConflict,
			"This product has no design file for that part yet, so there are no "+
				"variables to map. Assign one on the Design & fields panel first.")
		return
	}
	if s.renderer == nil {
		detail(c, http.StatusServiceUnavailable, "The renderer is not configured.")
		return
	}
	source, err := s.renderer.Source(ctx, key)
	if err != nil || len(source) == 0 {
		detail(c, http.StatusConflict,
			"Could not read "+key+", so its variables are unknown.")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"template_key": key,
		"items": suggestFieldMaps(
			personalise.DeclaredParams(source),
			s.observedPropertiesFor(ctx, product),
			declaredLimit(source),
		),
	})
}

// suggestFieldMaps pairs a file's variables with a product's order fields.
//
// PURE, so the rules below are testable against the shop's real labels
// without a product, a template or a database - and they were written against
// them: "step 4-first name-", "first name on plank", "name-1", "left name"
// and "top name" are five live spellings of one field.
func suggestFieldMaps(
	params []personalise.Param, observed []observedPropertyResponse, declaredMax int,
) []fieldSuggestionResponse {
	mappable := make([]personalise.Param, 0, len(params))
	for _, p := range params {
		if mappableParam(p) {
			mappable = append(mappable, p)
		}
	}

	out := make([]fieldSuggestionResponse, 0, len(mappable))
	claimed := map[string]bool{}

	// THE FILE FIRST, and it claims its keys before any guess is scored. An
	// author who said NAME_R comes from "second name" must not then have
	// "second name" taken by a guess about NAME_L.
	stated := map[string]fieldSuggestionResponse{}
	for _, p := range mappable {
		a, ok := personalise.Annotate(p)
		if !ok {
			continue
		}
		row, emit := statedSuggestion(p, a, observed, declaredMax)
		if !emit {
			continue
		}
		stated[p.Name] = row
		if row.PropertyKey != "" {
			claimed[row.PropertyKey] = true
		}
	}

	guessed := bestGuesses(mappable, stated, observed, claimed, declaredMax)

	// File order, because the author grouped these variables and that
	// grouping is the only guidance anyone has about which of fifty matter.
	for _, p := range mappable {
		if row, ok := stated[p.Name]; ok {
			out = append(out, row)
			continue
		}
		if row, ok := guessed[p.Name]; ok {
			out = append(out, row)
		}
	}
	return out
}

// mappableParam reports whether an order could fill this variable at all.
func mappableParam(p personalise.Param) bool {
	name := strings.TrimSpace(p.Name)
	if name == "" || unmappableParams[strings.ToLower(name)] {
		return false
	}
	// MAX_CHARS is the file describing its own text box. It is read as a
	// limit by personalisation_field_limit.go, not filled from an order.
	if isDeclaredLimitVar(strings.ToUpper(name)) {
		return false
	}
	// A boolean, vector or expression. Reported by DeclaredParams as "other"
	// precisely because it could not be classified, and quoting it wrongly is
	// either a syntax error or a silent comparison failure - so it is left to
	// a person unless the file's author said what it is.
	if p.Type == personalise.ParamOther {
		_, annotated := personalise.Annotate(p)
		return annotated
	}
	return true
}

// statedSuggestion is a row the design file itself asked for.
func statedSuggestion(
	p personalise.Param, a personalise.Annotation,
	observed []observedPropertyResponse, declaredMax int,
) (fieldSuggestionResponse, bool) {
	row := fieldSuggestionResponse{
		ScadVariable: p.Name,
		Optional:     a.Optional,
		DefaultValue: a.Default,
		Source:       sourceFile,
		Strength:     strengthStated,
		Note:         p.Note,
	}
	switch {
	case a.Fixed:
		row.FixedValue = a.FixedValue
		if row.FixedValue == "" {
			// "@fixed" alone means the value already written in the file,
			// which is how OUT_X = 200 is stated once instead of twice.
			row.FixedValue = strings.Trim(p.Default, `"`)
		}
		row.ValueType = string(valueKindFor(p, a, ""))
		row.Why = p.Name + " carries " + row.FixedValue +
			", which the design states itself."
		return row, row.FixedValue != ""
	case a.From != "":
		row.PropertyKey = a.From
		row.PropertyLabel = a.FromRaw
		if seen := findObserved(observed, a.From); seen != nil {
			row.PropertyLabel = seen.Label
			row.Sample = seen.Sample
			row.Why = "The design names this field, and " +
				strconv.Itoa(seen.Orders) + " recent " +
				plural(seen.Orders, "line") + " carried it."
		} else {
			// Said by the file and never seen on an order. Offered anyway -
			// the author knows what they built - but said plainly, because
			// the other reading is a label that has since been reworded.
			row.Why = "The design names this field, but no recent order has " +
				"carried it. Check the wording still matches the storefront."
		}
		row.ValueType = string(valueKindFor(p, a, row.Sample))
		row.MaxRunes = limitFor(a.Max, declaredMax, row.ValueType)
		return row, true
	}
	return row, false
}

// bestGuesses pairs what the file did not state, one key to one variable.
//
// Scored across the whole product and assigned best-first rather than
// variable-by-variable: NAME_L and NAME_R both say "name", and taking the
// first adequate key for each in turn is how a plank ends up with its names
// swapped.
func bestGuesses(
	params []personalise.Param, stated map[string]fieldSuggestionResponse,
	observed []observedPropertyResponse, claimed map[string]bool, declaredMax int,
) map[string]fieldSuggestionResponse {
	type pairing struct {
		param    personalise.Param
		prop     observedPropertyResponse
		score    float64
		strength matchStrength
	}
	var pairs []pairing
	for _, p := range params {
		if _, done := stated[p.Name]; done {
			continue
		}
		words := wordsOfVariable(p.Name)
		for _, prop := range observed {
			if claimed[prop.Key] {
				continue
			}
			score, strength, ok := scoreMatch(words, wordsOfPropertyKey(prop.Key))
			// A PARTIAL MATCH IS NOT PROPOSED, only offered by the dropdown
			// as it always was.
			//
			// Found on live data, not reasoned out. The Single Name Plank's
			// file declares both side_hearts and heart_drop - "mm the filler
			// heart is grown and pushed down" - and against the label "3 -
			// Red Heart" the two scored IDENTICALLY, because each shares
			// exactly the word "heart". The winner was then whichever sorted
			// first by name, and heart_drop did: a heart COUNT proposed into
			// a millimetre geometry parameter, which renders, slices, prints
			// and is quietly the wrong shape.
			//
			// There is no scoring tweak that fixes that, because the two
			// really are equally good matches on the evidence available. So
			// half a match is treated as no match, and the cost is one pick
			// from a dropdown that is still right there.
			if !ok || strength != strengthLikely {
				continue
			}
			pairs = append(pairs, pairing{p, prop, score, strength})
		}
	}
	// Best first. Ties go to the field more customers answered, then by name,
	// so the same catalogue proposes the same mapping every time it is asked.
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		if pairs[i].prop.Orders != pairs[j].prop.Orders {
			return pairs[i].prop.Orders > pairs[j].prop.Orders
		}
		return pairs[i].param.Name < pairs[j].param.Name
	})

	// How many lines the commonest field reached, so a field the orders
	// disagree about can be proposed optional rather than job-holding.
	mostSeen := mostSeenOrders(observed)
	out := make(map[string]fieldSuggestionResponse, len(pairs))
	for _, pair := range pairs {
		if _, taken := out[pair.param.Name]; taken || claimed[pair.prop.Key] {
			continue
		}
		claimed[pair.prop.Key] = true
		// WHAT THE FILE SAID STILL COUNTS, even when it did not name the
		// field. A variable annotated `@max 20 @optional` and nothing else
		// has told us two of the three things a row needs; only the address
		// is being guessed, and dropping the rest would mean a limit the
		// author wrote in the file silently not reaching the form that
		// enforces it.
		said, _ := personalise.Annotate(pair.param)
		kind := valueKindFor(pair.param, said, pair.prop.Sample)
		out[pair.param.Name] = fieldSuggestionResponse{
			ScadVariable:  pair.param.Name,
			PropertyKey:   pair.prop.Key,
			PropertyLabel: pair.prop.Label,
			ValueType:     string(kind),
			Optional:      said.Optional || absentFromSomeOrders(pair.prop, mostSeen),
			DefaultValue:  said.Default,
			Source:        sourceMatch,
			Strength:      string(pair.strength),
			Sample:        pair.prop.Sample,
			Note:          pair.param.Note,
			MaxRunes:      limitFor(said.Max, declaredMax, string(kind)),
			Why: "Matched on wording: " + pair.prop.Label + " and " +
				pair.param.Name + ". " + strconv.Itoa(pair.prop.Orders) +
				" recent " + plural(pair.prop.Orders, "line") + " carried it.",
		}
	}
	return out
}

// valueKindFor decides how the answer reaches OpenSCAD.
//
// Quoting is not cosmetic: an unquoted string is a syntax error, and a quoted
// number is a string that silently fails every arithmetic comparison the
// script makes against it.
func valueKindFor(
	p personalise.Param, a personalise.Annotation, sample string,
) personalise.ValueKind {
	if a.KindStated {
		return a.Kind
	}
	if p.Type != personalise.ParamNumber {
		return personalise.ValueString
	}
	// A numeric variable whose real answers are not numbers. The storefront
	// does not sell "1"; it sells "1 RED HEART", and renames the option
	// freely - so a count is read out of the wording. Without this the heart
	// option can only be mapped as a string, which reaches the script as
	// side_hearts="1 RED HEART" and is not a number.
	if sample != "" {
		if _, err := strconv.ParseFloat(strings.TrimSpace(sample), 64); err != nil {
			if strings.ContainsFunc(sample, func(r rune) bool { return r >= '0' && r <= '9' }) {
				return personalise.ValueCount
			}
		}
	}
	return personalise.ValueNumber
}

// limitFor is the character limit to propose, the file's own declaration
// standing in when the variable states none.
//
// Only on text. A number has no text box to overflow, and the API refuses the
// pair.
func limitFor(stated, declared int, valueType string) *int32 {
	if valueType != string(personalise.ValueString) {
		return nil
	}
	n := stated
	if n == 0 {
		n = declared
	}
	if n < 1 || n > maxDeclaredLimit {
		return nil
	}
	limit := int32(n)
	return &limit
}

func findObserved(observed []observedPropertyResponse, key string) *observedPropertyResponse {
	for i := range observed {
		if observed[i].Key == key {
			return &observed[i]
		}
	}
	return nil
}

// mostSeenOrders is the highest line count any observed property reached.
//
// A stand-in for "how many lines we looked at", which nothing records
// directly: the commonest field on a product is on essentially every line of
// it, so the top of the tally is the denominator the others are judged
// against.
func mostSeenOrders(observed []observedPropertyResponse) int {
	most := 0
	for _, p := range observed {
		if p.Orders > most {
			most = p.Orders
		}
	}
	return most
}

// absentFromSomeOrders reports whether this field is one the storefront only
// sometimes sends.
//
// PROPOSED OPTIONAL WHEN IT IS. The Family Crossword Frame is why: it sells
// two to six names, so name_5 and name_6 are on a minority of its lines, and
// a mapping that held the job for them meant every order of four names failed
// with `this order carries no "name 5"` - a product that could only ever make
// the six-name version of itself.
//
// The evidence was already on the screen beside each field ("name_1 · 3
// orders", "name_5 · 1 order") and only a person was reading it. A field the
// orders disagree about is exactly the field whose absence must not stop a
// render, so the suggestion now says so and the operator can still overrule
// it with one click.
//
// Deliberately not applied to a field on EVERY line: a plank's name is on all
// of them, and a blank name is scrap rather than a smaller product.
func absentFromSomeOrders(prop observedPropertyResponse, mostSeen int) bool {
	return mostSeen > 0 && prop.Orders > 0 && prop.Orders < mostSeen
}
