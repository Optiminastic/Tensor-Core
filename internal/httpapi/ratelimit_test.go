package httpapi

import (
	"testing"
	"time"
)

func TestOneCallerIsAllowedItsBurstAndNoMore(t *testing.T) {
	l := newRateLimiter(time.Minute, 3)
	now := time.Now()
	for i := range 3 {
		if !l.allow("1.2.3.4", now) {
			t.Fatalf("request %d was refused inside the burst", i+1)
		}
	}
	if l.allow("1.2.3.4", now) {
		t.Error("the fourth request past a burst of three was allowed")
	}
}

func TestOneNoisyCallerDoesNotAffectAnother(t *testing.T) {
	// A customer whose page is polling must not lock out every other customer
	// - which is what a single global counter would do.
	l := newRateLimiter(time.Minute, 2)
	now := time.Now()
	l.allow("1.2.3.4", now)
	l.allow("1.2.3.4", now)
	if l.allow("1.2.3.4", now) {
		t.Fatal("the noisy caller was not limited")
	}
	if !l.allow("5.6.7.8", now) {
		t.Error("a different caller was refused because of somebody else")
	}
}

func TestTheWindowReopens(t *testing.T) {
	l := newRateLimiter(time.Minute, 1)
	now := time.Now()
	if !l.allow("1.2.3.4", now) {
		t.Fatal("the first request was refused")
	}
	if l.allow("1.2.3.4", now.Add(30*time.Second)) {
		t.Error("allowed again inside the window")
	}
	if !l.allow("1.2.3.4", now.Add(61*time.Second)) {
		t.Error("still refused after the window passed")
	}
}

func TestAScanCannotGrowTheTableForever(t *testing.T) {
	// The memory an attacker can make this cost is bounded. Past the cap the
	// table is dropped rather than grown - the next window rebuilds it.
	l := newRateLimiter(time.Minute, 1)
	now := time.Now()
	for i := range publicRateMaxKeys + 10 {
		l.allow(string(rune(i%1114111)), now)
	}
	if len(l.buckets) > publicRateMaxKeys+1 {
		t.Errorf("the table grew to %d entries", len(l.buckets))
	}
}
