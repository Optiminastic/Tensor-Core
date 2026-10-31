package retention

// The shop's day, and the rule that a sweep must never break.
//
// shopDay decides which hours a deleted day covered, and it has to agree with
// the sales register down to the hour - the register is what survives the
// deletion, and a summary describing different hours could never be checked
// against it.

import (
	"testing"
	"time"
)

func ist(t *testing.T) *time.Location {
	t.Helper()
	// Fixed rather than loaded from the system zone database, which a Windows
	// build may not have: the offset is what matters here, not the name.
	return time.FixedZone("IST", 5*3600+1800)
}

func TestTheDayRunsNoonToNoon(t *testing.T) {
	loc := ist(t)
	// 3pm on the 20th is inside the window that OPENED at noon that day.
	from, to := shopDay(time.Date(2026, 10, 20, 15, 0, 0, 0, loc), loc)

	if from.Hour() != 12 || to.Hour() != 12 {
		t.Errorf("window is %s to %s, want noon to noon", from, to)
	}
	if from.Day() != 20 || to.Day() != 21 {
		t.Errorf("window is %s to %s, want 20th noon to 21st noon", from, to)
	}
}

func TestBeforeNoonBelongsToTheDayThatStartedYesterday(t *testing.T) {
	loc := ist(t)
	// 9am on the 21st is still the trading day that began at noon on the
	// 20th - the floor works past midnight, which is the whole reason the
	// register does not use calendar days.
	from, to := shopDay(time.Date(2026, 10, 21, 9, 0, 0, 0, loc), loc)

	if from.Day() != 20 || to.Day() != 21 {
		t.Errorf("window is %s to %s, want the 20th noon to the 21st noon", from, to)
	}
}

func TestExactlyNoonOpensTheNewDay(t *testing.T) {
	loc := ist(t)
	// Half-open, matching accounts.Window: an order placed exactly at noon
	// belongs to the day just starting, so it can never be counted twice -
	// once in a summary and once in the register.
	from, _ := shopDay(time.Date(2026, 10, 21, 12, 0, 0, 0, loc), loc)
	if from.Day() != 21 {
		t.Errorf("noon resolved into the %d, want the day it opens", from.Day())
	}
}

func TestASweepWithNoRetentionPeriodIsRefused(t *testing.T) {
	// Zero would mean "keep nothing", which is a configuration mistake rather
	// than an instruction - and the one mistake here is unrecoverable.
	if _, err := Sweep(t.Context(), nil, nil, []string{"x"}, Options{Days: 0}); err == nil {
		t.Error("a sweep with Days=0 was accepted; it would delete everything")
	}
}

func TestTheWalkStartsFromARealTimestamp(t *testing.T) {
	// timestamptz maps a zero time to NULL, and `created_at >= NULL` is never
	// true - so a pass starting from the zero value would ask one question,
	// get no rows, and report "nothing to do" against a full backlog.
	//
	// Asserted on the helper rather than through a database, because the
	// failure is silent either way: the sweep returns no results and no
	// error, which looks exactly like success.
	if ts := timestamptz(time.Time{}); ts.Valid {
		t.Error("the zero time produced a valid timestamp; this test's premise is gone")
	}
	if ts := timestamptz(time.Unix(0, 0).UTC()); !ts.Valid {
		t.Error("the epoch did not produce a valid timestamp, so every pass asks for NULL")
	}
}
