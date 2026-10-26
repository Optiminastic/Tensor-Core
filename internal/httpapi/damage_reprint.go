package httpapi

// Carrying out a reprint.
//
// Three acts against three systems, in the order the shop asked for:
//
//  1. BOOK THE COURIER. Delhivery either accepts the forward shipment or it
//     does not, and nothing else happens until it has.
//  2. TELL THE CUSTOMER - only on a booking. A promise to send a replacement
//     is made once there is a courier to send it with, never before.
//  3. PUT THE PRINT BACK IN THE QUEUE. The jobs already exist; they go back
//     to 'queued' and through batching again.
//
// EACH STEP IS RECORDED WHETHER IT WORKED OR NOT. Any one can fail while the
// others succeed, and a report whose courier was booked but whose customer was
// never told has to be distinguishable from one where both worked - which a
// single status cannot do. The stage rows are what the drawer draws.
//
// The steps are NOT a transaction, and cannot be: a booked courier is not
// something a database rollback can undo. So each is recorded as it lands, and
// a failure stops the ones that depend on it rather than unwinding the ones
// that already happened.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/damage"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// reprintOutcome is what the caller reports back to the screen.
type reprintOutcome struct {
	// Waybill is Delhivery's number for the replacement parcel, when one was
	// booked.
	Waybill string `json:"waybill"`
	// MessageSent says whether the customer actually heard. Its own field
	// rather than an inference: WhatsApp refuses free-form outside its
	// 24-hour window, so a perfectly booked reprint can leave somebody
	// untold, and the screen has to be able to say so.
	MessageSent bool `json:"message_sent"`
	// Requeued is how many production jobs went back in the queue.
	Requeued int `json:"requeued"`
	// Blocked carries the step that stopped this, if one did.
	Blocked string `json:"blocked,omitempty"`
}

// Complete reports whether every step the shop controls actually worked.
//
// What "resolved" is allowed to mean. A reprint whose courier was refused and
// whose customer was never told has had a DECISION taken about it and nothing
// else, and a board calling that resolved hides the row that most needs
// somebody.
func (o reprintOutcome) Complete() bool {
	return o.Blocked == "" && o.Waybill != "" && o.MessageSent
}

// runReprint books the courier, tells the customer and re-queues the print.
//
// Returns no error: every failure is recorded against the report and reported
// to the screen instead. The DECISION has already been stored and two people
// can see it on the board, so failing the request here would leave them
// looking at a reprint the API says never happened.
func (s *Server) runReprint(
	ctx context.Context, brand string, report gen.DamageReport, actor string,
) reprintOutcome {
	log := obs.FromContext(ctx)
	var out reprintOutcome

	// ---- 1. the courier -------------------------------------------------
	booked := false
	booking, err := s.bookReplacementCourier(ctx, brand, report)
	if err != nil {
		s.recordStage(ctx, brand, report.ID, damage.StageCourierBooked,
			damage.StateFailed, err.Error(), actor)
		log.Warn("could not book a courier for a reprint",
			"brand", brand, "report", report.ID, "error", err)
		out.Blocked = "Could not book the courier: " + err.Error()
	} else {
		booked = true
		out.Waybill = booking.Waybill
		s.recordStage(ctx, brand, report.ID, damage.StageCourierBooked,
			damage.StateDone, "Waybill "+booking.Waybill, actor)
		if err := s.store.Q.SetDamagePickupWaybill(ctx, gen.SetDamagePickupWaybillParams{
			ID: report.ID, BrandSlug: brand, PickupWaybill: booking.Waybill,
		}); err != nil {
			// Recorded in the stage row either way, so this is a tidiness
			// failure rather than a lost fact.
			log.Warn("could not store the reprint waybill on the report",
				"report", report.ID, "error", err)
		}
	}

	// ---- 2. the customer, ONLY on a booking -----------------------------
	//
	// THE MESSAGE IS WHAT WAITS ON THE COURIER, and only the message. A
	// promise to send a replacement is made once there is a courier to send
	// it with; without one the customer is told nothing and a person picks it
	// up from the board.
	switch {
	case !booked:
		s.recordStage(ctx, brand, report.ID, damage.StageCustomerTold, damage.StateSkipped,
			"Not told: no courier was booked, so there is nothing to promise yet", actor)
	default:
		out.MessageSent = s.tellCustomer(ctx, brand, report,
			s.noticeText(ctx, brand, noticeReprintScheduled))
		if out.MessageSent {
			s.recordStage(ctx, brand, report.ID, damage.StageCustomerTold,
				damage.StateDone, "", actor)
		} else {
			s.recordStage(ctx, brand, report.ID, damage.StageCustomerTold, damage.StateFailed,
				"WhatsApp would not take the message - most likely outside the 24-hour window", actor)
		}
	}

	// ---- 3. the print ----------------------------------------------------
	//
	// ATTEMPTED WHATEVER THE OTHER TWO DID. The plank has to be made either
	// way: a courier can be booked by hand afterwards and a customer can be
	// rung, but a replacement nobody started printing is days behind before
	// anyone notices. Leaving it unqueued would make a courier failure cost a
	// week rather than a phone call.
	requeued, err := s.requeueForReprint(ctx, report)
	if err != nil {
		s.recordStage(ctx, brand, report.ID, damage.StageJobQueued,
			damage.StateFailed, err.Error(), actor)
		log.Warn("could not re-queue the print for a reprint",
			"report", report.ID, "error", err)
		out.Blocked = appendBlocked(out.Blocked, "Could not re-queue the print: "+err.Error())
		return out
	}
	out.Requeued = requeued
	detail := fmt.Sprintf("%d job(s) back in the queue", requeued)
	if requeued == 0 {
		// Not a failure and not success: the order's jobs may still be in
		// production, which is a perfectly ordinary thing to find and not
		// something to re-queue underneath somebody.
		detail = "nothing to re-queue - this order's jobs are not finished"
	}
	s.recordStage(ctx, brand, report.ID, damage.StageJobQueued, damage.StateDone, detail, actor)
	return out
}

