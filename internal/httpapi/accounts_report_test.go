package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/accounts"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

func TestRecipientsAcceptHowPeopleActuallyType(t *testing.T) {
	// The box is a text field somebody pastes into. Commas, semicolons,
	// newlines and stray spaces are ordinary input, not errors to reject.
	got, refusal := cleanRecipients([]string{
		"insha@optiminastic.com, accounts@optiminastic.com;  \n tech5@optiminastic.com ",
	}, "To")
	if refusal != "" {
		t.Fatalf("refused ordinary input: %s", refusal)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d addresses from one pasted line: %v", len(got), got)
	}
}

func TestDuplicateRecipientsAreCollapsed(t *testing.T) {
	// Two copies means the accountant gets two emails, and whoever added the
	// second one will not see why.
	got, refusal := cleanRecipients([]string{
		"insha@optiminastic.com", "INSHA@optiminastic.com", " insha@optiminastic.com ",
	}, "To")
	if refusal != "" {
		t.Fatalf("refused: %s", refusal)
	}
	if len(got) != 1 {
		t.Errorf("kept %d copies of one address: %v", len(got), got)
	}
}

func TestObviouslyBrokenAddressesAreRefusedByName(t *testing.T) {
	// Named in the refusal, because "invalid input" on a list of twenty
	// leaves somebody hunting.
	for _, bad := range []string{"insha", "@optiminastic.com", "insha@", "a..b@c.com", "insha@com"} {
		_, refusal := cleanRecipients([]string{bad}, "Cc")
		if refusal == "" {
			t.Errorf("%q was accepted as an address", bad)
			continue
		}
		if !strings.Contains(refusal, bad) || !strings.Contains(refusal, "Cc") {
			t.Errorf("refusal for %q names neither the address nor the field: %s", bad, refusal)
		}
	}
}

