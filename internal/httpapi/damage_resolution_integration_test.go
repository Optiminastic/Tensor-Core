package httpapi

// Deciding what happens to a damaged parcel, and carrying it out.
//
// Delhivery is not configured in this harness, so these also cover what the
// shop does when the courier refuses - which is the case the whole ordering
// exists for: the customer is told only once there is a courier to send a
// replacement with.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/whatsappbot/content"
)

// filedReport files one damage report through the public form and returns its
// id, plus a support token that may decide its outcome.
func filedReport(t *testing.T, h *waHarness) (string, string) {
	t.Helper()
	paidOrder(t, h.store, "T3DPS-115766", 7002)

	h.tap(t, content.OptionDamage)
	token := strings.TrimPrefix(h.meta.last(t).URL, waFrontendURL+"/d/")
	attachDamageFile(t, h, token, "image/jpeg")
	if rr := postDamageJSON(h.router, "/public/damage/"+token, "115766"); rr.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", rr.Code, rr.Body.String())
	}

	// SUPPORT, not an admin: the shop asked for both, and support is the
	// narrower of the two - if it works for them it works for an admin, who
	// holds every permission by construction.
	support := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(), auth.DamageResolve.Key(),
	})
	rr := doJSON(h.router, http.MethodGet, "/brands/"+waBrand+"/damage-reports", support, nil)
	var list struct {
		Items []damageReportJSON `json:"items"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
	}
	if list.Items[0].ResolutionKind != "" {
		t.Fatalf("a new report arrived already decided as %q", list.Items[0].ResolutionKind)
	}
	return list.Items[0].ID, support
}

type decisionResult struct {
	Report      damageReportJSON `json:"report"`
	MessageSent bool             `json:"message_sent"`
	Waybill     string           `json:"waybill"`
	Requeued    int              `json:"requeued"`
	Blocked     string           `json:"blocked"`
}

func TestIntegrationDamageResolution(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	base := "/brands/" + waBrand + "/damage-reports"

	// Reading a report is not deciding it: the dropdown is a separate
	// permission from the one that moves a report between statuses.
	noDecide := h.minter.mint(t, []string{auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key()})
	if rr := doJSON(h.router, http.MethodPost, base+"/"+id+"/resolution", noDecide,
		map[string]any{"kind": "reprint"}); rr.Code != http.StatusForbidden {
		t.Errorf("without damage:resolve = %d, want 403", rr.Code)
	}

	// Neither of the two: refused by name rather than stored.
	if rr := doJSON(h.router, http.MethodPost, base+"/"+id+"/resolution", support,
		map[string]any{"kind": "refund"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("kind=refund = %d, want 422", rr.Code)
	}

	before := h.meta.count()
	decide := doJSON(h.router, http.MethodPost, base+"/"+id+"/resolution", support,
		map[string]any{"kind": "reprint", "note": "Plate cracked in transit, reprinting."})
	if decide.Code != http.StatusOK {
		t.Fatalf("decide = %d %s", decide.Code, decide.Body.String())
	}
	var decided decisionResult
	_ = json.Unmarshal(decide.Body.Bytes(), &decided)

	if decided.Report.ResolutionKind != "reprint" {
		t.Errorf("kind = %q, want reprint", decided.Report.ResolutionKind)
	}
	if decided.Report.ReverseState != "" {
		t.Errorf("a reprint opened a reverse state: %q", decided.Report.ReverseState)
	}
	if decided.Report.ResolutionNote == "" {
		t.Error("the note was not stored; the next person reading this report learns nothing")
	}

	// A FAILED REPRINT IS NOT RESOLVED. The courier was refused and the
	// customer was never told, so a decision has been taken about this report
	// and nothing else. Calling that resolved hides the row that most needs
	// somebody - and there is no status dropdown left to reopen it by hand.
	if decided.Report.Status == damageResolved {
		t.Errorf("status = %q although the courier was refused", decided.Report.Status)
	}

	// NO COURIER, NO MESSAGE - the rule the whole ordering exists for.
	// Promising a replacement that nothing has been arranged to carry is the
	// one outcome worse than telling somebody late.
	if decided.MessageSent {
		t.Error("the customer was promised a replacement although no courier was booked")
	}
	if h.meta.count() != before {
		t.Errorf("messages sent = %d, want %d - nothing goes without a courier",
			h.meta.count(), before)
	}
	if decided.Blocked == "" {
		t.Error("nothing said why the reprint stopped; the drawer would show a silent failure")
	}

	// The failure is on the record, and the report has NOT advanced past it:
	// a failed step is not progress.
	stages := damageStagesFor(t, h, id, support)
	if stages["courier_booked"] != "failed" {
		t.Errorf("courier stage = %q, want failed", stages["courier_booked"])
	}
	if stages["customer_told"] == "done" {
		t.Error("the timeline claims the customer was told")
	}
	// The report plainly exists, so the step saying so must not read as still
	// pending - the one thing certainly true looking like the one thing still
	// waiting is worse than not drawing it.
	if stages["reported"] != "done" {
		t.Errorf("reported stage = %q, want done", stages["reported"])
	}

	// DECIDED ONCE. A second decision - two people on the board, or one
	// double-click - must not act again.
	again := doJSON(h.router, http.MethodPost, base+"/"+id+"/resolution", support,
		map[string]any{"kind": "reverse"})
	if again.Code != http.StatusConflict {
		t.Errorf("second decision = %d, want 409", again.Code)
	}
	if h.meta.count() != before {
		t.Error("a refused second decision messaged the customer")
	}
}

// A REVERSE DOES NOT CLOSE THE REPORT, and that is the half that matters: it
// opens a negotiation running over days - the customer has to agree, pay, and
// have somebody check the payment - so a board showing it resolved would hide
// the rows that still need chasing.
func TestIntegrationAReverseAsksForTheMoneyAndStaysOpen(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)

	before := h.meta.count()
	decide := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/resolution", support,
		map[string]any{"kind": "reverse"})
	if decide.Code != http.StatusOK {
		t.Fatalf("decide = %d %s", decide.Code, decide.Body.String())
	}
	var decided decisionResult
	_ = json.Unmarshal(decide.Body.Bytes(), &decided)

	if decided.Report.Status != damageInReview {
		t.Errorf("status = %q, want %q - a reverse has days of negotiation ahead of it",
			decided.Report.Status, damageInReview)
	}
	if decided.Report.ReverseState != "offered" {
		t.Errorf("reverse_state = %q, want offered", decided.Report.ReverseState)
	}
	// Choosing a reverse books no courier and re-queues no print: those
	// belong to the other route entirely.
	if decided.Waybill != "" || decided.Requeued != 0 {
		t.Errorf("a reverse booked %q and re-queued %d jobs",
			decided.Waybill, decided.Requeued)
	}

	// EXACTLY ONE MESSAGE. It used to be none here and two later - an offer
	// with Yes/No buttons, then the QR once they agreed. Paying is agreeing
	// now, so the whole ask goes at once, and a second message would be the
	// shop asking a customer for money twice.
	if sent := h.meta.count() - before; sent != 1 {
		t.Fatalf("choosing a reverse sent %d messages, want exactly 1", sent)
	}
	if !decided.MessageSent {
		t.Error("message_sent = false, so the screen would say nobody was told")
	}

	ask := h.meta.last(t)
	// The QR IS the message. A caption cannot be scrolled away from the thing
	// it explains, and a customer reading "pay 100" three messages above the
	// code is a customer paying the wrong amount into the wrong place.
	if ask.Type != "image" {
		t.Errorf("the ask went as %q, want an image carrying the payment QR", ask.Type)
	}
	if !strings.HasPrefix(ask.URL, waPublicBaseURL) {
		t.Errorf("the QR points at %q, which is not this service", ask.URL)
	}
	// The price and what to do next both have to be in the caption: this is
	// the only message the customer gets before they are expected to pay.
	for _, want := range []string{"100", "screenshot"} {
		if !strings.Contains(ask.Text, want) {
			t.Errorf("the ask never mentions %q: %q", want, ask.Text)
		}
	}

	// And the timeline says so, because "we asked" is the step everything
	// after this is waiting on.
	if got := damageStagesFor(t, h, id, support)["customer_told"]; got != "done" {
		t.Errorf("customer_told = %q, want done", got)
	}
}

// damageStagesFor reads a report's timeline as stage -> state.
func damageStagesFor(t *testing.T, h *waHarness, id, token string) map[string]string {
	t.Helper()
	rr := doJSON(h.router, http.MethodGet,
		"/brands/"+waBrand+"/damage-reports/"+id, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Stages []struct {
			Stage string `json:"stage"`
			State string `json:"state"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("read stages: %v", err)
	}
	out := map[string]string{}
	for _, st := range body.Stages {
		out[st.Stage] = st.State
	}
	return out
}