// bookReplacementCourier asks Delhivery for a forward shipment to the address
// the original order went to.
func (s *Server) bookReplacementCourier(
	ctx context.Context, brand string, report gen.DamageReport,
) (delhivery.Booking, error) {
	// PER BRAND, like WhatsApp: each store has its own carrier account, and a
	// shared client would book one brand's parcel on another's bill.
	carrier := s.delhiveryFor(ctx, brand)
	if carrier == nil || !carrier.Configured() {
		return delhivery.Booking{}, fmt.Errorf(
			"Delhivery is not configured for this brand")
	}
	if report.OrderID == nil {
		// The customer typed an order number that was never matched to an
		// imported order, so there is no address to send anything to.
		return delhivery.Booking{}, fmt.Errorf(
			"this report is not linked to an imported order, so there is no address to send to")
	}
	order, err := s.store.Q.GetOrderByID(ctx, *report.OrderID)
	if err != nil {
		return delhivery.Booking{}, fmt.Errorf("read the order: %w", err)
	}

	to, err := recipientFromOrder(order)
	if err != nil {
		return delhivery.Booking{}, err
	}
	return carrier.CreateForwardShipment(ctx, s.cfg.DelhiveryPickupLocation,
		delhivery.ForwardShipment{
			// The ORIGINAL order number, with a suffix. Delhivery refuses a
			// duplicate order id, and the first parcel already used the bare
			// one - but a number with no relation to the order would make the
			// replacement untraceable from the order it replaces.
			OrderNumber:   order.OrderNumber + "-RP",
			To:            to,
			Description:   "Replacement for a damaged order",
			DeclaredValue: "0",
			Pieces:        1,
		})
}

// recipientFromOrder reads the delivery address off the stored order.
//
// Shipping first, billing second. A gift goes to the recipient, and the
// replacement has to follow the original parcel rather than the card.
func recipientFromOrder(order gen.Order) (delhivery.Recipient, error) {
	addr := firstUsableAddress(order.ShippingAddress, order.BillingAddress)
	if addr == nil {
		return delhivery.Recipient{}, fmt.Errorf(
			"this order carries no delivery address, so no courier can be booked for it")
	}

	// The phone comes off the ORDER when the address has none. Delhivery
	// refuses a shipment without one, and this store's addresses frequently
	// arrive with the field empty while the order itself carries the number.
	phone := strings.TrimSpace(addr.Phone)
	if phone == "" {
		phone = strings.TrimSpace(deref(order.CustomerPhone))
	}
	name := strings.TrimSpace(addr.Name)
	if name == "" {
		name = strings.TrimSpace(deref(order.CustomerName))
	}

	return delhivery.Recipient{
		Name:    name,
		Phone:   phone,
		Address: strings.TrimSpace(strings.TrimSpace(addr.Address1) + " " + strings.TrimSpace(addr.Address2)),
		City:    addr.City,
		State:   addr.Province,
		Pincode: addr.Zip,
		Country: addr.Country,
	}, nil
}

// firstUsableAddress returns the first address that has somewhere to deliver
// to. An address row with a name and nothing else is not one.
func firstUsableAddress(raw ...[]byte) *shopify.OrderAddress {
	for _, b := range raw {
		if len(b) == 0 {
			continue
		}
		var addr shopify.OrderAddress
		if err := json.Unmarshal(b, &addr); err != nil {
			continue
		}
		if strings.TrimSpace(addr.Address1) == "" && strings.TrimSpace(addr.Zip) == "" {
			continue
		}
		return &addr
	}
	return nil
}

// requeueForReprint puts the order's finished jobs back in the print queue.
func (s *Server) requeueForReprint(ctx context.Context, report gen.DamageReport) (int, error) {
	if report.OrderID == nil {
		return 0, fmt.Errorf("this report is not linked to an imported order")
	}
	rows, err := s.store.Q.RequeueJobsForOrder(ctx, report.OrderID)
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}

