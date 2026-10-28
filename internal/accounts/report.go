// Package accounts turns a day's orders into the sales register the shop's
// accountant files from.
//
// PURE, like internal/pricing. It takes orders and returns rows; no database,
// no Shopify, no clock of its own. The caller reads the orders and writes the
// spreadsheet, so every rule in here is testable against a hand-worked example
// rather than against a live store.
//
// THE DAY RUNS NOON TO NOON, not midnight to midnight. That is the shop's own
// definition and it is deliberate: the print floor works past midnight, so a
// calendar day would split one evening's trading across two reports.
package accounts

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// DefaultGSTPercent is PROVISIONAL and must be confirmed before anybody files
// from this.
//
// Shopify reports no tax at all on this store: every line carries a tax line
// titled IGST at rate 0.18, every one of them priced at 0.00, the order-level
// tax total is 0.00 and the order total equals its subtotal. So the tax cannot
// be read - it has to be derived from a rate somebody asserts, and the only
// rate the data suggests is the 18% Shopify tags each line with.
//
// Treated as INCLUSIVE: the price the customer paid already contains the tax,
// which is what a zero tax total beside a non-zero price means. If the shop's
// prices are actually ex-GST, every figure below is wrong and the constant is
// the one place to change.
const DefaultGSTPercent = 18.0

// DefaultSellerState decides CGST+SGST against IGST.
//
// A sale inside the seller's own state is split into central and state halves;
// one crossing a border is a single integrated tax. ALT OPTI MEDIA's GSTIN
// begins 27, which is Maharashtra.
const DefaultSellerState = "Maharashtra"

// Options are the assertions the caller makes about the shop.
type Options struct {
	// GSTPercent is the rate applied to every line. One rate, because the
	// store sells one kind of thing; a product taxed differently would need
	// this per line and the registry is where that would come from.
	GSTPercent float64
	// SellerState is compared against the shipping state, case-insensitively.
	SellerState string
	// Narration fills the last column. Blank is fine - it is a free-text note
	// for whoever reads the sheet.
	Narration string
}

func (o Options) withDefaults() Options {
	if o.GSTPercent <= 0 {
		o.GSTPercent = DefaultGSTPercent
	}
	if strings.TrimSpace(o.SellerState) == "" {
		o.SellerState = DefaultSellerState
	}
	return o
}

// Line is one item on an order, priced as the customer paid for it.
type Line struct {
	Title    string
	SKU      string
	Quantity int
	// Gross is the money taken for this line IN TOTAL, not per unit, and it
	// includes the tax. It is what the customer was charged after any
	// discount - a discounted line must report what was actually received,
	// not the list price, or the register will not reconcile with the bank.
	Gross float64
}

// Order is the subset of an order this report needs.
type Order struct {
	// PlacedAt is Shopify's own created_at. The window is judged on this and
	// never on when Tensor imported it: an order placed at 11:58 and synced
	// at 12:05 belongs to the day it was placed.
	PlacedAt time.Time
	Number   string
	Customer string
	// State is the SHIPPING province. Place of supply is where the goods go,
	// not where the card was billed.
	State string
	Lines []Line
}

// Row is one line of the sales register, in the order the template's columns
// run. The template is a single header row: Date, voucher no., Party Name,
// State Name, Item, Qty, Rate, Taxable Value, GST %, CGST, SGST, IGST, total
// value, naration.
type Row struct {
	Date    time.Time
	Voucher string
	Party   string
	State   string
	Item    string
	Qty     int
	// Rate is the per-unit TAXABLE value, which is what a sales register
	// means by rate - not the shelf price.
	Rate       float64
	Taxable    float64
	GSTPercent float64
	CGST       float64
	SGST       float64
	IGST       float64
	Total      float64
	Narration  string
}

// Window returns the most recent COMPLETE noon-to-noon day at or before now.
//
// Before noon, the finished day is the one that ended at yesterday's noon; at
// or after noon, it is the one that ended today. Half-open: [from, to), so an
// order placed exactly at noon belongs to the day that is only just starting
// and is never counted twice.
func Window(now time.Time, loc *time.Location) (from, to time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	n := now.In(loc)
	noon := time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, loc)
	if n.Before(noon) {
		noon = noon.AddDate(0, 0, -1)
	}
	return noon.AddDate(0, 0, -1), noon
}

// InWindow reports whether an order belongs to [from, to).
func InWindow(placedAt, from, to time.Time) bool {
	return !placedAt.Before(from) && placedAt.Before(to)
}

// Build turns a day's orders into register rows, ONE PER LINE ITEM.
//
// Per line, not per order, because that is what the template asks for: it has
// Item, Qty and Rate columns, so an order of two different planks is two rows
// sharing a voucher number.
//
// Orders are NOT filtered here. The caller selects the window - this would
// otherwise need a clock, and a pure function with a clock in it is not one.
func Build(orders []Order, opts Options) []Row {
	opts = opts.withDefaults()
	rows := make([]Row, 0, len(orders))

	for _, order := range orders {
		interState := !sameState(order.State, opts.SellerState)
		for _, line := range order.Lines {
			taxable, cgst, sgst, igst := Split(line.Gross, opts.GSTPercent, interState)

			qty := line.Quantity
			rate := taxable
			if qty > 1 {
				rate = round2(taxable / float64(qty))
			}

			rows = append(rows, Row{
				Date:       order.PlacedAt,
				Voucher:    order.Number,
				Party:      order.Customer,
				State:      order.State,
				Item:       line.Title,
				Qty:        qty,
				Rate:       rate,
				Taxable:    taxable,
				GSTPercent: opts.GSTPercent,
				CGST:       cgst,
				SGST:       sgst,
				IGST:       igst,
				Total:      round2(line.Gross),
				Narration:  opts.Narration,
			})
		}
	}
	return rows
}

// Split takes a tax-inclusive amount apart.
//
// INCLUSIVE, so the tax is backed out rather than added on: taxable =
// gross / (1 + rate), and the tax is whatever is left. Adding the rate to the
// gross instead would overstate every line by the tax on the tax.
//
// THE HALVES ARE MADE TO SUM. For an in-state sale the tax splits into CGST
// and SGST, and rounding each half independently loses or gains a paisa on odd
// amounts - so one half is rounded and the other is the remainder. A register
// whose columns do not add up is one the accountant has to redo by hand.
func Split(gross, ratePercent float64, interState bool) (taxable, cgst, sgst, igst float64) {
	if ratePercent <= 0 {
		return round2(gross), 0, 0, 0
	}
	taxable = round2(gross / (1 + ratePercent/100))
	tax := round2(gross - taxable)

	if interState {
		return taxable, 0, 0, tax
	}
	cgst = round2(tax / 2)
	// Not round2(tax/2) twice: on a tax of 0.05 that would make two 0.03s.
	sgst = round2(tax - cgst)
	return taxable, cgst, sgst, 0
}

// sameState compares two state names forgivingly. Shopify sends "Maharashtra";
// a human might have typed "MAHARASHTRA " into a setting.
func sameState(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// round2 is half-away-from-zero at two decimals, matching how money is
// rounded on an invoice. internal/pricing rounds half-to-EVEN for costing
// parity with the old Python engine; this is a different job - an invoice
// rounds a half-paisa up - and the two must not be confused.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// Money formats an amount the way the sheet shows it.
func Money(v float64) string { return fmt.Sprintf("%.2f", v) }