// --- Closed: support settled it in conversation. ---

func TestIntegrationClosingNeedsSayingWhatWasDone(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)

	// THE NOTE IS THE OUTCOME HERE. A reprint leaves a waybill and a print
	// job behind it and a reverse leaves a payment; a closed report leaves a
	// sentence, and an empty one means the shop has no record of what it
	// agreed with a customer who complained.
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/resolution", support,
		map[string]any{"kind": "closed", "note": "   "})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("closing with a blank note = %d %s, want 422", rr.Code, rr.Body.String())
	}
	// And the refusal has to say which field, or the operator retries the
	// same empty box.
	if !strings.Contains(strings.ToLower(rr.Body.String()), "note") {
		t.Errorf("the refusal never mentions the note: %s", rr.Body.String())
	}

	// Refused means NOT DECIDED. A report that recorded the decision and then
	// rejected the note would be closed with no record of why.
	if again := reportByID(t, h, id, support); again.ResolutionKind != "" {
		t.Errorf("a refused close still decided the report as %q", again.ResolutionKind)
	}
}

func TestIntegrationAClosedReportIsDoneAndTellsNobody(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)

	before := h.meta.count()
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/resolution", support,
		map[string]any{"kind": "closed", "note": "Posted the missing screw, customer happy."})
	if rr.Code != http.StatusOK {
		t.Fatalf("close = %d %s", rr.Code, rr.Body.String())
	}
	var decided decisionResult
	_ = json.Unmarshal(rr.Body.Bytes(), &decided)

	// RESOLVED IMMEDIATELY, unlike the other two. They sit in review because
	// work follows them that can fail - a courier, a message, a print job -
	// and this one has none: the conversation already happened.
	if decided.Report.Status != damageResolved {
		t.Errorf("status = %q, want %q", decided.Report.Status, damageResolved)
	}
	if decided.Report.ResolutionKind != "closed" {
		t.Errorf("resolution_kind = %q, want closed", decided.Report.ResolutionKind)
	}
	if decided.Report.ReverseState != "" {
		t.Errorf("a closed report has a reverse state of %q", decided.Report.ReverseState)
	}
	// Nothing is made, sent or collected - including to the customer, who has
	// already been spoken to.
	if h.meta.count() != before {
		t.Error("closing a report messaged the customer, who support had already spoken to")
	}

	// The note is the record, so it has to come back on the report rather
	// than only in the timeline.
	if !strings.Contains(decided.Report.ResolutionNote, "missing screw") {
		t.Errorf("resolution_note = %q, which is not what was typed", decided.Report.ResolutionNote)
	}
	stages := damageStagesFor(t, h, id, support)
	if stages["closed"] != "done" {
		t.Errorf("closed stage = %q, want done", stages["closed"])
	}
	// It must not borrow the other routes' steps: a closed report showing
	// "Courier booked" as still to come would have somebody waiting for a van
	// that is never coming.
	for _, never := range []string{"courier_booked", "pickup_booked", "payment_confirmed", "job_queued"} {
		if _, ok := stages[never]; ok {
			t.Errorf("a closed report's timeline promises %q, which never happens", never)
		}
	}
}