func TestTooManyRecipientsIsRefused(t *testing.T) {
	// Not a technical limit - a blast radius. This attachment is every sale
	// the shop made.
	many := make([]string, 0, maxRecipients+1)
	for i := 0; i <= maxRecipients; i++ {
		many = append(many, "person"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.com")
	}
	if _, refusal := cleanRecipients(many, "Bcc"); refusal == "" {
		t.Error("an unbounded recipient list was accepted")
	}
}

func TestEmptyListIsFineAndNotAnError(t *testing.T) {
	// Cc and Bcc are genuinely optional; only To is required, and that is
	// checked where the switch is turned on.
	got, refusal := cleanRecipients(nil, "Bcc")
	if refusal != "" || len(got) != 0 {
		t.Errorf("empty list gave %v / %q", got, refusal)
	}
}

func TestOrderConversionKeepsTheDiscountedPrice(t *testing.T) {
	// A register built from list prices does not reconcile with the bank.
	// LineTotal is after discount and must win.
	placed := time.Date(2026, 10, 27, 18, 0, 0, 0, ReportZone())
	order := accountsOrderFrom(shopify.OrderSummary{
		Name:            "T3DPS-116121",
		Customer:        &shopify.OrderCustomer{FirstName: "Chiku", LastName: "Patel"},
		ShippingAddress: &shopify.OrderAddress{Province: "Gujarat", City: "Surat"},
		LineItems: []shopify.OrderLineItem{{
			Name: "Dual Name Plank", VariantTitle: "PURPLE", SKU: "DNPWL-PUR",
			Quantity: 1, UnitPrice: "1298.00", DiscountedUnitPrice: "898.00",
			LineTotal: "898.00",
		}},
	}, placed)

	if len(order.Lines) != 1 {
		t.Fatalf("got %d lines", len(order.Lines))
	}
	if order.Lines[0].Gross != 898 {
		t.Errorf("gross = %.2f, want the discounted 898.00", order.Lines[0].Gross)
	}
	if order.Customer != "Chiku Patel" {
		t.Errorf("customer = %q", order.Customer)
	}
	if order.State != "Gujarat" {
		t.Errorf("state = %q; place of supply is where the goods went", order.State)
	}
	// The variant is part of what was sold and belongs in the Item column.
	if !strings.Contains(order.Lines[0].Title, "PURPLE") {
		t.Errorf("item title lost the variant: %q", order.Lines[0].Title)
	}
}

func TestEveryColumnIsPopulatedFromARealisticOrder(t *testing.T) {
	// THE REGRESSION. A first version read line items out of Tensor's own
	// mirror, which stores production.LineItem - product_name, sku, quantity,
	// and NO PRICE, because the pipeline's line item describes what to print
	// rather than what was charged. Go's JSON matching is case-insensitive,
	// so quantity decoded and nothing else did: a register went out with
	// every item "(unnamed item)" and every money column 0.00.
	//
	// This asserts the three things that were wrong, together, because each
	// one alone looked survivable.
	placed := time.Date(2026, 10, 27, 18, 0, 0, 0, ReportZone())
	order := accountsOrderFrom(shopify.OrderSummary{
		Name:            "T3DPS-116200",
		Customer:        &shopify.OrderCustomer{FirstName: "Saritha", LastName: "Ramala"},
		ShippingAddress: &shopify.OrderAddress{Province: "Telangana"},
		TotalPrice:      "649.00",
		LineItems: []shopify.OrderLineItem{{
			Name: "Personalised Diya Holder", SKU: "PDHR-P",
			Quantity: 1, LineTotal: "649.00",
		}},
	}, placed)

	line := order.Lines[0]
	if line.Title == "(unnamed item)" || line.Title == "" {
		t.Errorf("item name lost: %q", line.Title)
	}
	if line.Gross == 0 {
		t.Error("gross is zero, so every money column on the sheet would be 0.00")
	}
	if line.Quantity != 1 {
		t.Errorf("quantity = %d", line.Quantity)
	}

	// And the whole way through to a row, which is what anybody actually reads.
	rows := accounts.Build([]accounts.Order{order}, accounts.Options{})
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.Taxable == 0 || r.Total == 0 || r.IGST == 0 {
		t.Errorf("a register row with zero money: taxable=%.2f total=%.2f igst=%.2f",
			r.Taxable, r.Total, r.IGST)
	}
	if r.Party == "" || r.State == "" || r.Item == "" {
		t.Errorf("an incomplete row: %+v", r)
	}
}

func TestAGuestCheckoutStillHasAPartyName(t *testing.T) {
	// No customer record, but a name on the shipping address. "Party Name" is
	// a column an accountant reconciles against; blank is not acceptable.
	order := accountsOrderFrom(shopify.OrderSummary{
		Name:            "T3DPS-1",
		ShippingAddress: &shopify.OrderAddress{Name: "Walk In", Province: "Kerala"},
		LineItems:       []shopify.OrderLineItem{{Name: "Plank", Quantity: 1, LineTotal: "100"}},
	}, time.Now())
	if order.Customer != "Walk In" {
		t.Errorf("party name = %q, want the shipping name", order.Customer)
	}
}

func TestAnOrderWithNoLineItemsStillAppears(t *testing.T) {
	// Wrong in detail beats missing entirely: a sale absent from the register
	// is the one error nobody spots.
	order := accountsOrderFrom(shopify.OrderSummary{
		Name: "T3DPS-1", TotalPrice: "649.00",
	}, time.Now())
	if len(order.Lines) != 1 {
		t.Fatalf("got %d lines, want one standing in for the order", len(order.Lines))
	}
	if order.Lines[0].Gross != 649 {
		t.Errorf("gross = %.2f, want the order total", order.Lines[0].Gross)
	}
}

func TestReportZoneIsIST(t *testing.T) {
	// A UTC zone here would move every window by five and a half hours and
	// put a day's orders quietly on the wrong report.
	_, offset := time.Now().In(ReportZone()).Zone()
	if offset != 5*60*60+30*60 {
		t.Errorf("report zone offset = %ds, want IST", offset)
	}
}

func TestSubjectAndFilenameAgreeOnTheDay(t *testing.T) {
	// They are read side by side in an inbox; disagreeing is how somebody
	// files the wrong day.
	end := time.Date(2026, 10, 28, 12, 0, 0, 0, ReportZone())
	if !strings.Contains(reportSubject(end), "28 Oct 2026") {
		t.Errorf("subject = %q", reportSubject(end))
	}
	if !strings.Contains(accounts.Filename(end, ReportZone()), "2026-10-28") {
		t.Errorf("filename = %q", accounts.Filename(end, ReportZone()))
	}
}

func TestAQuietDaySaysSoInTheBody(t *testing.T) {
	// An empty attachment with no explanation reads as a broken report.
	from := time.Date(2026, 10, 27, 12, 0, 0, 0, ReportZone())
	body := reportBody(from, from.AddDate(0, 0, 1), 0, 0)
	if !strings.Contains(body, "No orders") {
		t.Errorf("a quiet day is not explained: %q", body)
	}
}
