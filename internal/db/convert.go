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

// Timestamptz builds a nullable timestamptz from a *time.Time (invalid/NULL when
// nil). It is the inverse of TimePtr for binding query parameters.
func Timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