// --- The reverse pickup's two human steps. ---

func TestIntegrationConfirmingThePaymentIsItsOwnStep(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	decideReverse(t, h, id, support)

	// From "asked", with no screenshot. A customer may pay and never send a
	// picture, and support who can see the money in the bank should not be
	// blocked by one: the screenshot is evidence, not a gate.
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/payment", support, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rr.Code, rr.Body.String())
	}
	if got := reportByID(t, h, id, support).ReverseState; got != "paid" {
		t.Fatalf("reverse_state = %q, want paid", got)
	}
	if got := damageStagesFor(t, h, id, support)["payment_confirmed"]; got != "done" {
		t.Errorf("payment_confirmed = %q, want done", got)
	}

	// Twice is refused, and the refusal says why rather than reporting a bare
	// conflict - two people on the same board will do this.
	again := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/payment", support, nil)
	if again.Code != http.StatusConflict {
		t.Errorf("confirming twice = %d, want 409", again.Code)
	}
	if !strings.Contains(strings.ToLower(again.Body.String()), "already") {
		t.Errorf("the second refusal does not say it is already confirmed: %s", again.Body.String())
	}
}

func TestIntegrationNoPickupBeforeThePaymentIsConfirmed(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	decideReverse(t, h, id, support)

	// THE ONE TRANSITION THAT MUST NEVER BE REACHABLE BY A TIRED PERSON
	// CLICKING DOWN A LIST. Booking costs a courier and puts a van outside
	// somebody's house, and here nobody has paid for it.
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/pickup", support, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("booking before payment = %d %s, want 409", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "payment") {
		t.Errorf("the refusal never names what is missing: %s", rr.Body.String())
	}
	if got := reportByID(t, h, id, support).ReverseState; got != "offered" {
		t.Errorf("a refused booking moved the report to %q", got)
	}
}

