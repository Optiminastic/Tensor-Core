package httpapi

// The two steps a person takes on a reverse pickup, after the customer has
// been asked to pay.
//
//	CONFIRM PAYMENT - somebody looked at the money and says it arrived.
//	BOOK PICKUP     - somebody sent a van to the customer's door.
//
// THEY ARE TWO CLICKS, at the shop's instruction, and the second is the
// reason. Confirming a payment is reversible - a person who ticked the wrong
// row can say so. Booking is not: it costs a courier and puts a van outside
// somebody's house. Collapsing them would make the expensive act a side
// effect of the cheap one, on a board where the rows look alike.
//
// Neither decides anything. damage.Advance owns which transitions exist and
// refuses the rest in words an operator can act on; these handlers carry out
// what it allows and record what happened.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/damage"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

func (s *Server) registerDamageReverse(g *gin.RouterGroup) {
	// The same permission as the decision: whoever may choose a reverse
	// pickup is whoever may carry it out. A separate permission would mean a
	// support person could start a negotiation they then could not finish.
	g.POST("/:id/payment",
		s.guards.RequirePermission(auth.DamageResolve.Key()), s.confirmDamagePayment)
	g.POST("/:id/pickup",
		s.guards.RequirePermission(auth.DamageResolve.Key()), s.bookDamagePickup)
}

// confirmDamagePayment records that the Rs 100 arrived.
//
// A SCREENSHOT IS NOT REQUIRED. Advance allows this from "asked" as well as
// from "they sent proof", because a customer may pay and never send a
// picture, and support who can see the money in the bank should not be
// blocked by one. The screenshot is evidence, not a gate.
func (s *Server) confirmDamagePayment(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	report, ok := s.reverseReportFor(c, id, brand)
	if !ok {
		return
	}
	if _, _, err := damage.Advance(reportState(report), damage.EventConfirmPayment); err != nil {
		// Advance's refusals name the state and what is waiting on what, and
		// they are the whole explanation the operator gets.
		detail(c, http.StatusConflict, err.Error())
		return
	}

	by := currentUserID(c)
	updated, err := s.store.Q.ConfirmDamagePayment(ctx, gen.ConfirmDamagePaymentParams{
		ID: id, BrandSlug: brand, ConfirmedBy: &by,
	})
	if isNoRows(err) {
		// The guard above passed and the write still matched nothing, so
		// somebody moved it in between. Re-read rather than guess.
		s.refuseStaleReverse(c, id, brand)
		return
	}
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not confirm the payment.")
		return
	}

	s.recordStage(ctx, brand, id, damage.StagePaymentConfirmed, damage.StateDone,
		"Rs 100 confirmed received", by)
	c.JSON(http.StatusOK, gin.H{"report": damageJSON(updated, 0)})
}

// bookDamagePickup sends the courier to the customer's door.
//
// THE ONE ACT HERE THAT SPENDS MONEY AND CANNOT BE TAKEN BACK. Everything
// before it is a row in a database.
//
// The courier is booked BEFORE the report is moved, so a refusal leaves the
// report exactly where it was and somebody can try again. Writing
// pickup_booked first and then failing to book would leave a report that says
// a van is coming when none is.
func (s *Server) bookDamagePickup(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	report, ok := s.reverseReportFor(c, id, brand)
	if !ok {
		return
	}
	if _, _, err := damage.Advance(reportState(report), damage.EventBookPickup); err != nil {
		detail(c, http.StatusConflict, err.Error())
		return
	}

	by := currentUserID(c)
	booking, err := s.bookReversePickupCourier(ctx, brand, report)
	if err != nil {
		// Recorded as a failed stage, which is what makes damage.Blocked able
		// to name this as the step everyone is waiting on.
		s.recordStage(ctx, brand, id, damage.StagePickupBooked, damage.StateFailed, err.Error(), by)
		log.Error("damage: could not book the reverse pickup",
			"report", id, "error", err)
		detail(c, http.StatusBadGateway, "Could not book the pickup: "+err.Error())
		return
	}

	updated, err := s.store.Q.BookDamagePickup(ctx, gen.BookDamagePickupParams{
		ID: id, BrandSlug: brand, PickupWaybill: booking.Waybill, BookedBy: &by,
	})
	if isNoRows(err) {
		// THE COURIER IS ALREADY BOOKED. Somebody moved the report between
		// the guard and the write, and a van is now coming for a parcel this
		// report no longer expects. Loud, and with the waybill, because the
		// only way back is a person cancelling it at Delhivery.
		log.Error("damage: a reverse pickup was booked but the report had moved",
			"report", id, "waybill", booking.Waybill)
		s.recordStage(ctx, brand, id, damage.StagePickupBooked, damage.StateFailed,
			"Booked "+booking.Waybill+" but the report had already moved on; cancel it at Delhivery", by)
		detail(c, http.StatusConflict,
			"A pickup was booked with Delhivery ("+booking.Waybill+") but this report had "+
				"already moved on. Cancel that waybill at Delhivery before trying again.")
		return
	}
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not record the pickup.")
		return
	}

	s.recordStage(ctx, brand, id, damage.StagePickupBooked, damage.StateDone,
		"Pickup booked, waybill "+booking.Waybill, by)

	// Told AFTER the booking is recorded, not before. The customer's one
	// instruction is to keep the product packed, and telling them that about
	// a van we failed to record would be a promise the board cannot see.
	// Best-effort: the van is coming either way, and a message that will not
	// send must not undo a booking.
	sent := s.tellCustomer(ctx, brand, updated,
		s.noticeText(ctx, brand, noticeReversePickupBooked))
	if sent {
		s.recordStage(ctx, brand, id, damage.StageCustomerTold, damage.StateDone,
			"Told the customer the pickup is booked", by)
	}

	c.JSON(http.StatusOK, gin.H{
		"report":       damageJSON(updated, 0),
		"waybill":      booking.Waybill,
		"message_sent": sent,
	})
}

