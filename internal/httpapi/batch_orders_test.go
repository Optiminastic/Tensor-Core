package httpapi

import "testing"

// A reprint is numbered from a sequence, not after its order, so reading the
// order out of the job number tagged a hand-built bed "1000005" - a number
// belonging to no order, on a bed built to reprint somebody's plank.
func TestOrderTagPrefersTheOrdersOwnNumber(t *testing.T) {
	order := "T3DPS-115073"
	if got := orderTagFor(&order, "JOB-1000005"); got != "115073" {
		t.Errorf("orderTagFor = %q, want 115073 - the order the reprint belongs to", got)
	}
}

func TestOrderTagKeepsReadingTheJobNumberWhenThereIsNoOrder(t *testing.T) {
	if got := orderTagFor(nil, "JOB-114556"); got != "114556" {
		t.Errorf("orderTagFor = %q, want 114556", got)
	}
}

// An imported job is numbered after its order, so the tag is unchanged for
// every row that already existed.
func TestOrderTagIsUnchangedForAnImportedJob(t *testing.T) {
	order := "T3DPS-115251"
	if got := orderTagFor(&order, "JOB-115251"); got != "115251" {
		t.Errorf("orderTagFor = %q, want 115251", got)
	}
}

// A store numbering its orders some other way falls back rather than showing a
// fragment of a format this does not understand.
func TestOrderTagFallsBackOnAnUnreadableOrderNumber(t *testing.T) {
	for _, odd := range []string{"", "   ", "ORD/abc", "T3DPS-"} {
		if got := orderTagFor(&odd, "JOB-114556"); got != "114556" {
			t.Errorf("orderTagFor(%q) = %q, want the job number's 114556", odd, got)
		}
	}
}