func TestIntegrationARefusedCourierLeavesTheReportWhereItWas(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	decideReverse(t, h, id, support)
	if rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/payment", support, nil); rr.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rr.Code, rr.Body.String())
	}

	// Delhivery is not configured in this harness, which is the same shape as
	// a courier that refuses. The report must stay at paid so somebody can
	// try again - writing pickup_booked first and then failing would leave a
	// report saying a van is coming when none is.
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/pickup", support, nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("booking with no courier = %d %s, want 502", rr.Code, rr.Body.String())
	}
	if got := reportByID(t, h, id, support).ReverseState; got != "paid" {
		t.Errorf("reverse_state = %q after a refused booking, want paid", got)
	}
	// Recorded as FAILED rather than left absent, because that is what makes
	// the drawer able to name the step everyone is waiting on.
	if got := damageStagesFor(t, h, id, support)["pickup_booked"]; got != "failed" {
		t.Errorf("pickup_booked = %q, want failed", got)
	}
}

// decideReverse chooses the reverse route and asserts it took.
func decideReverse(t *testing.T, h *waHarness, id, token string) {
	t.Helper()
	rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/resolution", token,
		map[string]any{"kind": "reverse", "note": "Cracked base, customer will post it back."})
	if rr.Code != http.StatusOK {
		t.Fatalf("decide reverse = %d %s", rr.Code, rr.Body.String())
	}
}

// reportByID re-reads one report from the board.
func reportByID(t *testing.T, h *waHarness, id, token string) damageReportJSON {
	t.Helper()
	rr := doJSON(h.router, http.MethodGet,
		"/brands/"+waBrand+"/damage-reports/"+id, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Report damageReportJSON `json:"report"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("read report: %v", err)
	}
	return body.Report
}
