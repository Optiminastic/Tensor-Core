package db

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// NumFloat returns a numeric as a float64 (0 when NULL).
func NumFloat(n pgtype.Numeric) float64 {
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0
	}
	return f.Float64
}

// NumFloatPtr returns a nullable numeric as *float64 (nil when NULL).
func NumFloatPtr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	v := f.Float64
	return &v
}

// Time returns a timestamptz as time.Time (zero when NULL).
func Time(t pgtype.Timestamptz) time.Time { return t.Time }

// TimePtr returns a nullable timestamptz as *time.Time (nil when NULL).
func TimePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// StrPtrFromIface converts a nullable text/enum column read as interface{} to
// *string (nil when NULL). pgx yields a string for a non-null text value.
func StrPtrFromIface(v any) *string {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}
