package httpapi

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bindRawJSON unmarshals a pre-read body into dst (used where a handler already
// consumed the body via readBody).
func bindRawJSON(body []byte, dst any) error { return json.Unmarshal(body, dst) }

// jobQuantity clamps a job quantity to at least 1.
func jobQuantity(q int32) int32 {
	if q < 1 {
		return 1
	}
	return q
}

// int32ptr returns a pointer to an int32-narrowed int.
func int32ptr(v int) *int32 { n := int32(v); return &n }

// int32PtrFromInt narrows an optional int to an optional int32.
func int32PtrFromInt(p *int) *int32 {
	if p == nil {
		return nil
	}
	return int32ptr(*p)
}

// int32PtrToIntPtr widens an optional int32 to an optional int.
func int32PtrToIntPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// Small conversion helpers shared by the production-pipeline handlers.

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// uuidPtrStr renders a nullable UUID as a nullable string.
func uuidPtrStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// nilIfEmpty maps a blank query value to nil so it drops out of an optional
// filter; a non-blank value becomes a pointer to itself.
func nilIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// nonEmptyPtr returns a pointer to a trimmed string, or nil when it is empty.
func nonEmptyPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// intPtrToInt32 narrows an optional int to an optional int32.
func intPtrToInt32(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

// filamentForQty multiplies a per-unit filament weight by the job quantity, or
// returns nil when the line has no weight.
func filamentForQty(perUnit *float64, qty int32) *float64 {
	if perUnit == nil {
		return nil
	}
	v := *perUnit * float64(qty)
	return &v
}

// descriptionOf is the job description for a line item: its product name, or a
// placeholder when the line carries none.
func descriptionOf(li production.LineItem) string {
	if strings.TrimSpace(li.ProductName) != "" {
		return li.ProductName
	}
	return "Unknown product"
}

// roleStrings converts the caller's typed roles to plain strings for the pure
// production package's field-gate.
func roleStrings(roles []auth.RoleName) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	return out
}

// nowPtr returns the current UTC time as a pointer, for stamping validated_at.
func nowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
