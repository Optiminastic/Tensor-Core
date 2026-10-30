package personalise

// Matching a variable by name, whatever case the file wrote it in.
//
// Tensor passes its arguments in the spelling IT uses - PART for the piece
// being drawn, and whatever the registry's field mapping says for the rest.
// The file decides what those are called, and the shop's files do not all
// agree: the Royal holder's printed plate declares PART and TEXT, while the
// assembled preview beside it declares part and text.
//
// OpenSCAD accepts -D for a variable it does not have and never reads it, so
// the mismatch is silent. A preview rendered that way drew the WHOLE model on
// every piece pass, because part kept its default "all" - four stacked copies
// of a diya holder, and the customer's name missing from all of them, under a
// countdown telling them this is exactly how it will be made.
//
// Nothing warns, because the check that exists for precisely this is
// case-insensitive: DeclaresAll lower-cases both sides, so a mapping written
// TEXT against a file declaring text passes validation and then does nothing.
// Rather than make that check fussier - which would refuse correct files over
// a capital letter - the two are brought into agreement here: if validation
// says these names mean the same variable, the render must pass the one the
// file will actually read.

import "strings"

// AlignToSource rewrites argument names into the spellings the file declares.
//
// A name the file declares in another case is rewritten to the file's own.
// One it declares exactly is left alone, and one it does not declare at all is
// passed as written: that argument does nothing either way, and dropping it
// would hide a mapping mistake that the registry's own checks are there to
// report.
//
// The source is the authority on spelling, so this cannot be driven from the
// registry: there is nothing for anyone to configure, and a product whose file
// renames a variable keeps working without anybody being told.
func AlignToSource(source []byte, params map[string]string) map[string]string {
	if len(params) == 0 {
		return params
	}
	declared := make(map[string]string, len(params))
	for _, p := range DeclaredParams(source) {
		name := strings.TrimSpace(p.Name)
		key := strings.ToLower(name)
		// FIRST DECLARATION WINS, matching DeclaredParams' own order. A file
		// declaring both PART and part is pathological; picking consistently
		// at least makes two renders of it agree with each other.
		if _, seen := declared[key]; !seen {
			declared[key] = name
		}
	}

	out := make(map[string]string, len(params))
	for name, value := range params {
		if spelling, ok := declared[strings.ToLower(strings.TrimSpace(name))]; ok {
			out[spelling] = value
			continue
		}
		out[name] = value
	}
	return out
}
