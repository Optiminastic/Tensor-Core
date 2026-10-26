package httpapi

// Deciding what happens to a damaged parcel.
//
// Three outcomes, and they are not alike:
//
//	REPRINT - we make it again and send it, at our cost. The customer pays
//	          nothing, returns nothing and arranges nothing, and the message
//	          below says so in as many words.
//	REVERSE - the customer pays Rs 100 for the leg to our warehouse, we repair
//	          it, and we pay the leg home. That is a negotiation spread over
//	          days, so choosing it only ASKS - see internal/damage.
//	CLOSED  - support settled it in conversation. Nothing is made, sent or
//	          collected, and the note is the whole record, so it is the one
//	          outcome that refuses an empty one.
//
// BOTH ADMIN AND SUPPORT may decide, at the shop's instruction. The person
// reading the report is the person who should be able to act on it, and a
// queue where every row waits for an admin is a queue that waits.
//
// The decision is made ONCE. SetDamageResolution compares-and-sets on an empty
// resolution_kind, so two people on the same board - or one person
// double-clicking - cannot both send the customer a message about it.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/damage"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxResolutionNote is how long the note beside a decision may be.
//
// A line or two about WHY, for the next person to read - not a case file. The
// column is text, so this is a kindness to the reader rather than a storage
// limit.
const maxResolutionNote = 500

type damageResolutionRequest struct {
	// Kind is "reprint", "reverse" or "closed". Validated against damage.Kind
	// rather than a binding tag, so the refusal names the three that exist.
	Kind string `json:"kind" binding:"required"`
	// Note is the deciding person's short note. INTERNAL: it is stored on the
	// report and never sent to the customer, who gets the fixed message below.
	Note string `json:"note"`
}

func (s *Server) registerDamageResolution(g *gin.RouterGroup) {
	g.POST("/:id/resolution",
		s.guards.RequirePermission(auth.DamageResolve.Key()), s.resolveDamageReport)
}

