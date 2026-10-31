package personalise

// The charm, and why it changes which file renders a plank.
//
// The storefront sells a "Charm" alongside the two names - "Warm Embrace",
// "Ghost Lovers", "Heart Huggers" - and "No charm" for the plain plank. A plank
// WITH a charm is not the joinery plank plus an ornament: it is a different
// file, with its own geometry, which takes only the two names and a colour.
//
// So this is a TEMPLATE decision, not a parameter, and it sits beside the one
// the photo frame already makes in ForProduct.
//
// "No charm" MUST render exactly what it renders today. That is the whole
// safety property here: this code runs on every Dual Name Plank the shop
// sells, and the overwhelming majority of them have no charm at all.

import (
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// charmKeys are the labels the storefront has used for the charm option.
//
// Matched through lookupRaw, so an exact label wins before a phrase match - the
// same precedence every other option on this line uses.
var charmKeys = []string{"charm"}

// noCharm is the storefront's wording for "I do not want one".
//
// Compared case- and space-insensitively rather than byte for byte: the value
// arrives as the customer's chosen option text, and a storefront that
// re-capitalises its own option would otherwise send every plain plank down the
// charm path - which is the expensive direction to be wrong in.
const noCharm = "no charm"

// CharmFromProperties returns the charm a line asks for.
//
// The empty string means NO CHARM, and it means it for three different reasons
// that are deliberately collapsed here:
//
//   - the line carries no charm property at all (every plank sold before the
//     option existed, and every product that does not offer it)
//   - the option is present and set to "No charm"
//   - the option is present and blank
//
// All three are the plank the shop has always printed, so all three must take
// the untouched path. This is the opposite of heartsFromProperties, which
// distinguishes "not offered" from "unreadable" because a heart count changes
// the margins; a charm that cannot be read is simply not a charm.
func CharmFromProperties(props []production.LineProp) string {
	value, _ := lookupRaw(props, charmKeys...)
	return NormaliseCharm(value)
}

// NormaliseCharm reduces a charm option to "" (none) or the charm's own name.
//
// The NAME is returned rather than a bool because the operator needs to know
// WHICH charm to put in the box, even though every charm renders the same
// plank - the charm itself is not drawn by the file.
func NormaliseCharm(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(trimmed, noCharm) {
		return ""
	}
	return trimmed
}

// templateCharm is the shop's "DNP + Charm (with joinery)" file.
//
// A SEPARATE FILE rather than a flag on the heart-joinery one, because the two
// lay the plank out differently: this one reserves zone_charm = 40mm at the
// right-hand end for the charm's peg pair, so the letter run has 40mm less to
// live in and the names are fitted into what remains.
//
// It is also why maxCharmNameLetters is lower than the plank's twelve.
const templateCharm = "dnp_charm_joinery"

// maxCharmNameLetters is this file's own limit, stated in its header: "max 10
// letters, A-Z".
//
// TWO LETTERS SHORTER than the plain plank's twelve, for the reason above -
// the charm's 40mm is taken out of the letter run. A name Tensor accepted at
// eleven would be squeezed into a space the file cannot lay it out in, so the
// charm path checks against this instead of inheriting the plank's bound.
const maxCharmNameLetters = 10

// The charm file's own vocabulary for choosing a piece.
//
// It splits by export_part, NOT by PART, and names its two pieces plank_part
// and text_part rather than base and text. Both the variable AND the values
// differ from Tensor's, so AlignToSource cannot bridge this: that aligns
// SPELLING - PART against part - and these are different words.
//
// Getting it wrong is silent and expensive. OpenSCAD accepts -D for a variable
// a file never declares and simply never reads it, so passing PART here would
// leave export_part at its default "all" and BOTH colour passes would return
// the entire model - two stacked copies of the plank in one 3MF, each painted
// a different colour. That is the exact failure the Royal diya holder hit.
const (
	charmPartVariable = "export_part"
	charmPartPlank    = "plank_part"
	charmPartText     = "text_part"
)

// charmExportPart translates Tensor's piece name into the file's.
//
// PartAll maps to the file's own "all", which it treats as both colours.
func charmExportPart(part string) string {
	switch part {
	case PartBase:
		return charmPartPlank
	case PartText:
		return charmPartText
	default:
		return PartAll
	}
}

// PieceArg names the -D that tells a template WHICH piece to draw, and the
// value to give it.
//
// THE ONE PLACE THIS IS DECIDED. It exists because there were two: this
// package built "PART" for its own callers while httpapi's renderPlan built
// "PART" for the real render, and only one of them learned about the charm
// file. The result passed every test and doubled the model on the floor -
// both colour passes returned the whole plank, 8002 triangles each, stacked
// in one 3MF in two colours.
//
// Any template that names its piece selector something else belongs here, not
// in a second branch at the call site.
func PieceArg(template, part string) (name, value string) {
	if template == templateCharm {
		return charmPartVariable, charmExportPart(part)
	}
	return "PART", part
}

// charmColours are the seventeen names the file's text_color and plank_color
// accept, keyed by their upper-cased form so a lookup is case-insensitive.
//
// The file takes a NAME, not a hex - "Gold", "SkyBlue" - and an unrecognised
// one would render in OpenSCAD's default yellow rather than fail.
//
// This matters LESS than it looks: the printed 3MF takes its colour from the
// hex Tensor attaches to each mesh, not from the file, so a wrong name here
// cannot print the wrong colour. It is what the CUSTOMER'S PREVIEW is drawn
// from, and what the shop sees when it opens the file in OpenSCAD to check an
// order - both of which are worth getting right.
var charmColours = map[string]string{
	"GOLD": "Gold", "SILVER": "Silver", "WHITE": "White", "BLACK": "Black",
	"RED": "Red", "PINK": "Pink", "HOTPINK": "HotPink", "PURPLE": "Purple",
	"BLUE": "Blue", "SKYBLUE": "SkyBlue", "NAVY": "Navy", "GREEN": "Green",
	"LIME": "Lime", "YELLOW": "Yellow", "ORANGE": "Orange", "BROWN": "Brown",
	"GREY": "Grey", "GRAY": "Grey",
}

// charmColourAliases are the storefront's names for colours the file calls
// something else.
//
// Only one today: the shop sells BABY PINK and the file offers Pink and
// HotPink. Pink is the lighter of the two, so it is the baby pink.
//
// Deliberately a short list of real colours seen in orders rather than a
// general colour-name system - the shop sells ten plank colours and nine of
// them are already the file's own words.
var charmColourAliases = map[string]string{
	"BABY PINK": "Pink",
	"BABYPINK":  "Pink",
	"SKY BLUE":  "SkyBlue",
}

// CharmColourName turns the colour on an order into the file's wording.
//
// Returns "" when the colour is not one the file offers, which the caller
// reads as "leave the file's default alone" rather than as an error: an
// unrecognised colour must not fail a render whose printed output does not
// depend on this value at all.
func CharmColourName(colour string) string {
	trimmed := strings.TrimSpace(colour)
	if trimmed == "" {
		return ""
	}
	upper := strings.ToUpper(trimmed)
	if name, ok := charmColourAliases[upper]; ok {
		return name
	}
	// Spaces removed so "SKY BLUE" also reaches SKYBLUE without an alias, and
	// a future two-word colour the file spells as one word just works.
	if name, ok := charmColours[strings.ReplaceAll(upper, " ", "")]; ok {
		return name
	}
	return ""
}

// WantsLetteringColour reports whether a template states its lettering colour
// as a variable of its own, rather than taking it from the 3MF.
func WantsLetteringColour(template string) bool { return template == templateCharm }

// charmArgs renders Params as the charm file's -D parameters.
//
// add_charm is forced TRUE rather than passed through. The file can draw a
// plank without one, but Tensor only ever sends an order here when the line
// actually carries a charm - CharmFromProperties decided that - and a plain
// plank has its own file. Leaving it to a default would make the two decisions
// able to disagree.
func charmArgs(p Params, shape Shape) map[string]string {
	args := map[string]string{
		"name1":       Quote(p.NameLeft),
		"name2":       Quote(p.NameRight),
		"add_charm":   "true",
		"plank_color": Quote(BasePlankColour),
		// The finished size, stated rather than scaled to - the same three
		// names the heart-joinery file uses, and for the same reason.
		"plank_length": strconv.Itoa(shape.X),
		"plank_width":  strconv.Itoa(shape.Y),
		"total_height": strconv.Itoa(shape.Z),
	}
	if name := CharmColourName(p.TextColour); name != "" {
		args["text_color"] = Quote(name)
	}
	return args
}

// BasePlankColour is the plank body's colour in the charm file's wording.
//
// White on every Dual Name Plank regardless of what the customer chose - the
// same rule BasePlateColour states as a hex for the 3MF. Stated twice because
// the two are read by different things, and kept beside the args that use it.
const BasePlankColour = "White"
