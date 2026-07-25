package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// patchField applies one optional field of a PATCH request. When key is present
// in raw it decodes the value into a T, runs validate (which returns "" when the
// value is acceptable, or the exact 422 message otherwise), and then calls apply
// to set it on the update params. An absent key leaves the field untouched. It
// writes the error response and returns false when the caller should stop.
//
// This replaces the copy-pasted "if v, ok := raw[key]; ok { decode; validate;
// assign }" block that each PATCH handler used to repeat per field; the exact
// validation and message stay in the caller's closures, so the wire contract is
// unchanged.
func patchField[T any](
	c *gin.Context,
	raw map[string]json.RawMessage,
	key string,
	validate func(T) string,
	apply func(T),
) bool {
	v, ok := raw[key]
	if !ok {
		return true
	}
	var val T
	if !decodeField(c, v, &val) {
		return false
	}
	if validate != nil {
		if msg := validate(val); msg != "" {
			detail(c, http.StatusUnprocessableEntity, msg)
			return false
		}
	}
	apply(val)
	return true
}

// noValidation is a convenience for patchField calls whose only step is decoding
// and assignment (no business rule beyond the JSON type).
func noValidation[T any](T) string { return "" }
