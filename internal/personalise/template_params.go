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

// MissingTemplateParams names the required variables a template does not declare.
func MissingTemplateParams(source []byte) []string {
	text := string(source)
	var missing []string
	for _, name := range RequiredTemplateParams {
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
