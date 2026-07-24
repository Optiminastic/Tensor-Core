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
)

// detail writes {"detail": msg} with the given status.
func detail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"detail": msg})
}

// bindJSON binds and validates the request body, writing a 422 with a string
// detail on failure. Returns false when the caller should stop.
func bindJSON(c *gin.Context, obj any) bool {
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
	body, err := io.ReadAll(c.Request.Body)
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
