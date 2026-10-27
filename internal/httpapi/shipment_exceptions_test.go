package httpapi

import (
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
)

func TestExceptionRowPassesDelhiverysTabThrough(t *testing.T) {
	// THE POINT OF THE WHOLE FEATURE. Classifying the status code locally is
	// what made Tensor list three parcels as needing a decision while
	// Delhivery's Need Action tab was empty. The row's own word wins.
	row := exceptionRow(delhivery.Exception{
		Waybill: "69187910000232",
		ID:      "9663139c",
		Action:  "Auto Re-attempt",
		Reason:  "Consignee Unavailable",
	}, shipmentRowResponse{})

	if row.Tab != "Auto Re-attempt" {
		t.Errorf("tab = %q, want Delhivery's own filing", row.Tab)
	}
	if row.Reason != "Consignee Unavailable" {
		t.Errorf("reason = %q", row.Reason)
	}
}

func TestExceptionRowSortsTheButtons(t *testing.T) {
	// cta_list is a JSON object, so Go hands it back in random order. Buttons
	// that swap places between page loads are how somebody presses Return
	// meaning Re-attempt.
	row := exceptionRow(delhivery.Exception{
		CTAList: map[string]string{
			"CTA023": "Reattempt shipment",
			"CTA002": "Return shipment",
			"CTA005": "Repack All and Deliver",
		},
	}, shipmentRowResponse{})

	if len(row.Actions) != 3 {
		t.Fatalf("want 3 actions, got %d", len(row.Actions))
	}
	want := []string{"CTA002", "CTA005", "CTA023"}
	for i, code := range want {
		if row.Actions[i].Code != code {
			t.Errorf("action %d = %s, want %s", i, row.Actions[i].Code, code)
		}
	}
	if row.Actions[2].Label != "Reattempt shipment" {
		t.Errorf("label lost: %q", row.Actions[2].Label)
	}
}

func TestExceptionRowTakesTheOrderWhenOneIsKnown(t *testing.T) {
	row := exceptionRow(
		delhivery.Exception{Waybill: "69187910000232"},
		shipmentRowResponse{
			OrderName:    "T3DPS-115896",
			CustomerName: "Pradnya Jadhav",
			City:         "Navi Mumbai",
			AdminURL:     "https://admin.shopify.com/orders/1",
		})

	if row.OrderName != "T3DPS-115896" || row.CustomerName != "Pradnya Jadhav" {
		t.Errorf("order details lost: %+v", row)
	}
}

func TestExceptionRowSurvivesAnUnknownWaybill(t *testing.T) {
	// A brand may have Delhivery One and no connected Shopify store. The
	// queue is still the answer; the order number is a convenience.
	row := exceptionRow(delhivery.Exception{Waybill: "69187910000232"}, shipmentRowResponse{})
	if row.Waybill != "69187910000232" {
		t.Errorf("waybill lost: %q", row.Waybill)
	}
	if row.OrderName != "" {
		t.Errorf("order name invented: %q", row.OrderName)
	}
}

func TestExceptionRowReadsDispatchCountFromAString(t *testing.T) {
	row := exceptionRow(delhivery.Exception{DispatchCount: "4"}, shipmentRowResponse{})
	if row.Attempts != 4 {
		t.Errorf("attempts = %d, want 4", row.Attempts)
	}
	blank := exceptionRow(delhivery.Exception{DispatchCount: ""}, shipmentRowResponse{})
	if blank.Attempts != 0 {
		t.Errorf("a missing count should be 0, got %d", blank.Attempts)
	}
}

func TestSLABreachIsComputedNotTrusted(t *testing.T) {
	// default_cta reads "Overdue" on rows whose SLA has not passed, so the
	// clock is read here instead.
	past := exceptionRow(delhivery.Exception{SLA: time.Now().Add(-time.Hour).Unix()},
		shipmentRowResponse{})
	if !past.SLABreached {
		t.Error("an SLA an hour ago is breached")
	}
	future := exceptionRow(delhivery.Exception{SLA: time.Now().Add(time.Hour).Unix()},
		shipmentRowResponse{})
	if future.SLABreached {
		t.Error("an SLA an hour away is not breached")
	}
	none := exceptionRow(delhivery.Exception{SLA: 0}, shipmentRowResponse{})
	if none.SLABreached {
		t.Error("no SLA cannot be breached")
	}
}

func TestEpochSecondsTreatsZeroAsNever(t *testing.T) {
	// Delhivery uses 0 for "never", which formats as 1970 if passed through.
	if got := epochSeconds(0); got != "" {
		t.Errorf("zero should be blank, got %q", got)
	}
	if got := epochSeconds(-1); got != "" {
		t.Errorf("negative should be blank, got %q", got)
	}
	if got := epochSeconds(1792486267); got == "" {
		t.Error("a real timestamp should format")
	}
}

func TestExceptionTabLabels(t *testing.T) {
	// These are the words on Delhivery's own tabs, and the page is supposed
	// to read the same as their dashboard.
	cases := map[delhivery.Tab]string{
		delhivery.TabNeedAction:           "Need Action",
		delhivery.TabAutoReattempt:        "Auto Reattempt",
		delhivery.TabReturnRecommendation: "Return Recommendation",
		delhivery.TabClosed:               "Closed",
	}
	for tab, want := range cases {
		if got := exceptionTabLabel(tab); got != want {
			t.Errorf("%s label = %q, want %q", tab, got, want)
		}
	}
}

func TestExceptionTabsAreTheOpenWorkOnly(t *testing.T) {
	// Closed is history and is fetched only when asked for; including it by
	// default would put resolved parcels on a page about stuck ones.
	for _, tab := range exceptionTabs {
		if tab == delhivery.TabClosed {
			t.Error("Closed must not be fetched by default")
		}
		if !tab.Valid() {
			t.Errorf("%s is not a tab Delhivery knows", tab)
		}
	}
	if len(exceptionTabs) != 3 {
		t.Errorf("want Delhivery's three tabs, got %d", len(exceptionTabs))
	}
}
