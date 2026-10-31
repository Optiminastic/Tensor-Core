package personalise

import "strings"

// RequiredTemplateParams are the variables Tensor sets with -D on every render.
//
// OpenSCAD does not fail on an unknown -D: it accepts the assignment and the
// template simply never reads it. So a template missing NAME_L renders a plank
// with no name and exits 0, which is the worst kind of failure - a successful
// one. Checked wherever a template enters the system, while the person who can
// fix it is still present.
var RequiredTemplateParams = []string{"NAME_L", "NAME_R", "OUT_X", "OUT_Y", "OUT_Z", "PART"}

// JoineryTemplateParams is the same guarantee for the joinery template, which
// speaks its own vocabulary - see joineryArgs.
//
// There are two lists because there are two vocabularies, not because one
// template is special. Checking the joinery file against NAME_L/OUT_X would
// refuse the shop's own file the moment they tried to replace it from the
// Designs tab, and the message would name variables that file has never had.
var JoineryTemplateParams = []string{
	"name1", "name2", "side_hearts",
	"plank_length", "plank_width", "total_height",
	"PART",
}

// CharmTemplateParams is the charm file's vocabulary - see charmArgs.
//
// A THIRD list, for the same reason there is a second: this file speaks
// neither NAME_L nor side_hearts. It takes the two names, the finished size,
// the charm flag and the lettering colour.
//
// export_part IS THIS FILE'S PART, and that is the substance of this entry
// rather than a detail. The renderer runs a template once per coloured piece,
// and a file that does not read the piece it was asked for draws the whole
// model every time - the 3MF then holds two overlapping copies of the plank in
// two colours. It slices, it prints, and it is wrong. The guarantee is
// identical to PART's; only the word differs.
var CharmTemplateParams = []string{
	"name1", "name2", "add_charm", "text_color",
	"plank_length", "plank_width", "total_height",
	"export_part",
}

// AlwaysPassedTemplateParams is the only thing EVERY template is driven with,
// whatever vocabulary it speaks.
//
// Exactly one variable: PART. The renderer runs a template once per coloured
// piece and writes the results into one 3MF as separately coloured objects. A
// template that ignores PART renders the identical solid every time, so the
// model comes out doubled - the same geometry overlapping itself in two
// colours. It slices, it prints, and it is wrong.
var AlwaysPassedTemplateParams = []string{"PART"}

// plankTemplateKeys are the keys the DNP path drives, and the only keys whose
// files Tensor passes names and finished sizes to.
//
// Everything else is a REGISTRY product: its arguments are built from the
// product's own field mapping, so the renderer never sets NAME_L or OUT_X and
// has no business demanding them.
var plankTemplateKeys = []string{
	templateJoinery,
	templateTwoHeart, templateOneHeart, templateNoHeart, templateFrame,
}

// RequiredParamsForTemplate is what a template stored under this key must
// declare, decided by which vocabulary Tensor will drive it in.
//
// THREE vocabularies, not two. The joinery file speaks name1/side_hearts; the
// planks it replaced speak NAME_L/OUT_X; and a registry product speaks
// whatever its field mapping says, which this package cannot know and must not
// guess.
//
// Returning the plank's list for every unrecognised key was a real refusal,
// not a theoretical one: `cmd/loadtemplates` turned away the shop's own
// complete lotus-holder file for "does not declare NAME_L, NAME_R, OUT_X,
// OUT_Y, OUT_Z" - variables a lotus holder has never had and never will -
// while the same file uploaded cleanly through the Designs tab, which applied
// this rule correctly. Two gates on one door disagreeing about what is allowed
// through is worse than either gate alone.
func RequiredParamsForTemplate(key string) []string {
	if key == templateCharm {
		return CharmTemplateParams
	}
	if key == templateJoinery {
		return JoineryTemplateParams
	}
	for _, plank := range plankTemplateKeys {
		if key == plank {
			return RequiredTemplateParams
		}
	}
	return AlwaysPassedTemplateParams
}

// MissingTemplateParams names the required variables a template does not
// declare, for a template in the legacy NAME_L/OUT_X vocabulary.
//
// Prefer MissingParamsForTemplate, which knows which vocabulary applies. This
// stays for callers that have the source and no key.
func MissingTemplateParams(source []byte) []string {
	return missingFrom(source, RequiredTemplateParams)
}

// MissingParamsForTemplate names the required variables a template does not
// declare, checked against the vocabulary its key is driven in.
func MissingParamsForTemplate(key string, source []byte) []string {
	return missingFrom(source, RequiredParamsForTemplate(key))
}

func missingFrom(source []byte, required []string) []string {
	text := string(source)
	var missing []string
	for _, name := range required {
		if !declaresParam(text, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

func declaresParam(text, name string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, name) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, name))
		if strings.HasPrefix(rest, "=") {
			return true
		}
	}
	return false
}
