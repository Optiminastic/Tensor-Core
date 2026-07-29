package orientation

import (
	"fmt"
	"math"
	"sort"
)

// Options tunes the orientation search.
type Options struct {
	// OverhangThresholdDeg is the self-support limit: faces angled downward more
	// than this (from horizontal) need support. 45 is a safe printer default.
	OverhangThresholdDeg float64
	// MaxCandidates caps how many resting orientations are scored.
	MaxCandidates int
	// MaxTriangles caps the mesh size scored; larger meshes are sampled down so
	// runtime stays bounded. 0 means no cap.
	MaxTriangles int
}

const (
	defaultOverhangDeg = 45.0
	defaultCandidates  = 24
	defaultMaxTris     = 500_000

	// A candidate normal within ~10deg of one already picked is a duplicate.
	candidateCosTol = 0.985
	// Below these, the model is treated as already well-oriented.
	minMeaningfulDeg = 5.0
	minReduction     = 0.05
)

func (o Options) withDefaults() Options {
	if o.OverhangThresholdDeg <= 0 {
		o.OverhangThresholdDeg = defaultOverhangDeg
	}
	if o.MaxCandidates <= 0 {
		o.MaxCandidates = defaultCandidates
	}
	if o.MaxTriangles <= 0 {
		o.MaxTriangles = defaultMaxTris
	}
	return o
}

// Recommendation is the orientation advice for one model. It is marshalled to
// JSON and stored on the slice metrics, then surfaced on the design detail page.
// EstReductionPct is a fraction (0..1), matching the codebase's cp_pct convention.
type Recommendation struct {
	RotationAxis            Vec3    `json:"rotation_axis"`
	RotationDegrees         float64 `json:"rotation_degrees"`
	OverhangAreaBaseline    float64 `json:"overhang_area_baseline"`
	OverhangAreaRecommended float64 `json:"overhang_area_recommended"`
	EstReductionPct         float64 `json:"est_reduction_pct"`
	Description             string  `json:"description"`
	AlreadyOptimal          bool    `json:"already_optimal"`
}

// Recommend searches resting orientations and returns the one with the least
// downward-overhang area. ok is false only for an empty mesh. It never changes
// the model; the result is advice for whoever positions the print.
func Recommend(m Mesh, opts Options) (Recommendation, bool) {
	if m.empty() {
		return Recommendation{}, false
	}
	opts = opts.withDefaults()

	scored := m
	if len(m.Triangles) > opts.MaxTriangles {
		scored = Mesh{Triangles: sampleTriangles(m.Triangles, opts.MaxTriangles), Min: m.Min, Max: m.Max}
	}

	negSinT := -math.Sin(opts.OverhangThresholdDeg * math.Pi / 180)
	down := Vec3{0, 0, -1} // the as-uploaded "down": current bottom stays down

	baseline := scoreOrientation(scored, down, negSinT)
	best, bestDir := baseline, down
	for _, c := range candidates(scored, opts.MaxCandidates) {
		if c == down {
			continue
		}
		if s := scoreOrientation(scored, c, negSinT); betterThan(s, best) {
			best, bestDir = s, c
		}
	}
	return buildRecommendation(baseline, best, bestDir), true
}

// scoreResult is one orientation's cost. overhang (to minimise) is the primary
// objective; contact area and build height break ties.
type scoreResult struct {
	overhang float64
	contact  float64
	height   float64
}

// scoreOrientation scores resting the model so direction c points straight down.
// Rotating c -> -Z makes a face normal's rotated z-component -(n.c) and a
// vertex's rotated height -(v.c), so no rotation matrix is needed. A downward
// face beyond the overhang threshold needs support unless it rests on the plate
// (its lowest point is at the build minimum), which the plate supports for free.
func scoreOrientation(m Mesh, c Vec3, negSinT float64) scoreResult {
	minH, maxH := math.Inf(1), math.Inf(-1)
	for _, t := range m.Triangles {
		for _, v := range [3]Vec3{t.V0, t.V1, t.V2} {
			h := -v.dot(c)
			minH, maxH = math.Min(minH, h), math.Max(maxH, h)
		}
	}
	eps := 1e-3*(maxH-minH) + 1e-9

	var overhang, contact float64
	for _, t := range m.Triangles {
		if -t.Normal.dot(c) >= negSinT { // not a downward overhang
			continue
		}
		faceMinH := math.Min(math.Min(-t.V0.dot(c), -t.V1.dot(c)), -t.V2.dot(c))
		if faceMinH <= minH+eps {
			contact += t.Area // resting on the plate; free
		} else {
			overhang += t.Area
		}
	}
	return scoreResult{overhang: overhang, contact: contact, height: maxH - minH}
}

