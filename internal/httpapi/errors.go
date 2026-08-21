// Package httpapi is the Gin HTTP layer. It mirrors the FastAPI routers
// one-for-one: same paths, methods, guards, request/response JSON and status
// codes. Every error body is {"detail": "<string>"} so the frontend's
// body.detail always reads a string.
package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Optiminastic/tensor-core/internal/obs"
)

// detail writes {"detail": msg} with the given status.
func detail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"detail": msg})
}

// isNoRows reports whether err is pgx's "no rows" sentinel. Handlers use it to
// tell "the row does not exist yet" apart from a real database error, so the
// latter is never silently rendered as an empty field.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// isUniqueViolation reports whether err is Postgres' unique_violation (SQLSTATE
// 23505), so a handler can map a duplicate-key insert/update to a 409 instead of
// a generic 500. Used for the designs_sku_key partial unique index.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// dbError maps a database error to the response, centralising the 404-vs-500
// decision that used to be hand-rolled at every read site: pgx.ErrNoRows becomes
// a 404 with notFoundMsg, and any other error is logged server-side (with the
// request id) and returned as a 500 with genericMsg. The underlying cause is
// never sent to the client but is always logged, closing the "500 cause
// discarded" gap without leaking internals.
func dbError(c *gin.Context, err error, notFoundMsg, genericMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		detail(c, http.StatusNotFound, notFoundMsg)
		return
	}
	obs.FromContext(c.Request.Context()).Error("database error",
		"error", err,
		"route", c.FullPath(),
	)
	detail(c, http.StatusInternalServerError, genericMsg)
}

// httpErr carries the status and client-facing message for a failure that
// happened below the handler. It lets one service method serve both an HTTP
// handler and a background worker: the handler renders status+msg unchanged,
// the worker matches cause with errors.Is and ignores the rest.
//
// msg is never derived from cause, so an internal string can never reach a
// client.
type httpErr struct {
	status int
	msg    string
	cause  error
}

func (e *httpErr) Error() string { return e.msg }

// Unwrap exposes the sentinel (if any) to errors.Is/errors.As.
func (e *httpErr) Unwrap() error { return e.cause }

// respondErr renders err when it is an *httpErr, preserving the exact status
// and detail string the route contract promises. Anything else is logged with
// the request id and rendered as a 500 with fallbackMsg, so an unmapped error
// is never leaked verbatim.
func respondErr(c *gin.Context, err error, fallbackMsg string) {
	var he *httpErr
	if errors.As(err, &he) {
		detail(c, he.status, he.msg)
		return
	}
	obs.FromContext(c.Request.Context()).Error("service error",
		"error", err,
		"route", c.FullPath(),
	)
	detail(c, http.StatusInternalServerError, fallbackMsg)
}

// maxJSONBodyBytes caps JSON request bodies so a malicious or buggy client
// cannot exhaust memory with an unbounded payload. It is intentionally generous
// for config/pricing JSON; the multipart design upload has its own separate,
// much larger limit and does not pass through here.
const maxJSONBodyBytes = 1 << 20 // 1 MiB

// bindJSON binds and validates the request body, writing a 422 with a string
// detail on failure. Returns false when the caller should stop.
func bindJSON(c *gin.Context, obj any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes)
	if err := c.ShouldBindJSON(obj); err != nil {
		detail(c, http.StatusUnprocessableEntity, validationMessage(err))
		return false
	}
	return true
}

func validationMessage(err error) string {
	var ve validator.ValidationErrors
	if errors.As(err, &ve) && len(ve) > 0 {
		f := ve[0]
		return fmt.Sprintf("Field '%s' failed validation (%s).", f.Field(), f.Tag())
	}
	return "The request body is invalid."
}

// readBody reads and restores the request body so it can be parsed more than
// once (used by PATCH handlers that need to detect which fields were sent).
func readBody(c *gin.Context) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes))
	if err != nil {
		detail(c, http.StatusBadRequest, "Could not read the request body.")
		return nil, false
	}
	return body, true
}

// parseUUIDParam parses a path UUID, writing a 422 on failure.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The identifier in the URL is not valid.")
		return uuid.Nil, false
	}
	return id, true
}
