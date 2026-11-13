package personalise

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RunSource renders a .scad source that is not a registry template, with
// extra files staged beside it, to a binary STL.
//
// For print files built on demand (the sized moon lamp): Tensor writes the
// textured skin as skin.stl and the shop's joint file cuts it. The manifold
// backend is asked for because these are large meshes and CSG on them with
// the default backend took minutes rather than seconds.
func (r *Renderer) RunSource(
	ctx context.Context, source []byte, files map[string][]byte, params map[string]string, timeout time.Duration,
	enable ...string,
) ([]byte, error) {
	dir, err := os.MkdirTemp("", "scad-*")
	if err != nil {
		return nil, fmt.Errorf("scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, data := range files {
		if filepath.Base(name) != name {
			return nil, fmt.Errorf("staged file %q must be a bare name", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return nil, fmt.Errorf("stage %s: %w", name, err)
		}
	}
	scadPath := filepath.Join(dir, "main.scad")
	if err := os.WriteFile(scadPath, source, 0o600); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}
	outPath := filepath.Join(dir, "out.stl")
	args := []string{"--backend", "manifold", "--export-format", "binstl", "-o", outPath}
	// enable names experimental features the file needs, e.g. "textmetrics".
	for _, feature := range enable {
		args = append(args, "--enable", feature)
	}
	for name, value := range params {
		args = append(args, "-D", name+"="+value)
	}
	args = append(args, scadPath)

	if timeout <= 0 {
		timeout = r.timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, r.bin, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("openscad timed out after %s", timeout)
	}
	if err := runCtx.Err(); err != nil {
		return nil, fmt.Errorf("render aborted before completing: %w", err)
	}
	if runErr != nil {
		return nil, fmt.Errorf("openscad failed: %v: %s", runErr, lastLines(stderr.String()))
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("openscad wrote no model: %s", lastLines(stderr.String()))
	}
	return out, nil
}
