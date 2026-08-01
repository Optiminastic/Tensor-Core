package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// The design review workflow: a designer submits a priced Green/Yellow design to
// the Project Lead, who comments and either approves it or sends it back. Each
// action records a row on the design_reviews thread and advances the design
// status; the RBAC is the already-seeded design:submit / design:approve /
// design:reject permissions guarding these routes.

type reviewResponse struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id"`
	Kind      string    `json:"kind"`
	Body      *string   `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func reviewDTO(r gen.DesignReview) reviewResponse {
	return reviewResponse{
		ID: r.ID.String(), AuthorID: r.AuthorID, Kind: r.Kind, Body: r.Body, CreatedAt: db.Time(r.CreatedAt),
	}
}

// respondDesign re-reads a design and returns its base shape, so an action
// response carries the new status.
func (s *Server) respondDesign(c *gin.Context, id uuid.UUID) {
	d, err := s.store.Q.GetDesignByID(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design.")
		return
	}
	c.JSON(http.StatusOK, designDTO(d.ID, d.BrandSlug, d.Name, d.CreatedBy, d.Status, d.Material,
		d.Colour, d.Finish, d.UnitsPerBed, d.Quality, d.InfillPct, d.PreviewKey, d.CreatedAt, d.UpdatedAt))
}

type submitRequest struct {
	Message *string `json:"message"`
}

// submitDesign sends a priced Green/Yellow design to the Project Lead for review.
func (s *Server) submitDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req submitRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	design, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	if design.Status != designPriced && design.Status != designChangesRequested {
		detail(c, http.StatusConflict, "Only a priced design can be submitted for review.")
		return
	}
	pricing, err := s.store.Q.GetDesignPricing(ctx, id)
	if err != nil {
		dbError(c, err, "The design has not been priced yet.", "Could not load the design's pricing.")
		return
	}
	if pricing.Verdict != verdictGreen && pricing.Verdict != verdictYellow {
		detail(c, http.StatusConflict, "A Red design must be revised before it can be submitted.")
		return
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designSubmitted, ID: id}); err != nil {
			return err
		}
		return s.recordReview(ctx, q, id, reviewSubmit, currentUserID(c), req.Message)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not submit the design.")
		return
	}
	s.respondDesign(c, id)
}

type approveRequest struct {
	ApprovedSP *int `json:"approved_sp"`
}

// approveDesign locks the selling price and marks the design approved. Only a
// submitted design can be approved.
func (s *Server) approveDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req approveRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	design, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	if design.Status != designSubmitted {
		detail(c, http.StatusConflict, "Only a submitted design can be approved.")
		return
	}
	pricing, err := s.store.Q.GetDesignPricing(ctx, id)
	if err != nil {
		dbError(c, err, "The design has not been priced yet.", "Could not load the design's pricing.")
		return
	}
	price := pricing.RecommendedSp
	if req.ApprovedSP != nil {
		p := int32(*req.ApprovedSP)
		price = &p
	}
	if price == nil {
		detail(c, http.StatusUnprocessableEntity, "A selling price is required to approve.")
		return
	}

	userID := currentUserID(c)
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ApproveDesignPricing(ctx, gen.ApproveDesignPricingParams{
			ApprovedSp: price, ApprovedBy: &userID, DesignID: id,
		}); err != nil {
			return err
		}
		if err := q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designApproved, ID: id}); err != nil {
			return err
		}
		return s.recordReview(ctx, q, id, reviewApprove, userID, nil)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not approve the design.")
		return
	}
	s.respondDesign(c, id)
}

type rejectRequest struct {
	Comment string `json:"comment" binding:"required,min=1,max=2000"`
}

// rejectDesign sends a submitted design back to the designer with a required
// comment explaining what to change.
func (s *Server) rejectDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req rejectRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	design, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	if design.Status != designSubmitted {
		detail(c, http.StatusConflict, "Only a submitted design can be sent back.")
		return
	}

	comment := req.Comment
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designChangesRequested, ID: id}); err != nil {
			return err
		}
		return s.recordReview(ctx, q, id, reviewReject, currentUserID(c), &comment)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not send the design back.")
		return
	}
	s.respondDesign(c, id)
}

type commentRequest struct {
	Body string `json:"body" binding:"required,min=1,max=2000"`
}

// commentOnDesign adds a freeform comment to the review thread. Anyone who can
// view the design may comment.
func (s *Server) commentOnDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req commentRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetDesignByID(ctx, id); err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	body := req.Body
	row, err := s.store.Q.InsertDesignReview(ctx, gen.InsertDesignReviewParams{
		ID: uuid.New(), DesignID: id, AuthorID: currentUserID(c), Kind: reviewComment, Body: &body,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not add the comment.")
		return
	}
	c.JSON(http.StatusCreated, reviewDTO(row))
}

// listDesignReviews returns the design's review thread, oldest first.
func (s *Server) listDesignReviews(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetDesignByID(ctx, id); err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	rows, err := s.store.Q.ListDesignReviews(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the review thread.")
		return
	}
	out := make([]reviewResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, reviewDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// recordReview inserts a review event on the given tx-bound querier.
func (s *Server) recordReview(ctx context.Context, q *gen.Queries, designID uuid.UUID, kind, authorID string, body *string) error {
	_, err := q.InsertDesignReview(ctx, gen.InsertDesignReviewParams{
		ID: uuid.New(), DesignID: designID, AuthorID: authorID, Kind: kind, Body: body,
	})
	return err
}
