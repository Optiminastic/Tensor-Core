package accounts

import (
	"testing"
	"time"
)

func ist(t *testing.T) *time.Location {
	t.Helper()
	// Fixed rather than LoadLocation: Windows has no tzdata by default and a
	// nil location would silently make every window UTC, which is 5h30 wrong.
	return time.FixedZone("IST", 5*60*60+30*60)
}

func TestWindowRunsNoonToNoon(t *testing.T) {
	loc := ist(t)

	// Just after noon: the day that just ended is today's.
	from, to := Window(time.Date(2026, 10, 28, 12, 30, 0, 0, loc), loc)
	if to.Day() != 28 || to.Hour() != 12 {
		t.Errorf("to = %s, want noon on the 28th", to)
	}
	if from.Day() != 27 || from.Hour() != 12 {
		t.Errorf("from = %s, want noon on the 27th", from)
	}

	// Before noon: today's noon has not happened, so the finished day ended
	// at yesterday's.
	from, to = Window(time.Date(2026, 10, 28, 9, 0, 0, 0, loc), loc)
	if to.Day() != 27 {
		t.Errorf("to = %s, want noon on the 27th", to)
	}
	if from.Day() != 26 {
		t.Errorf("from = %s, want noon on the 26th", from)
	}
}

func TestWindowIsHalfOpenSoNoOrderIsCountedTwice(t *testing.T) {
	loc := ist(t)
	_, to := Window(time.Date(2026, 10, 28, 13, 0, 0, 0, loc), loc)
	from := to.AddDate(0, 0, -1)

	// An order placed exactly on the boundary belongs to the day STARTING,
	// never to the one ending - otherwise it appears on two reports.
	if InWindow(to, from, to) {
		t.Error("an order at exactly noon was counted in the closing day")
	}
	if !InWindow(from, from, to) {
		t.Error("an order at the opening noon was excluded")
	}
	if !InWindow(to.Add(-time.Second), from, to) {
		t.Error("an order a second before noon was excluded")
	}
}

func TestSplitBacksTaxOutRatherThanAddingIt(t *testing.T) {
	// A 898.00 sale at 18% INCLUSIVE: 898 / 1.18 = 761.02, tax 136.98.
	// Adding 18% on top would give 1059.64 instead and overstate the day.
	taxable, cgst, sgst, igst := Split(898, 18, true)
	if taxable != 761.02 {
		t.Errorf("taxable = %.2f, want 761.02", taxable)
	}
	if igst != 136.98 {
		t.Errorf("igst = %.2f, want 136.98", igst)
	}
	if cgst != 0 || sgst != 0 {
		t.Errorf("an interstate sale must not split: cgst=%.2f sgst=%.2f", cgst, sgst)
	}
	if got := taxable + igst; !near(got, 898) {
		t.Errorf("taxable + tax = %.2f, want the gross back", got)
	}
}

func TestInStateSaleSplitsAndTheHalvesStillSum(t *testing.T) {
	taxable, cgst, sgst, igst := Split(898, 18, false)
	if igst != 0 {
		t.Errorf("an in-state sale must not use IGST, got %.2f", igst)
	}
	if got := cgst + sgst; !near(got, 136.98) {
		t.Errorf("cgst + sgst = %.2f, want the whole tax 136.98", got)
	}
	if got := taxable + cgst + sgst; !near(got, 898) {
		t.Errorf("columns sum to %.2f, want 898.00", got)
	}
}

func TestAnOddTaxDoesNotLoseAPaisa(t *testing.T) {
	// The reason one half is the remainder rather than a second rounding:
	// rounding 0.025 twice gives two 0.03s and invents a paisa.
	for _, gross := range []float64{0.33, 1.01, 7.77, 19.99, 123.45, 649, 1098} {
		taxable, cgst, sgst, _ := Split(gross, 18, false)
		if got := taxable + cgst + sgst; !near(got, round2(gross)) {
			t.Errorf("gross %.2f: columns sum to %.2f", gross, got)
		}
	}
}

