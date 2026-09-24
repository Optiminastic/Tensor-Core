// Command scadcompare measures what a change of OpenSCAD does to the planks.
//
// The templates are written FOR OpenSCAD 2021.01: each carries a hardcoded
// W_TBL of glyph widths, measured by extruding every letter and reading its
// bounding box, "because OpenSCAD 2021.01 has no textmetrics()". Moving to a
// newer renderer therefore changes the font engine underneath a table that was
// measured against the old one, and the models it feeds already print
// correctly. Whether that matters is a question about millimetres, so it is
// answered in millimetres rather than argued about.
//
// It renders, it does not compare: each image carries one OpenSCAD, so the run
// emits JSON for the renderer it has and the two files are diffed afterwards.
// That also means the two sides are measured by identical code.
//
//	docker run --rm tensor-production-worker:latest   scadcompare > old.json
//	docker run --rm tensor-production-worker:scad2026 scadcompare > new.json
//	scadcompare -diff old.json,new.json
//
// Not a test: it needs two OpenSCAD installations, takes minutes, and answers a
// one-off question. When the question is settled this command should go.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// sample is one plank to render. Named so a bad result says which case broke.
type sample struct {
	Why       string `json:"why"`
	Template  string `json:"template"`
	NameLeft  string `json:"name_left"`
	NameRight string `json:"name_right"`
}

// measurement is what one render produced, in the only terms that matter: how
// many triangles, and the box the geometry occupies.
type measurement struct {
	Sample sample `json:"sample"`
	Part   string `json:"part"`
	// Err is set instead of the rest when the render failed. A refused render
	// is a result, not a reason to abandon the run.
	Err       string     `json:"err,omitempty"`
	Triangles int        `json:"triangles,omitempty"`
	Min       [3]float64 `json:"min,omitempty"`
	Max       [3]float64 `json:"max,omitempty"`
}

type report struct {
	OpenSCADBin  string        `json:"openscad_bin"`
	Version      string        `json:"version"`
	Measurements []measurement `json:"measurements"`
}