// bookReversePickupCourier asks Delhivery to collect from the address the
// original order was delivered to.
func (s *Server) bookReversePickupCourier(
	ctx context.Context, brand string, report gen.DamageReport,
) (delhivery.Booking, error) {
	carrier := s.delhiveryFor(ctx, brand)
	if carrier == nil || !carrier.Configured() {
		return delhivery.Booking{}, fmt.Errorf("Delhivery is not configured for this brand")
	}
	if report.OrderID == nil {
		return delhivery.Booking{}, fmt.Errorf(
			"this report is not linked to an imported order, so there is no address to collect from")
	}
	order, err := s.store.Q.GetOrderByID(ctx, *report.OrderID)
	if err != nil {
		return delhivery.Booking{}, fmt.Errorf("read the order: %w", err)
	}
	// The same address the parcel went to, read the same way the reprint
	// reads it - the broken product is wherever the original was delivered.
	from, err := recipientFromOrder(order)
	if err != nil {
		return delhivery.Booking{}, err
	}
	return carrier.CreateReverseShipment(ctx, s.cfg.DelhiveryPickupLocation,
		delhivery.ReverseShipment{
			// Suffixed, like the reprint's "-RP". Delhivery refuses a
			// duplicate order id and the original parcel used the bare one,
			// but a reference unrelated to the order would make the
			// collection untraceable from the order it undoes.
			OrderNumber:   order.OrderNumber + "-RV",
			From:          from,
			Description:   "Damaged product returning for repair",
			DeclaredValue: "0",
			Pieces:        1,
		})
}

// askForReversePayment sends the whole Rs 100 ask in one message.
//
// THE QR IS THE MESSAGE, with everything else as its caption: the price, the
// terms and the thing you scan arrive together and cannot be scrolled apart.
// A customer who reads "pay Rs 100" three messages above the QR is a customer
// who pays the wrong amount.
//
// Falls back to text when the service has no public base URL to serve the QR
// from. It still ASKS - dropping the message because an image is missing
// would leave a report saying the customer was asked and a customer who was
// not, and somebody can always send the QR by hand.
//
// Best-effort, like every other customer message in this flow: the decision
// is already stored and two people can see it, and a send that fails must not
// undo it. The caller records what happened.
func (s *Server) askForReversePayment(
	ctx context.Context, brand string, report gen.DamageReport,
) bool {
	log := obs.FromContext(ctx)
	wa := s.whatsappFor(ctx, brand)
	if wa == nil || !wa.Configured() {
		log.Warn("WhatsApp is not configured for this brand",
			"brand", brand, "report", report.ID)
		return false
	}

	// Both wordings read together, so the fallback below cannot send the old
	// text while the QR version sends the new one.
	said := s.noticesFor(ctx, brand)

	qr := s.payQRURL()
	if qr == "" {
		log.Warn("no public base URL, so the reverse QR cannot be sent as an image",
			"brand", brand, "report", report.ID)
		return s.tellCustomer(ctx, brand, report, said.text(noticeReversePaymentNoQR))
	}
	if _, err := wa.SendImage(ctx, report.WaID, qr, said.text(noticeReversePaymentAsk)); err != nil {
		log.Warn("could not ask the customer for the reverse pickup payment",
			"brand", brand, "report", report.ID, "error", err)
		return false
	}
	return true
}

// reverseReportFor reads a report and refuses anything that is not a reverse.
func (s *Server) reverseReportFor(
	c *gin.Context, id uuid.UUID, brand string,
) (gen.DamageReport, bool) {
	report, err := s.store.Q.GetDamageReport(c.Request.Context(),
		gen.GetDamageReportParams{ID: id, BrandSlug: brand})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That damage report does not exist.")
		return gen.DamageReport{}, false
	}
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not read the report.")
		return gen.DamageReport{}, false
	}
	return report, true
}

// reportState is the stored report as the rules read it.
func reportState(r gen.DamageReport) damage.Report {
	return damage.Report{
		Kind:    damage.Kind(r.ResolutionKind),
		Reverse: damage.ReverseState(r.ReverseState),
	}
}

// refuseStaleReverse re-reads a report whose guarded write matched nothing and
// says what it is now, rather than reporting a generic conflict.
func (s *Server) refuseStaleReverse(c *gin.Context, id uuid.UUID, brand string) {
	report, err := s.store.Q.GetDamageReport(c.Request.Context(),
		gen.GetDamageReportParams{ID: id, BrandSlug: brand})
	if err != nil {
		detail(c, http.StatusConflict, "This report moved on while you were looking at it.")
		return
	}
	detail(c, http.StatusConflict, "This report is "+
		damage.Describe(damage.ReverseState(report.ReverseState))+
		". Somebody else may have just acted on it.")
}
