package personalise

// Refusing to render a template this OpenSCAD cannot build.
//
// OpenSCAD 2021.01 has no textmetrics(), which is how a template measures a
// name so it can be fitted and centred. A template that needs it cannot work
// on that binary - and the Soulmate heart keychain, knowing this, tests
// `version()[0] <= 2021` and draws a placard reading "WRONG OPENSCAD" INSTEAD
// OF THE PRODUCT.
//
// That placard is a successful render as far as everything downstream is
// concerned. It measured 99.67 x 30.71 x 2.00mm, was stored as JOB-115684-2's
// print file, was accepted as the model for the job, and went into a batch
// queued for a printer - a sign reading "WRONG OPENSCAD" where a customer had
// ordered a keychain with their name on it. The two parts were byte-identical
// because the placard is emitted at the top level, so both of Tensor's colour
// passes returned the same solid.
//
// Nothing in Tensor could have caught it. The renderer exited 0, the output
// had geometry, and the bbox was plausible for a small part. The only signal
// was an echo on stderr, which is where OpenSCAD puts its chatter.
//
// So the version is checked BEFORE the render instead: a template that calls
// textmetrics() on a binary older than 2022 fails, loudly, and the job is held
// with a reason naming the version it found and the version it needs. A held
// job is visible on the floor; a placard is not.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
)

// firstTextmetricsVersion is the first OpenSCAD year that has textmetrics().
//
// 2021.01 is the last release without it, which is exactly the test the
// keychain template makes (`version()[0] <= 2021`). Kept the same on purpose:
// two different ideas of "too old" would be a template refusing to build on a
// binary Tensor thought was fine, which is the state this fixes.
const firstTextmetricsVersion = 2022

// versionCache holds the binary's version, read once per Renderer.
//
// Behind a pointer so WithTemplateLoader can keep copying the Renderer by
// value: a sync.Once inside the struct would make `clone := *r` a copied lock.
type versionCache struct {
	once  sync.Once
	major int
	err   error
}

// MajorVersion is the OpenSCAD year, e.g. 2021 for "OpenSCAD version 2021.01".
//
// Read by running the binary, because there is no other honest source: the
// configured path says nothing about what is behind it, and a deployment can
// change the binary without changing anything Tensor is told. Cached, since it
// cannot change while the process runs.
func (r *Renderer) MajorVersion(ctx context.Context) (int, error) {
	if r.version == nil {
		// A Renderer built before this existed, or by a test. Read it without
		// caching rather than panicking.
		return readMajorVersion(ctx, r.bin)
	}
	r.version.once.Do(func() {
		r.version.major, r.version.err = readMajorVersion(ctx, r.bin)
	})
	return r.version.major, r.version.err
}

// versionYear matches the year in "OpenSCAD version 2021.01" and in the dated
// snapshots ("OpenSCAD version 2026.09.12"), which is the build the production
// image pins.
var versionYear = regexp.MustCompile(`version\s+(\d{4})`)

func readMajorVersion(ctx context.Context, bin string) (int, error) {
	// --version goes to stderr on some builds and stdout on others, so both
	// are collected and searched together.
	cmd := exec.CommandContext(ctx, bin, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return 0, fmt.Errorf("ask %s for its version: %w", bin, err)
	}
	return parseMajorVersion(out.String())
}

// parseMajorVersion reads the year out of OpenSCAD's version banner.
func parseMajorVersion(banner string) (int, error) {
	m := versionYear.FindStringSubmatch(banner)
	if m == nil {
		return 0, fmt.Errorf("cannot read an OpenSCAD version from %q", firstLine(banner))
	}
	year, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("cannot read an OpenSCAD version from %q", firstLine(banner))
	}
	return year, nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}

// needsTextmetrics reports whether a template measures text.
//
// A source check rather than a list of template names: the point is to catch
// the NEXT template that needs it, including one the shop uploads without
// telling anybody, and the call is right there in the source.
//
// COMMENTS ARE STRIPPED FIRST, and that is not a nicety. Every plank template
// carries the line "OpenSCAD 2021.01 has no textmetrics(). Used to auto-fit
// SQUEEZE so wide..." - prose explaining why it does NOT use it, containing
// the exact spelling of the call. Searching the raw source refused every
// plank on this machine, which is the opposite of the intent: the planks have
// a hardcoded table of glyph widths precisely so they work without it.
func needsTextmetrics(source []byte) bool {
	return bytes.Contains(stripSCADComments(source), []byte("textmetrics("))
}

// stripSCADComments blanks // and /* */ comments, leaving the code.
//
// Replaces each comment with a space rather than removing it, so nothing on
// either side is accidentally joined into a token that was never written.
// Quoted strings are respected - a name containing "//" is not a comment - and
// OpenSCAD has no escape-aware string subtleties that matter here beyond the
// backslash.
func stripSCADComments(source []byte) []byte {
	out := make([]byte, 0, len(source))
	const (
		code = iota
		inString
		inLine
		inBlock
	)
	state := code
	for i := 0; i < len(source); i++ {
		c := source[i]
		switch state {
		case code:
			switch {
			case c == '"':
				state = inString
				out = append(out, c)
			case c == '/' && i+1 < len(source) && source[i+1] == '/':
				state = inLine
				i++
				out = append(out, ' ')
			case c == '/' && i+1 < len(source) && source[i+1] == '*':
				state = inBlock
				i++
				out = append(out, ' ')
			default:
				out = append(out, c)
			}
		case inString:
			out = append(out, c)
			switch c {
			case '\\':
				if i+1 < len(source) {
					i++
					out = append(out, source[i])
				}
			case '"':
				state = code
			}
		case inLine:
			if c == '\n' {
				state = code
				out = append(out, c)
			}
		case inBlock:
			if c == '*' && i+1 < len(source) && source[i+1] == '/' {
				i++
				state = code
				out = append(out, ' ')
			} else if c == '\n' {
				// Kept so line numbers in any later message still line up.
				out = append(out, c)
			}
		}
	}
	return out
}

// checkRendererVersion refuses a render the binary cannot do correctly.
//
// FAILS CLOSED when the version cannot be read, but only for a template that
// needs textmetrics - the common template is unaffected either way. The
// asymmetry is deliberate: a held job with a reason on it is a question
// somebody answers, and a placard shipped to a customer is not.
func (r *Renderer) checkRendererVersion(ctx context.Context, template string, source []byte) error {
	if !needsTextmetrics(source) {
		return nil
	}
	major, err := r.MajorVersion(ctx)
	if err != nil {
		return fmt.Errorf(
			"%s measures text with textmetrics(), which needs OpenSCAD %d or newer, "+
				"and the version of %s could not be determined: %w",
			template, firstTextmetricsVersion, r.bin, err)
	}
	if major < firstTextmetricsVersion {
		return fmt.Errorf(
			"%s measures text with textmetrics(), which OpenSCAD %d does not have - "+
				"it needs %d or newer. This renderer is %s. Rendering it on this "+
				"version produces the template's own \"wrong version\" placard "+
				"instead of the product",
			template, major, firstTextmetricsVersion, r.bin)
	}
	return nil
}
