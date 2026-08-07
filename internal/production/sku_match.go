package production

import "github.com/google/uuid"

// DesignFacts is what a matched approved design contributes to a job: the
// print settings and STL the job-creation worker snapshots onto
// production_jobs, so a later edit to the design or its printer profile never
// silently changes an already-created job.
type DesignFacts struct {
	DesignID      uuid.UUID
	Material      string
	Colours       []string
	PrintFileID   *uuid.UUID
	PrintTimeMin  *int
	FilamentGrams *float64
	LeftNozzleMM  *float64
	RightNozzleMM *float64
	FlowPct       *float64
	QualityMM     *float64
	MachineFamily string
	// Geometry/slice snapshot (see 0030_job_geometry_snapshot.sql).
	// BboxXMM/YMM/ZMM and ColourCount are per-unit facts, never scaled by
	// quantity. SupportWeightG/PurgeWeightG are per-unit grams as measured -
	// applyMatch scales them by quantity, matching FilamentGrams' convention.
	BboxXMM, BboxYMM, BboxZMM    *float64
	SupportWeightG, PurgeWeightG *float64
	ColourCount                  *int
	// SupportUsed/InfillPct already exist as production_jobs columns and are
	// already read by the planner's compatibility grouping (groupKey), but
	// were never actually populated here - every job in the system had the
	// same false/0 value on both axes. Fixed alongside the rest of this
	// snapshot since it's the same source data (GetLatestMetricsForDesign).
	SupportUsed *bool
	InfillPct   *float64
}

// MatchResult is what SKU-matching a line item against the design catalog
// produces: either a matched design's facts, or a reason (from the Issue*
// taxonomy) the caller should record on the job instead of dropping it.
type MatchResult struct {
	Design *DesignFacts
	Reason string
}

// Matched reports whether a design was found.
func (m MatchResult) Matched() bool { return m.Design != nil }
