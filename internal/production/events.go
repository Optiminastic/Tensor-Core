package production

// The job history vocabulary. Pure: these are the names and the rules about
// them, not the writing of them - internal/httpapi owns the I/O.
//
// Every event type is "<subject>.<past-tense-verb>". The prefix before the dot
// is the stage where one exists, which is what lets CountOpenIssuesForJobs pair
// an issue with the completion that resolves it without a separate mapping.

// Job-level events, with no single station.
const (
	EventJobCreated     = "job.created"
	EventJobBatched     = "job.batched"
	EventPrintStarted   = "print.started"
	EventPrintCompleted = "print.completed"
	EventPrintFailed    = "print.failed"
)

// Station events. Each station has a completion, a skip where skipping is a
// real decision, and an issue.
const (
	EventAssemblyCompleted = "assembly.completed"
	EventAssemblySkipped   = "assembly.skipped"
	EventAssemblyIssue     = "assembly.issue"

	EventFinishingCompleted = "finishing.completed"
	EventFinishingSkipped   = "finishing.skipped"
	EventFinishingIssue     = "finishing.issue"

	EventQcPassed = "qc.passed"
	EventQcFailed = "qc.failed"
	EventQcIssue  = "qc.issue"

	EventPackagingPacked = "packaging.packed"
)

// Reprint and dispatch.
const (
	EventReprintRequested = "reprint.requested"
	EventReprintCreated   = "reprint.created"
	EventDispatched       = "dispatch.dispatched"
)

// Stages. A stage is where an issue can be raised, and the axis a timeline
// groups by.
const (
	StageAssembly  = "assembly"
	StageFinishing = "finishing"
	StageQc        = "qc"
	StagePackaging = "packaging"
	StagePrint     = "print"
)

// issueStages is where an operator may raise an issue: the three stations where
// a human inspects a physical object. Packaging is deliberately absent - by
// then the product has passed QC and a problem there is a QC failure, not a
// packaging one.
var issueStages = set(StageAssembly, StageFinishing, StageQc)

// ValidIssueStage reports whether an issue may be raised at this stage.
func ValidIssueStage(s string) bool { return issueStages[s] }

// IssueEventType is the event type for an issue raised at a stage. Empty for a
// stage that does not accept issues, which the caller must treat as a rejection
// rather than writing an event with a blank type.
func IssueEventType(stage string) string {
	if !ValidIssueStage(stage) {
		return ""
	}
	return stage + ".issue"
}

// StationIssueReasons is the operator-facing taxonomy for a problem found at a
// station. Deliberately a third, separate list:
//
//   - issue_reason (IssueSKUMissing etc.) is the pre-print validation taxonomy.
//     It describes missing catalogue data, and setting it excludes a job from
//     batching - useless and actively harmful for a part someone is holding.
//   - failureReasons describes why a PRINT failed on the bed.
//
// This one describes a physical defect found after printing.
var StationIssueReasons = []string{
	"missing_part",
	"damaged_part",
	"poor_surface_finish",
	"wrong_colour",
	"wrong_personalisation",
	"dimension_out_of_spec",
	"cracked",
	"warped",
	"hardware_missing",
	"addon_faulty",
	"other",
}

var stationIssueReasons = set(StationIssueReasons...)

// ValidStationIssueReason validates an operator-reported issue reason.
func ValidStationIssueReason(s string) bool { return stationIssueReasons[s] }