// tellCustomer sends one free-form message and says whether it went.
func (s *Server) tellCustomer(
	ctx context.Context, brand string, report gen.DamageReport, body string,
) bool {
	log := obs.FromContext(ctx)
	wa := s.whatsappFor(ctx, brand)
	if wa == nil || !wa.Configured() {
		log.Warn("WhatsApp is not configured for this brand",
			"brand", brand, "report", report.ID)
		return false
	}
	if _, err := wa.SendText(ctx, report.WaID, body); err != nil {
		log.Warn("could not message the customer about their damage report",
			"brand", brand, "report", report.ID, "error", err)
		return false
	}
	return true
}

// recordStage writes one step, and advances the report when it was a success.
//
// A FAILURE IS RECORDED BUT DOES NOT ADVANCE THE REPORT. The timeline shows
// what was attempted; the board shows how far it actually got, and a failed
// courier must not make a report read as though it had one.
func (s *Server) recordStage(
	ctx context.Context, brand string, id uuid.UUID,
	stage damage.Stage, state damage.State, detail, actor string,
) {
	log := obs.FromContext(ctx)
	var who *string
	if strings.TrimSpace(actor) != "" {
		who = &actor
	}
	if err := s.store.Q.RecordDamageStage(ctx, gen.RecordDamageStageParams{
		ReportID: id, Stage: string(stage), State: string(state),
		Detail: detail, Actor: who,
	}); err != nil {
		log.Warn("could not record a damage stage",
			"report", id, "stage", stage, "error", err)
		return
	}
	if state != damage.StateDone {
		return
	}
	if err := s.store.Q.SetDamageStage(ctx, gen.SetDamageStageParams{
		ID: id, BrandSlug: brand, Stage: string(stage),
	}); err != nil {
		log.Warn("could not advance a damage report's stage",
			"report", id, "stage", stage, "error", err)
	}
}

// damageStageJSON is one row of the drawer's timeline.
type damageStageJSON struct {
	Stage string `json:"stage"`
	// Label is what the operator reads - "Courier booked", not
	// "COURIER_BOOKED".
	Label string `json:"label"`
	// State is done | failed | skipped | pending. PENDING is not a stored
	// value: it is a step in the plan that has no row yet, which is how the
	// drawer knows what is still to come.
	State  string `json:"state"`
	Detail string `json:"detail"`
	Actor  string `json:"actor,omitempty"`
	At     string `json:"at,omitempty"`
}

// damageTimeline is every step this report should pass through, with what
// happened at each.
//
// Built from the PLAN rather than from the rows, so a report that stalled
// three steps in still shows the three it has not reached. Rows that are not
// in the plan - a step recorded before somebody changed their mind about the
// route - are appended at the end rather than dropped, because a thing that
// happened should not vanish from the record of what happened.
func (s *Server) damageTimeline(ctx context.Context, row gen.DamageReport) []damageStageJSON {
	log := obs.FromContext(ctx)
	rows, err := s.store.Q.ListDamageStages(ctx, row.ID)
	if err != nil {
		log.Warn("could not read a damage report's stages", "report", row.ID, "error", err)
		rows = nil
	}

	// LAST ROW PER STAGE WINS for what is shown, because a retry that
	// succeeded is the current truth - while the failed attempt stays in the
	// table for whoever asks later why it took two goes.
	latest := map[string]gen.DamageStage{}
	for _, r := range rows {
		latest[r.Stage] = r
	}

	// THE REPORT EXISTS, SO IT WAS REPORTED. Nothing writes that step - the
	// row itself is the evidence - and leaving it grey makes the one thing
	// certainly true look like the one thing still pending.
	if _, ok := latest[string(damage.StageReported)]; !ok {
		latest[string(damage.StageReported)] = gen.DamageStage{
			Stage: string(damage.StageReported),
			State: string(damage.StateDone),
			At:    row.ReportedAt,
		}
	}

	out := make([]damageStageJSON, 0, len(rows)+2)
	seen := map[string]bool{}
	for _, step := range damage.Plan(damage.Kind(row.ResolutionKind)) {
		seen[string(step.Stage)] = true
		entry := damageStageJSON{
			Stage: string(step.Stage), Label: step.Label, State: "pending",
		}
		if r, ok := latest[string(step.Stage)]; ok {
			entry.State, entry.Detail = r.State, r.Detail
			entry.Actor = deref(r.Actor)
			entry.At = r.At.Time.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	for _, r := range rows {
		if seen[r.Stage] {
			continue
		}
		seen[r.Stage] = true
		out = append(out, damageStageJSON{
			Stage: r.Stage, Label: r.Stage, State: r.State, Detail: r.Detail,
			Actor: deref(r.Actor), At: r.At.Time.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// appendBlocked joins two reasons, so a report that failed at two steps says
// both rather than only the last one somebody happened to write.
func appendBlocked(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + " " + next
}
