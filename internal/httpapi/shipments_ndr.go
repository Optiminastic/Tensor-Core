package httpapi

// Acting on a parcel Delhivery could not deliver.
//
// READING a shipment is order:read - it is an order's parcel, and whoever may
// see the order may see where it got to. ACTING on one is dispatch:manage:
// re-attempting a delivery sends a van out again and editing an address
// changes where somebody's parcel goes, which is the shop spending money and
// making a promise rather than looking something up.
//
// SUBMITTED, NOT DONE. Delhivery takes these instructions asynchronously: the
// response carries an upload id and nothing about the parcel. So this reports
// what was asked for and says plainly that it was asked, and the screen is
// written to match. Reading an accepted instruction as a completed one is the
// mistake that left a customer waiting for a payment message that never came.
//
// NO RETURN TO ORIGIN. Delhivery's own action list is re-attempt, defer and
// edit; RTO lives in third-party wrappers around this endpoint, not in the
// endpoint. A Return button here would be a button that does nothing.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxWaybillLen bounds what may appear in the path segment: a waybill and
// nothing else.
const maxWaybillLen = 40

type ndrActionRequest struct {
	// Act is "reattempt", "defer" or "edit", in the screen's words rather
	// than Delhivery's - the API's own spelling (RE-ATTEMPT, DEFER_DLV,
	// EDIT_DETAILS) is this package's business, not the frontend's.
	Act string `json:"act" binding:"required"`
	// Date is the new delivery date for a defer, as YYYY-MM-DD.
	Date string `json:"date"`
	// Name, Address and Phone are the corrections for an edit. Blank fields
	// are left alone rather than blanked.
	Name    string `json:"name"`
	Address string `json:"address"`
	Phone   string `json:"phone"`
}

func (s *Server) registerShipmentNDR(g *gin.RouterGroup) {
	g.POST("/:waybill/ndr",
		s.guards.RequirePermission(auth.DispatchManage.Key()), s.actOnNDR)
}

func (s *Server) actOnNDR(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	waybill := strings.TrimSpace(c.Param("waybill"))
	if waybill == "" || len(waybill) > maxWaybillLen {
		detail(c, http.StatusUnprocessableEntity, "That is not a waybill.")
		return
	}

	var req ndrActionRequest
	if !bindJSON(c, &req) {
		return
	}
	instruction, refusal := ndrInstruction(waybill, req)
	if refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}

	ctx := c.Request.Context()
	carrier := s.delhiveryFor(ctx, brand)
	if carrier == nil || !carrier.Configured() {
		detail(c, http.StatusServiceUnavailable,
			"Delhivery is not connected for this brand, so nothing can be sent to the courier.")
		return
	}

	res, err := carrier.UpdateNDR(ctx, instruction)
	if err != nil {
		// Delhivery's refusals name the parcel's state - "waybill not in a
		// pending state" tells an operator the attempt already happened -
		// so they are passed through rather than flattened.
		obs.FromContext(ctx).Warn("delhivery refused an NDR instruction",
			"brand", brand, "waybill", waybill, "act", req.Act, "error", err)
		detail(c, http.StatusBadGateway, "Delhivery would not take that: "+err.Error())
		return
	}

	// RECORDED BECAUSE NOTHING ELSE IS. There is no row in Tensor for a
	// shipment - the list is assembled live from Shopify and Delhivery on
	// every read - so this log line is the only trace that somebody asked
	// for this, and the only way to tie an upload id back to a person.
	obs.FromContext(ctx).Info("NDR instruction submitted to delhivery",
		"brand", brand, "waybill", waybill, "act", string(instruction.Act),
		"upload_id", res.UploadID, "by", currentUserID(c))

	c.JSON(http.StatusOK, gin.H{
		"waybill":   waybill,
		"act":       req.Act,
		"upload_id": res.UploadID,
		// SUBMITTED, deliberately not "done". Delhivery has taken the
		// instruction and will act on it out of band; the screen says so.
		"submitted": true,
	})
}

// ndrInstruction turns the screen's words into Delhivery's, or says why it
// cannot - in the operator's words, naming the field to fix.
func ndrInstruction(waybill string, req ndrActionRequest) (delhivery.NDRInstruction, string) {
	in := delhivery.NDRInstruction{Waybill: waybill}

	switch strings.ToLower(strings.TrimSpace(req.Act)) {
	case "reattempt":
		in.Act = delhivery.Reattempt
		return in, ""

	case "defer":
		in.Act = delhivery.Defer
		date := strings.TrimSpace(req.Date)
		if date == "" {
			return in, "Pick the date to try again on."
		}
		// Parsed here rather than passed through, so a date Delhivery would
		// refuse is refused now, and a date in the past - which it accepts
		// and never acts on - is caught at all.
		when, err := time.Parse("2006-01-02", date)
		if err != nil {
			return in, "That date could not be read. Use the date picker."
		}
		if when.Before(time.Now().In(when.Location()).Truncate(24 * time.Hour)) {
			return in, "That date has passed. Pick today or later."
		}
		in.Deferred = when
		return in, ""

	case "edit":
		in.Act = delhivery.EditAddress
		in.Name, in.Address, in.Phone = req.Name, req.Address, req.Phone
		if strings.TrimSpace(req.Name) == "" &&
			strings.TrimSpace(req.Address) == "" &&
			strings.TrimSpace(req.Phone) == "" {
			return in, "Change at least one of the name, address or phone number."
		}
		return in, ""
	}

	// Return is the one an operator will look for, so it is named rather
	// than lumped in with a typo.
	if strings.EqualFold(strings.TrimSpace(req.Act), "return") {
		return in, "Delhivery does not accept returns through this API. " +
			"Start a return from the Delhivery dashboard."
	}
	return in, "That is not something the courier can be asked to do."
}
