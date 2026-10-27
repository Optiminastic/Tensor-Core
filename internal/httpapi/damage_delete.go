package httpapi

// Removing a damage report from the board.
//
// A DELETE, NOT AN ARCHIVE, because what this is actually for is clearing
// test rows and mistakes off a working queue - and a board that fills with
// things nobody will action is a board people stop reading.
//
// ADMIN ONLY. Support decides what the shop does about a complaint; erasing
// the record that a customer ever complained is a different act, so it is a
// different permission - see auth.DamageDelete.
//
// IT REFUSES WHEN A COURIER IS ALREADY COMING. That is the one case where the
// row is not just a row: a van is booked against the shop's account, and this
// report is the only place the waybill is written down. Deleting it would
// leave somebody to find the charge on an invoice weeks later with nothing to
// match it to. The refusal names the waybill so it can be cancelled first.

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/damage"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

func (s *Server) registerDamageDelete(g *gin.RouterGroup) {
	g.DELETE("/:id",
		s.guards.RequirePermission(auth.DamageDelete.Key()), s.deleteDamageReport)
}

func (s *Server) deleteDamageReport(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	report, err := s.store.Q.GetDamageReport(ctx, gen.GetDamageReportParams{
		ID: id, BrandSlug: brand,
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That damage report does not exist.")
		return
	}
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not read the report.")
		return
	}

	// READ BEFORE DELETING, so the refusal can name the waybill. Checking
	// after would mean the row is already gone when we discover we should
	// have kept it.
	if waybill := s.bookedCourierFor(ctx, report); waybill != "" {
		detail(c, http.StatusConflict,
			"A courier is already booked for this report ("+waybill+"). Cancel that "+
				"waybill at Delhivery first - deleting this now would leave the "+
				"booking with nothing to match it to.")
		return
	}

	rows, err := s.store.Q.DeleteDamageReport(ctx, gen.DeleteDamageReportParams{
		ID: id, BrandSlug: brand,
	})
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not delete the report.")
		return
	}
	if rows == 0 {
		// Somebody deleted it between the read and the write. The outcome is
		// the one asked for, so this is not an error to report.
		c.Status(http.StatusNoContent)
		return
	}

	// LOUD, because nothing else records that this existed. The report is
	// gone and its timeline went with it; this line is the only trace left of
	// a customer complaint that was removed, and who removed it.
	obs.FromContext(ctx).Info("damage report deleted",
		"brand", brand, "report", id, "order", report.OrderNumber,
		"status", report.Status, "resolution", report.ResolutionKind,
		"by", currentUserID(c))

	c.Status(http.StatusNoContent)
}

// bookedCourierFor returns the waybill of a courier already on its way, or ""
// when nothing has been booked.
//
// TWO PLACES TO LOOK, because the two routes record it differently. A reverse
// pickup stores its waybill on the report; a reprint's lives only in the
// detail of its timeline step, which is why this reads the stages at all.
func (s *Server) bookedCourierFor(ctx context.Context, report gen.DamageReport) string {
	if w := strings.TrimSpace(report.PickupWaybill); w != "" {
		return w
	}
	stages, err := s.store.Q.ListDamageStages(ctx, report.ID)
	if err != nil {
		// Unknown rather than none. Refusing to delete on a failed read would
		// be safer still, but it would also make the button fail for a reason
		// nobody can act on; the reverse waybill above is the case that
		// actually costs money twice.
		obs.FromContext(ctx).Warn("could not read a report's stages before deleting",
			"report", report.ID, "error", err)
		return ""
	}
	for _, st := range stages {
		if damage.Stage(st.Stage) != damage.StageCourierBooked {
			continue
		}
		if damage.State(st.State) != damage.StateDone {
			continue
		}
		// The detail reads "Waybill 69187910002321"; the number is what a
		// person needs to cancel it.
		if w := strings.TrimSpace(strings.TrimPrefix(st.Detail, "Waybill")); w != "" {
			return w
		}
		return "already booked"
	}
	return ""
}
