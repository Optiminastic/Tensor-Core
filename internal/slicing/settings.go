package slicing

// SliceSettings are the advanced, user-tunable overrides applied on top of the
// resolved H2S profile. Every field is optional: an absent field leaves the
// profile default in place.
//
// This type is the security boundary for CLI overrides. Values are clamped and
// allowlisted by Normalize before Flags turns them into "--key=value" arguments,
// so a client can never inject an arbitrary Bambu Studio flag - only these keys,
// with validated values, ever reach the command line.

import (
	"fmt"
	"strings"
)

type SliceSettings struct {
	LayerHeightMM *float64 `json:"layer_height_mm,omitempty"`
	WallLoops     *int     `json:"wall_loops,omitempty"`
	InfillPattern *string  `json:"infill_pattern,omitempty"`
	Support       *bool    `json:"support,omitempty"`
	SupportDeg    *int     `json:"support_threshold_deg,omitempty"`
}

// Bounds for the Bambu H2S 0.4mm nozzle. Layer height is capped at 70% of the
// nozzle diameter; a floor keeps slice times sane.
const (
	minLayerHeightMM = 0.08
	maxLayerHeightMM = 0.28
	minWallLoops     = 1
	maxWallLoops     = 8
	minSupportDeg    = 0
	maxSupportDeg    = 90
)

// infillPatterns is the allowlist of accepted Bambu sparse_infill_pattern values.
var infillPatterns = map[string]bool{
	"grid": true, "gyroid": true, "honeycomb": true, "cubic": true,
	"triangles": true, "line": true, "concentric": true, "tri-hexagon": true,
	"lightning": true,
}

// Normalize clamps numeric fields into range and drops an out-of-set pattern,
// returning a cleaned copy that is safe to turn into flags.
func (s SliceSettings) Normalize() SliceSettings {
	out := SliceSettings{Support: s.Support}
	if s.LayerHeightMM != nil {
		v := clampF(*s.LayerHeightMM, minLayerHeightMM, maxLayerHeightMM)
		out.LayerHeightMM = &v
	}
	if s.WallLoops != nil {
		v := clampI(*s.WallLoops, minWallLoops, maxWallLoops)
		out.WallLoops = &v
	}
	if s.InfillPattern != nil {
		if p := strings.ToLower(strings.TrimSpace(*s.InfillPattern)); infillPatterns[p] {
			out.InfillPattern = &p
		}
	}
	if s.SupportDeg != nil {
		v := clampI(*s.SupportDeg, minSupportDeg, maxSupportDeg)
		out.SupportDeg = &v
	}
	return out
}

// Flags renders the settings as Bambu CLI overrides. The enable-support flag is
// emitted by RunSlice (it defaults on); everything else comes from here. Only the
// allowlisted keys above can ever appear.
func (s SliceSettings) Flags() []string {
	var flags []string
	if s.LayerHeightMM != nil {
		flags = append(flags, fmt.Sprintf("--layer-height=%g", *s.LayerHeightMM))
	}
	if s.WallLoops != nil {
		flags = append(flags, fmt.Sprintf("--wall-loops=%d", *s.WallLoops))
	}
	if s.InfillPattern != nil {
		flags = append(flags, "--sparse-infill-pattern="+*s.InfillPattern)
	}
	if s.SupportDeg != nil {
		flags = append(flags, fmt.Sprintf("--support-threshold-angle=%d", *s.SupportDeg))
	}
	return flags
}

// SupportEnabled reports whether auto-support should run: on by default, off only
// when the caller explicitly turned it off.
func (s SliceSettings) SupportEnabled() bool {
	return s.Support == nil || *s.Support
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
