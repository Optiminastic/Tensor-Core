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

// RequiredParamsForTemplate is what a template stored under this key must
// declare, decided by which vocabulary Tensor will drive it in.
func RequiredParamsForTemplate(key string) []string {
	if key == templateJoinery {
		return JoineryTemplateParams
	}
	return RequiredTemplateParams
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
