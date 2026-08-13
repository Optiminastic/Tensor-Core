package slicing

// Runs Bambu Studio headless (its AppRun under xvfb) to slice one model. Ported
// from the Python worker's slicer.py. To batch, the model is loaded units_per_bed
// times and auto-arranged; the caller divides the plate totals per unit.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	exportName = "out.gcode.3mf"
	resultName = "result.json"
)

// SliceOutput is the pair of files a successful slice leaves in outdir.
type SliceOutput struct {
	ResultJSONPath string
	Gcode3mfPath   string
}

// RunSlice slices stlPath with the caller's resolved profiles into outdir. It enables
// auto-support and applies the infill override, mirroring the Python invocation.
// It fails with a clear reason when the slicer crashes or exports no G-code,
// rather than letting the caller trip over a missing file.
func RunSlice(
	ctx context.Context, bambuRoot string, profiles ResolvedProfiles,
	stlPath string, infillPct float64, unitsPerBed int, outdir string, timeout time.Duration,
) (SliceOutput, error) {
	copies := unitsPerBed
	if copies < 1 {
		copies = 1
	}
	appRun := filepath.Join(bambuRoot, "AppRun")
	args := []string{
		"-a", appRun,
		"--load-settings", profiles.MachinePath + ";" + profiles.ProcessPath,
		"--load-filaments", profiles.FilamentPath,
		"--arrange", "1",
		"--orient", "1",
		"--slice", "0",
		// Auto-support: Bambu adds support only where overhangs need it, so support
		// material is a real, costed metric instead of always off.
		"--enable-support=1",
		fmt.Sprintf("--sparse-infill-density=%g%%", infillPct),
		"--outputdir", outdir,
		"--export-3mf", exportName,
	}
	for i := 0; i < copies; i++ {
		args = append(args, stlPath)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "xvfb-run", args...)
	out, runErr := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return SliceOutput{}, fmt.Errorf("slice timed out after %s", timeout)
	}
	// A cancelled parent (River's job timeout, or a worker shutdown) kills the
	// subprocess before it can write anything. Reporting that honestly matters:
	// falling through to the os.Stat check below would blame the slicer for
	// "producing no result.json" when nothing was ever given the chance to run.
	if err := runCtx.Err(); err != nil {
		return SliceOutput{}, fmt.Errorf("slice aborted before completing: %w", err)
	}

	resultPath := filepath.Join(outdir, resultName)
	if _, err := os.Stat(resultPath); err != nil {
		return SliceOutput{}, fmt.Errorf("slicer produced no result.json (%v): %s", runErr, tail(string(out)))
	}
	// A run can write result.json but still fail to export the G-code (a bad slice,
	// an unsupported model). Surface the real reason.
	gcodePath := filepath.Join(outdir, exportName)
	if _, err := os.Stat(gcodePath); err != nil {
		reason := sliceErrorReason(resultPath)
		if reason == "" {
			reason = tail(string(out))
		}
		return SliceOutput{}, fmt.Errorf("slice produced no G-code: %s", reason)
	}
	return SliceOutput{ResultJSONPath: resultPath, Gcode3mfPath: gcodePath}, nil
}

// sliceErrorReason reads the slicer's own failure reason from result.json.
func sliceErrorReason(resultPath string) string {
	result, err := LoadResultJSON(resultPath)
	if err != nil {
		return ""
	}
	if result.ReturnCode == nil || *result.ReturnCode == 0 {
		return ""
	}
	msg := result.ErrorString
	if msg == "" {
		msg = "no message"
	}
	return fmt.Sprintf("return_code %d: %s", *result.ReturnCode, msg)
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const limit = 300
	if len(s) > limit {
		return s[len(s)-limit:]
	}
	if s == "" {
		return "no output"
	}
	return s
}