func (s *Server) resolveDamageReport(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}

	var req damageResolutionRequest
	if !bindJSON(c, &req) {
		return
	}
	kind := damage.Kind(strings.ToLower(strings.TrimSpace(req.Kind)))
	if !kind.Valid() {
		detail(c, http.StatusUnprocessableEntity,
			"Choose what to do about this report: reprint it at our cost, ask for the "+
				"reverse pickup, or close it if support has already sorted it.")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > maxResolutionNote {
		detail(c, http.StatusUnprocessableEntity,
			"That note is too long. A line or two about why is what the next person needs.")
		return
	}
	// CLOSING WITHOUT SAYING WHAT WAS DONE IS NOT CLOSING IT.
	//
	// A reprint leaves a waybill and a print job behind it and a reverse
	// leaves a payment; both say what happened without anybody writing it
	// down. A closed report leaves one sentence, and an empty one means the
	// shop has no record of what it agreed with a customer who complained.
	if kind.NeedsNote() && note == "" {
		detail(c, http.StatusUnprocessableEntity,
			"Say what was done. A closed report's note is the only record of how "+
				"it was sorted with the customer.")
		return
	}

	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	// The reverse route OPENS with the offer; the reprint route has no further
	// state to be in. Advance decides which, so the rule lives in one place
	// and this handler does not re-state it.
	//
	// IN REVIEW UNTIL THE WORK ACTUALLY LANDS.
	//
	// This used to resolve a reprint the moment it was chosen, which was
	// honest while choosing WAS the whole action. It is not any more: a
	// reprint now books a courier, messages the customer and re-queues the
	// print, and any of those can fail. A report whose courier was refused
	// and whose customer was never told has had a decision taken about it and
	// nothing else - calling that resolved hides the row that most needs
	// somebody. It is promoted below, once the steps report back.
	reverse := damage.StateNone
	status := damageInReview
	switch kind {
	case damage.KindReverse:
		next, _, err := damage.Advance(damage.Report{}, damage.EventOffer)
		if err != nil {
			detail(c, http.StatusConflict, err.Error())
			return
		}
		reverse = next
	case damage.KindClosed:
		// RESOLVED THE MOMENT IT IS CHOSEN, unlike the other two.
		//
		// They are in review because work follows them that can fail - a
		// courier, a message, a print job - and a report whose courier was
		// refused is not resolved however confidently somebody clicked. A
		// closed report has no work after it. The conversation already
		// happened; this is somebody writing down that it did.
		status = damageResolved
	}

	by := currentUserID(c)
	report, err := s.store.Q.SetDamageResolution(ctx, gen.SetDamageResolutionParams{
		ID: id, BrandSlug: brand,
		ResolutionKind: string(kind),
		ReverseState:   string(reverse),
		IsOffer:        reverse == damage.StateOffered,
		NextStatus:     status,
		DecidedBy:      &by,
	})
	if isNoRows(err) {
		// Either the report is not this brand's, or somebody already decided.
		// Told apart by reading it back, because "already decided as a
		// reprint" is a different thing to do next from "no such report".
		s.refuseAlreadyDecided(c, id, brand)
		return
	}
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not record the decision.")
		return
	}

	// The note is written separately and is allowed to fail quietly. The
	// decision is the thing that matters and it is already stored; losing a
	// note is a worse outcome to turn into a 500 than to log.
	if note != "" {
		if _, err := s.store.Q.UpdateDamageReportStatus(ctx, gen.UpdateDamageReportStatusParams{
			ID: id, BrandSlug: brand,
			ResolutionNote:  &note,
			StatusChangedBy: &by,
		}); err != nil {
			log.Warn("could not store the note beside a damage decision",
				"report", id, "error", err)
		} else {
			report.ResolutionNote = note
		}
	}

	s.recordStage(ctx, brand, report.ID, damage.StageDecided, damage.StateDone,
		"Chose "+humanKind(string(kind)), by)

	// The note is the closed route's whole second step, so it is what the
	// timeline entry says. Recorded here rather than in a branch below
	// because there is no work to report back from.
	if kind == damage.KindClosed {
		s.recordStage(ctx, brand, report.ID, damage.StageClosed, damage.StateDone, note, by)
	}

	// CARRYING IT OUT IS BEST-EFFORT, and the decision stands either way.
	//
	// A courier that refuses, or a message that will not send, must not undo a
	// decision two people can already see on the board - that would leave the
	// report undecided while the person who decided it believes otherwise.
	// Each step records itself instead, and the screen reports what happened.
	//
	// A REVERSE sends ONE message: the Rs 100 ask with the QR attached. There
	// is no second step where the customer agrees, because paying is
	// agreeing - so this is the only thing the shop does until the money
	// turns up, and a reverse that sent nothing would be a report claiming
	// the customer was asked when they never were.
	var out reprintOutcome
	switch kind {
	case damage.KindReverse:
		out.MessageSent = s.askForReversePayment(ctx, brand, report)
		state := damage.StateDone
		if !out.MessageSent {
			// Recorded as failed rather than skipped: nobody chose not to
			// ask, and this is the step the whole route is now waiting on.
			state = damage.StateFailed
			out.Blocked = "could not ask the customer for the ₹100"
		}
		s.recordStage(ctx, brand, report.ID, damage.StageCustomerTold, state,
			"Asked for the ₹100 with the payment QR", by)
	case damage.KindReprint:
		out = s.runReprint(ctx, brand, report, by)
		// Resolved only when every step the shop controls worked. Printing
		// and dispatch are still ahead, and they are the floor's business
		// rather than this queue's: once the courier is booked, the customer
		// told and the print re-queued, there is nothing here for anybody to
		// come back to.
		if out.Complete() {
			if _, err := s.store.Q.UpdateDamageReportStatus(ctx, gen.UpdateDamageReportStatusParams{
				ID: id, BrandSlug: brand,
				Status: ptr(damageResolved), StatusChangedBy: &by,
			}); err != nil {
				obs.FromContext(ctx).Warn("could not resolve a completed reprint",
					"report", id, "error", err)
			}
		}
	}

	// Re-read, because runReprint moved the stage and may have stored a
	// waybill. Returning the pre-flight row would show the screen a report
	// that had not done any of it.
	if fresh, err := s.store.Q.GetDamageReport(ctx, gen.GetDamageReportParams{
		ID: id, BrandSlug: brand,
	}); err == nil {
		report = fresh
	}

	c.JSON(http.StatusOK, gin.H{
		"report":       damageJSON(report, 0),
		"message_sent": out.MessageSent,
		"waybill":      out.Waybill,
		"requeued":     out.Requeued,
		"blocked":      out.Blocked,
	})
}

// refuseAlreadyDecided says which of the two reasons the write found no row.
func (s *Server) refuseAlreadyDecided(c *gin.Context, id uuid.UUID, brand string) {
	existing, err := s.store.Q.GetDamageReport(c.Request.Context(), gen.GetDamageReportParams{
		ID: id, BrandSlug: brand,
	})
	if err != nil {
		detail(c, http.StatusNotFound, "That damage report does not exist.")
		return
	}
	detail(c, http.StatusConflict, "This report was already set to "+
		humanKind(existing.ResolutionKind)+". Reopen it if that was wrong.")
}

// humanKind names a stored kind the way the message above reads.
func humanKind(kind string) string {
	switch damage.Kind(kind) {
	case damage.KindReprint:
		return "a reprint"
	case damage.KindReverse:
		return "a reverse pickup"
	case damage.KindClosed:
		return "to close it"
	default:
		return "something else"
	}
}