func TestZeroRateLeavesTheAmountAlone(t *testing.T) {
	// Guards the configuration mistake, not a real tax position: a rate of
	// zero must not divide by one and silently report the gross as taxable
	// with tax invented from nothing.
	taxable, cgst, sgst, igst := Split(500, 0, false)
	if taxable != 500 || cgst != 0 || sgst != 0 || igst != 0 {
		t.Errorf("zero rate gave %.2f/%.2f/%.2f/%.2f", taxable, cgst, sgst, igst)
	}
}

func TestBuildMakesOneRowPerLineItem(t *testing.T) {
	loc := ist(t)
	placed := time.Date(2026, 10, 27, 18, 0, 0, 0, loc)

	rows := Build([]Order{{
		PlacedAt: placed, Number: "T3DPS-116121", Customer: "Chiku Patel",
		State: "Gujarat",
		Lines: []Line{
			{Title: "Dual Name Plank - PURPLE", SKU: "DNPWL-PUR", Quantity: 1, Gross: 898},
			{Title: "Lotus Diya Holder", SKU: "PWDH-P", Quantity: 2, Gross: 1098},
		},
	}}, Options{})

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want one per line item", len(rows))
	}
	// Both rows carry the same voucher: they are one invoice.
	if rows[0].Voucher != "T3DPS-116121" || rows[1].Voucher != rows[0].Voucher {
		t.Errorf("voucher numbers differ across one order: %q %q", rows[0].Voucher, rows[1].Voucher)
	}
	if rows[0].Party != "Chiku Patel" || rows[0].State != "Gujarat" {
		t.Errorf("party/state lost: %+v", rows[0])
	}
	// Gujarat against a Maharashtra seller is interstate.
	if rows[0].IGST == 0 || rows[0].CGST != 0 {
		t.Errorf("Gujarat sale should be IGST: %+v", rows[0])
	}
	// Rate is PER UNIT and taxable, not the shelf price.
	if rows[1].Qty != 2 {
		t.Errorf("qty = %d", rows[1].Qty)
	}
	// Rate is the per-unit taxable value SHOWN TO TWO DECIMALS, so on an odd
	// amount it cannot always multiply back exactly - 930.51 over two units
	// is 465.255, and either rounding loses a paisa. Taxable Value is the
	// authoritative column and the one the tax is computed from; Rate is for
	// the reader. Tolerance is a paisa per unit, which is the most that
	// rounding can cost.
	if drift := rows[1].Rate*2 - rows[1].Taxable; drift > 0.02 || drift < -0.02 {
		t.Errorf("rate %.2f x 2 is %.2f off taxable %.2f", rows[1].Rate, drift, rows[1].Taxable)
	}
	if !near(rows[1].Total, 1098) {
		t.Errorf("total = %.2f, want the gross 1098", rows[1].Total)
	}
}

func TestAnInStateOrderUsesCGSTAndSGST(t *testing.T) {
	loc := ist(t)
	rows := Build([]Order{{
		PlacedAt: time.Now().In(loc), Number: "T3DPS-1", Customer: "Someone",
		State: "maharashtra ", // messy on purpose: case and a stray space
		Lines: []Line{{Title: "Plank", Quantity: 1, Gross: 649}},
	}}, Options{SellerState: "Maharashtra"})

	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].IGST != 0 {
		t.Errorf("a Maharashtra sale from Maharashtra must not be IGST: %+v", rows[0])
	}
	if rows[0].CGST == 0 || rows[0].SGST == 0 {
		t.Errorf("in-state sale did not split: %+v", rows[0])
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	rows := Build([]Order{{
		Number: "X", State: "Gujarat",
		Lines: []Line{{Title: "T", Quantity: 1, Gross: 100}},
	}}, Options{})
	if rows[0].GSTPercent != DefaultGSTPercent {
		t.Errorf("gst %% = %v, want the default %v", rows[0].GSTPercent, DefaultGSTPercent)
	}
}

func TestNoOrdersMakesNoRows(t *testing.T) {
	// A quiet day is a real answer, and must not be a nil-pointer panic in
	// the writer downstream.
	if rows := Build(nil, Options{}); len(rows) != 0 {
		t.Errorf("got %d rows from no orders", len(rows))
	}
}

func near(a, b float64) bool {
	d := a - b
	return d < 0.005 && d > -0.005
}