func main() {
	diff := flag.String("diff", "", "compare two reports: old.json,new.json")
	only := flag.String("template", "", "restrict to one template key")
	timeout := flag.Duration("timeout", 4*time.Minute, "per-render timeout")
	flag.Parse()

	if *diff != "" {
		if err := runDiff(*diff); err != nil {
			fmt.Fprintln(os.Stderr, "diff:", err)
			os.Exit(1)
		}
		return
	}
	if err := runRender(*only, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
}

// templateKeys are the four .scad files, named here rather than exported from
// personalise: this command is throwaway, and the plan for it is explicit that
// internal/ does not change to accommodate it. They are embedded template
// filenames, so a wrong one fails loudly on its first render.
var templateKeys = []string{
	"dnp_two_heart",
	"dual_one_heart",
	"dnp_with_no_heart",
	"dnpf_without_heart",
}

// samples are the names worth rendering.
//
// Extremes rather than a random draw: the W_TBL entries this exercises are the
// ones furthest apart ("W" 1.48 against "I" 0.30), and a drift that shows
// anywhere shows there first. The long names matter for a second reason - they
// are what trips needsWideMargins, and a name that only just fitted at 2021.01
// is exactly the one an upgrade would push over the edge.
func samples(templates []string) []sample {
	pairs := []struct{ why, l, r string }{
		{"widest glyphs", "WWWWWW", "MMMMMM"},
		{"narrowest glyphs", "IIIIII", "JJJJJJ"},
		{"mixed widths", "WILLIAM", "ANNA"},
		{"short", "JO", "AL"},
		{"single letter", "A", "Z"},
		{"long, trips wide margins", "CHRISTOPHER", "ALEXANDRIA"},
		{"longest realistic", "MASSIMILIANO", "WILHELMINA"},
		{"descenders", "GregoryJ", "Jaqueline"},
		{"repeated narrow", "LILLIE", "TILLIE"},
		{"ordinary pair", "PRIYA", "ROHIT"},
	}
	out := make([]sample, 0, len(pairs)*len(templates))
	for _, t := range templates {
		for _, p := range pairs {
			out = append(out, sample{Why: p.why, Template: t, NameLeft: p.l, NameRight: p.r})
		}
	}
	return out
}

func runRender(only string, timeout time.Duration) error {
	bin := os.Getenv("OPENSCAD_BIN")
	r := personalise.NewRenderer(bin, os.Getenv("OPENSCAD_ASSET_DIR"), timeout)
	if !r.Available() {
		return fmt.Errorf("no OpenSCAD at %q; set OPENSCAD_BIN", bin)
	}

	keys := templateKeys
	if only != "" {
		keys = []string{only}
	}
	rep := report{OpenSCADBin: bin, Version: openscadVersion(bin)}

	ctx := context.Background()
	for _, s := range samples(keys) {
		// Exactly the parameters production builds, through the same helpers -
		// a harness that assembled its own -D flags would be measuring
		// something the worker never renders.
		p := personalise.Params{Template: s.Template, NameLeft: s.NameLeft, NameRight: s.NameRight}
		for _, part := range []string{personalise.PartBase, personalise.PartText} {
			m := measurement{Sample: s, Part: part}
			stl, err := r.RenderSTL(ctx, s.Template, p.ArgsForPart(part))
			if err != nil {
				m.Err = err.Error()
			} else if err := measure(&m, stl); err != nil {
				m.Err = err.Error()
			}
			rep.Measurements = append(rep.Measurements, m)
			fmt.Fprintf(os.Stderr, "%-24s %-6s %-12s %-12s %s\n",
				s.Template, part, s.NameLeft, s.NameRight, statusOf(m))
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// measure fills in the geometry, via the loader the pipeline itself uses so a
// parsing quirk cannot show up as a renderer difference.
func measure(m *measurement, stl []byte) error {
	dir, err := os.MkdirTemp("", "scadcompare-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "part.stl")
	if err := os.WriteFile(path, stl, 0o600); err != nil {
		return err
	}
	mesh, err := orientation.LoadModel(path, ".stl")
	if err != nil {
		return err
	}
	if len(mesh.Triangles) == 0 {
		return fmt.Errorf("no triangles")
	}
	// Mesh carries its own bounds, computed by the same loader the pipeline
	// uses. Recomputing them here would be a second implementation to disagree.
	m.Triangles = len(mesh.Triangles)
	m.Min = [3]float64{mesh.Min.X, mesh.Min.Y, mesh.Min.Z}
	m.Max = [3]float64{mesh.Max.X, mesh.Max.Y, mesh.Max.Z}
	return nil
}

func statusOf(m measurement) string {
	if m.Err != "" {
		return "FAILED: " + m.Err
	}
	return fmt.Sprintf("%d tris", m.Triangles)
}

func openscadVersion(bin string) string {
	if bin == "" {
		bin = "openscad"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// CombinedOutput: OpenSCAD prints its version on stderr.
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "unknown"
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// --- diff -------------------------------------------------------------------

type delta struct {
	key      string
	part     string
	why      string
	triOld   int
	triNew   int
	maxShift float64
	note     string
}

func runDiff(arg string) error {
	paths := strings.SplitN(arg, ",", 2)
	if len(paths) != 2 {
		return fmt.Errorf("want two paths separated by a comma")
	}
	oldRep, err := readReport(paths[0])
	if err != nil {
		return err
	}
	newRep, err := readReport(paths[1])
	if err != nil {
		return err
	}

	index := make(map[string]measurement, len(newRep.Measurements))
	for _, m := range newRep.Measurements {
		index[keyOf(m)] = m
	}

	deltas := make([]delta, 0, len(oldRep.Measurements))
	var broke int
	for _, o := range oldRep.Measurements {
		n, ok := index[keyOf(o)]
		if !ok {
			continue
		}
		d := delta{key: keyOf(o), part: o.Part, why: o.Sample.Why,
			triOld: o.Triangles, triNew: n.Triangles}
		switch {
		case o.Err != "" || n.Err != "":
			d.note = "render failed"
			broke++
		default:
			d.maxShift = maxShift(o, n)
		}
		deltas = append(deltas, d)
	}
	// Worst first: this is read to find the case that breaks, not to browse.
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].maxShift > deltas[j].maxShift })

	fmt.Printf("old: %s\nnew: %s\n\n", oldRep.Version, newRep.Version)
	fmt.Printf("%-52s %-6s %8s %8s %12s  %s\n", "CASE", "PART", "TRIS OLD", "TRIS NEW", "MAX SHIFT", "")
	var worst float64
	for _, d := range deltas {
		if d.maxShift > worst {
			worst = d.maxShift
		}
		fmt.Printf("%-52s %-6s %8d %8d %9.4f mm  %s\n",
			d.key, d.part, d.triOld, d.triNew, d.maxShift, d.note)
	}
	fmt.Printf("\ncompared %d renders; worst bounding-box shift %.4f mm; %d failed\n",
		len(deltas), worst, broke)
	// The number the decision rests on. Said out loud rather than left for the
	// reader to work out from a table they have to scroll.
	if worst == 0 && broke == 0 {
		fmt.Println("IDENTICAL - the renderers agree on every sample.")
	}
	return nil
}

// keyOf identifies one render. The PART belongs in it: every sample is
// rendered twice, and without it the two collapse onto one key, the second
// overwrites the first, and the diff silently compares a base plate against
// lettering. That reported a 107mm shift when a report was compared with
// itself - which is the only reason the self-check exists.
func keyOf(m measurement) string {
	return fmt.Sprintf("%s/%s+%s/%s",
		m.Sample.Template, m.Sample.NameLeft, m.Sample.NameRight, m.Part)
}

// maxShift is the largest movement of any face of the bounding box, which is
// what "the letters moved" looks like in a number.
func maxShift(a, b measurement) float64 {
	var worst float64
	for i := 0; i < 3; i++ {
		for _, d := range []float64{a.Min[i] - b.Min[i], a.Max[i] - b.Max[i]} {
			if d < 0 {
				d = -d
			}
			if d > worst {
				worst = d
			}
		}
	}
	return worst
}

func readReport(path string) (report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	var r report
	return r, json.Unmarshal(data, &r)
}
