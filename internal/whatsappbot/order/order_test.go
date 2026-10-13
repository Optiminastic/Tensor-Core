package order

import (
	"testing"
	"time"
)

func ist(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, Location)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIsExpress(t *testing.T) {
	if !(Order{ShippingTitle: "EXPRESS DISPATCH ⚡️"}).IsExpress() {
		t.Error("express shipping line not detected")
	}
	if (Order{ShippingTitle: "FREE DISPATCH - BEST DEAL 🎉"}).IsExpress() {
		t.Error("free dispatch flagged as express")
	}
}

func TestAddWorkingDaysSkipsSundays(t *testing.T) {
	// Fri 2026-10-09 + 3 working days: Sat 10, (Sun 11 skipped), Mon 12, Tue 13.
	got := AddWorkingDays(ist(t, "2026-10-09 18:00"), 3)
	if got.Format("2006-01-02") != "2026-10-13" {
		t.Fatalf("got %s", got.Format("2006-01-02 Mon"))
	}
}

func TestExpectedDispatch(t *testing.T) {
	created := ist(t, "2026-10-12 10:00").UTC() // Monday
	standard := Order{CreatedAt: created}.ExpectedDispatch()
	if standard.Format("2006-01-02") != "2026-10-23" {
		t.Errorf("standard = %s", standard.Format("2006-01-02 Mon"))
	}
	express := Order{CreatedAt: created, ShippingTitle: "EXPRESS DISPATCH"}.ExpectedDispatch()
	if express.Format("2006-01-02") != "2026-10-15" {
		t.Errorf("express = %s", express.Format("2006-01-02 Mon"))
	}
}

func TestHasPhone(t *testing.T) {
	o := Order{Phones: []string{"", "+91 98765 43210"}}
	if !o.HasPhone("919876543210") {
		t.Error("expected match")
	}
	if o.HasPhone("919113994682") {
		t.Error("unexpected match")
	}
}