// betterThan reports whether a is a better resting orientation than b: less
// overhang first, then more plate contact, then a shorter build.
func betterThan(a, b scoreResult) bool {
	tol := 1e-6 * (math.Abs(b.overhang) + 1)
	switch {
	case a.overhang < b.overhang-tol:
		return true
	case a.overhang > b.overhang+tol:
		return false
	case a.contact > b.contact+tol:
		return true
	case a.contact < b.contact-tol:
		return false
	default:
		return a.height < b.height
	}
}

// candidates are the directions worth testing as "down": the six axis
// directions plus the distinct large-face normals (a face resting flat on the
// plate is the usual support-minimising pose).
func candidates(m Mesh, maxCandidates int) []Vec3 {
	cands := []Vec3{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}}

	idx := make([]int, len(m.Triangles))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool {
		return m.Triangles[idx[i]].Area > m.Triangles[idx[j]].Area
	})

	for _, i := range idx {
		if len(cands) >= maxCandidates {
			break
		}
		n := m.Triangles[i].Normal
		if n == (Vec3{}) {
			continue
		}
		if !isDuplicateDir(n, cands) {
			cands = append(cands, n)
		}
	}
	return cands
}

func isDuplicateDir(n Vec3, existing []Vec3) bool {
	for _, c := range existing {
		if n.dot(c) > candidateCosTol {
			return true
		}
	}
	return false
}

func sampleTriangles(tris []Triangle, max int) []Triangle {
	step := len(tris)/max + 1
	out := make([]Triangle, 0, len(tris)/step+1)
	for i := 0; i < len(tris); i += step {
		out = append(out, tris[i])
	}
	return out
}

func buildRecommendation(baseline, best scoreResult, bestDir Vec3) Recommendation {
	reduction := 0.0
	if baseline.overhang > 1e-9 {
		reduction = (baseline.overhang - best.overhang) / baseline.overhang
	}
	axis, deg := rotationTo(bestDir)

	rec := Recommendation{
		RotationAxis:            axis,
		RotationDegrees:         round2(deg),
		OverhangAreaBaseline:    round2(baseline.overhang),
		OverhangAreaRecommended: round2(best.overhang),
		EstReductionPct:         reduction,
	}
	if deg < minMeaningfulDeg || reduction < minReduction || baseline.overhang <= 1e-9 {
		rec.AlreadyOptimal = true
		rec.RotationAxis, rec.RotationDegrees, rec.EstReductionPct = Vec3{}, 0, 0
	}
	rec.Description = describe(rec)
	return rec
}

// rotationTo returns the minimal rotation (axis + degrees) that turns direction
// c to point straight down (-Z), i.e. the motion to reach the recommended pose.
func rotationTo(c Vec3) (Vec3, float64) {
	target := Vec3{0, 0, -1}
	cu := c.normalize()
	d := clamp(cu.dot(target), -1, 1)
	axis := cu.cross(target)
	if axis.length() < 1e-9 { // parallel (already down) or anti-parallel (flip)
		if d > 0 {
			return Vec3{}, 0
		}
		return Vec3{1, 0, 0}, 180
	}
	return axis.normalize(), math.Acos(d) * 180 / math.Pi
}

func describe(rec Recommendation) string {
	if rec.AlreadyOptimal {
		return "This model is already well-oriented; no rotation is needed to keep supports low."
	}
	return fmt.Sprintf(
		"Rotate the model about the %s axis by ~%.0f%s so a flatter face rests on the plate.",
		nearestAxisName(rec.RotationAxis), rec.RotationDegrees, "°",
	)
}

// nearestAxisName names the principal axis closest to v, for a human-readable
// rotation instruction ("about the X axis").
func nearestAxisName(v Vec3) string {
	ax, ay, az := math.Abs(v.X), math.Abs(v.Y), math.Abs(v.Z)
	switch {
	case ax >= ay && ax >= az:
		return "X"
	case ay >= az:
		return "Y"
	default:
		return "Z"
	}
}

func clamp(x, lo, hi float64) float64 {
	return math.Min(math.Max(x, lo), hi)
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
