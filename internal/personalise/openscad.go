// Package personalise turns an order's personalisation text into a printable
// model, with no designer and no CAD application in the loop.
//
// The shop's masters are Fusion 360 files, which cannot be driven headlessly -
// so each personalisable product gets an OpenSCAD template here instead,
// authored from the product's own Fusion sketch. Rendering is then a process
// call: text in, STL out, which is what lets an order flow straight through to
// the slicer.
//
// It also replaces the manual Bambu Studio step. A Dual Name Plank's geometry
// grows with the text, and every order was being resized by hand to
// 200x50x40 with "uniform scale" unticked. The templates carry an OUT_X/Y/Z
// block that applies exactly that scaling at render time, so the STL comes out
// at the finished size and nothing has to open the slicer's GUI.
package personalise

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.scad
var templates embed.FS

// ErrNoTemplate means the product has no OpenSCAD template yet, so its model
// still has to be made by hand. Callers treat it as "not automatable", not as
// a failure to fix at runtime.
var ErrNoTemplate = errors.New("no personalisation template for this product")

// DefaultTimeout bounds one render.
//
// Measured, not guessed: a Dual Name Plank takes 22-25s on this hardware, and
// the cost is dominated by a CGAL intersection whose size grows with the letter
// count. 180s leaves room for a longer name on a slower machine while still
// catching a template bug - a runaway loop or an unreachable import - rather
// than letting it hold a worker indefinitely.
const DefaultTimeout = 180 * time.Second

// Renderer runs OpenSCAD. Safe for concurrent use: every render works in its
// own temporary directory and shares nothing but the configuration.
type Renderer struct {
	bin      string
	assetDir string
	timeout  time.Duration
}

// NewRenderer builds a Renderer. bin is the OpenSCAD executable (looked up on
// PATH when it is just a name); assetDir is where the products' own STL parts
// live - the hearts, frames and other pieces a template imports.
func NewRenderer(bin, assetDir string, timeout time.Duration) *Renderer {
	if bin == "" {
		bin = "openscad"
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Renderer{bin: bin, assetDir: assetDir, timeout: timeout}
}

// Available reports whether OpenSCAD can actually be run.
//
// Checked before enqueuing rather than at render time so a service without
// OpenSCAD installed says "not configured" once, instead of failing every job
// with an exec error that reads like a template problem.
func (r *Renderer) Available() bool {
	if filepath.IsAbs(r.bin) {
		// An absolute path is not on PATH by definition - LookPath would
		// reject a perfectly good binary. This is the normal case on Windows,
		// where OpenSCAD installs to Program Files and is never added to PATH.
		info, err := os.Stat(r.bin)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(r.bin)
	return err == nil
}

// AssetPath resolves one of the shop's STL parts inside the asset directory.
// Empty when no asset directory is configured, which templates read as "draw
// nothing" rather than failing.
func (r *Renderer) AssetPath(name string) string {
	if r.assetDir == "" || name == "" {
		return ""
	}
	return filepath.Join(r.assetDir, name)
}

// RenderSTL renders one template with the given parameters and returns the STL
// bytes. Parameters are passed as OpenSCAD literals: quote strings with
// Quote before putting them in the map.
func (r *Renderer) RenderSTL(ctx context.Context, template string, params map[string]string) ([]byte, error) {
	return r.render(ctx, template, params, "stl", nil)
}

// RenderPNG renders the same model as a picture, for showing an operator what
// the customer will receive before anything is printed. Same template and same
// parameters as RenderSTL, so the preview cannot drift from the file that goes
// to the printer.
func (r *Renderer) RenderPNG(ctx context.Context, template string, params map[string]string, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		width, height = 900, 600
	}
	return r.render(ctx, template, params, "png", []string{
		"--imgsize=" + strconv.Itoa(width) + "," + strconv.Itoa(height),
		"--viewall", "--autocenter",
		"--colorscheme=Tomorrow",
		// A shallow three-quarter view: raised text reads as raised, which a
		// straight-on view flattens away.
		"--camera=0,0,0,58,0,28,0",
	})
}

func (r *Renderer) render(
	ctx context.Context, template string, params map[string]string, ext string, extra []string,
) ([]byte, error) {
	source, err := templates.ReadFile("templates/" + template + ".scad")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoTemplate, template)
	}

	dir, err := os.MkdirTemp("", "personalise-*")
	if err != nil {
		return nil, fmt.Errorf("scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	scadPath := filepath.Join(dir, template+".scad")
	if err := os.WriteFile(scadPath, source, 0o600); err != nil {
		return nil, fmt.Errorf("write template: %w", err)
	}
	outPath := filepath.Join(dir, template+"."+ext)

	args := make([]string, 0, len(params)*2+len(extra)+4)
	args = append(args, "-o", outPath)
	args = append(args, extra...)
	for name, value := range params {
		args = append(args, "-D", name+"="+value)
	}
	args = append(args, scadPath)

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Checked before runErr, and the two context cases kept apart, matching
	// slicing.RunSlice. A killed subprocess reports "signal: killed", which
	// blames the template for what was actually a deadline - and a cancelled
	// parent (a shutdown) must not be reported as a render failure at all.
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("openscad timed out after %s rendering %s", r.timeout, template)
	}
	if err := runCtx.Err(); err != nil {
		return nil, fmt.Errorf("render aborted before completing: %w", err)
	}
	if runErr != nil {
		return nil, fmt.Errorf("openscad: %w: %s", runErr, lastLines(stderr.String()))
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered model: %w", err)
	}
	if len(out) == 0 {
		// OpenSCAD exits 0 on an empty model, which would otherwise reach the
		// printer as a job that prints nothing.
		return nil, errors.New("openscad produced an empty model")
	}
	return out, nil
}

// Quote renders a Go string as an OpenSCAD string literal.
func Quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// lastLines keeps the tail of OpenSCAD's output - the part naming the failure -
// out of a log line that would otherwise carry the whole render trace.
func lastLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ")
}
