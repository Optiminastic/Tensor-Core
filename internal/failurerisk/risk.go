// Package failurerisk turns a design's slice metrics into an advisory print-
// reliability score (spec section 2, "Failure risk score"). It is pure: a weighted
// heuristic over support, wall thickness, overhang, and waste - no DB, no I/O. It
// does not change the cost verdict; it is a separate signal a designer/PL reads.
package failurerisk

// Inputs are the signals the score is derived from. OverhangBaselineMM2 comes from
// the orientation recommendation (0 when unknown); everything else from the slice.
type Inputs struct {
	FilamentG           float64
	SupportG            float64
	SupportUsed         bool
	WallLoops           int
	PurgeG              float64
	ColourChanges       int
	OverhangBaselineMM2 float64
}

// Assessment is the result: a 0-100 score, a band, and the human-readable factors
// that drove it.
type Assessment struct {
	Score   int      `json:"score"`
	Band    string   `json:"band"` // low | medium | high
	Factors []string `json:"factors"`
}

// Thresholds are exported so they read as the tuning knobs they are.
const (
	supportHeavyRatio = 0.30
	supportSomeRatio  = 0.15
	purgeHighRatio    = 0.15
	overhangLargeMM2  = 3000
	overhangModMM2    = 1000
	manyColourChanges = 4

	bandMediumAt = 25
	bandHighAt   = 50
)

// Assess computes the failure-risk score. Higher is riskier.
func Assess(in Inputs) Assessment {
	score := 0
	factors := []string{}
	add := func(points int, factor string) {
		score += points
		factors = append(factors, factor)
	}

	supportPoints(in, add)

	switch {
	case in.WallLoops <= 1:
		add(25, "Very thin walls - fragile, prone to cracking")
	case in.WallLoops == 2:
		add(10, "Thin walls - consider adding a wall loop")
	}

	switch {
	case in.OverhangBaselineMM2 >= overhangLargeMM2:
		add(20, "Large overhang area - reorient to reduce support")
	case in.OverhangBaselineMM2 >= overhangModMM2:
		add(10, "Moderate overhang area")
	}

	if in.ColourChanges >= manyColourChanges {
		add(8, "Many colour changes - waste and quality risk")
	}
	if in.FilamentG > 0 && in.PurgeG/in.FilamentG >= purgeHighRatio {
		add(6, "High purge/waste relative to part")
	}

	if score > 100 {
		score = 100
	}
	band := "low"
	if score >= bandHighAt {
		band = "high"
	} else if score >= bandMediumAt {
		band = "medium"
	}
	if len(factors) == 0 {
		factors = []string{"No significant risk factors detected"}
	}
	return Assessment{Score: score, Band: band, Factors: factors}
}

// supportPoints scores the support burden by its share of the part's filament.
func supportPoints(in Inputs, add func(int, string)) {
	if in.FilamentG > 0 {
		ratio := in.SupportG / in.FilamentG
		switch {
		case ratio >= supportHeavyRatio:
			add(30, "Heavy support material (>30% of part) - reorient or split")
		case ratio >= supportSomeRatio:
			add(18, "Significant support material")
		case in.SupportUsed && ratio > 0:
			add(8, "Some support required")
		}
		return
	}
	if in.SupportUsed {
		add(8, "Support required")
	}
}
