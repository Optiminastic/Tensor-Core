package httpapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Keyset pagination for the unbounded list endpoints. It is opt-in and backward
// compatible: without a `limit` query parameter a handler returns the full list
// exactly as before. With `limit` (and an optional opaque `cursor`), it returns
// one page and, when more rows may exist, the next cursor in the X-Next-Cursor
// response header - so the JSON body shape never changes.
const (
	nextCursorHeader = "X-Next-Cursor"
	maxPageLimit     = 200
)

// pageParams is the parsed pagination request. When paginate is false the caller
// serves the full (unpaginated) list.
type pageParams struct {
	paginate bool
	limit    int32
	cursorTS pgtype.Timestamptz
	cursorID *uuid.UUID
}

// parsePageParams reads the optional `limit` and `cursor` query parameters. It
// writes a 422 and returns ok=false on an invalid value. A missing `limit` means
// "no pagination" (paginate=false), preserving the previous full-list behaviour.
func parsePageParams(c *gin.Context) (pageParams, bool) {
	limitStr := strings.TrimSpace(c.Query("limit"))
	if limitStr == "" {
		return pageParams{paginate: false}, true
	}
	n, err := strconv.Atoi(limitStr)
	if err != nil || n < 1 || n > maxPageLimit {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("'limit' must be an integer between 1 and %d.", maxPageLimit))
		return pageParams{}, false
	}
	p := pageParams{paginate: true, limit: int32(n)}

	if cursorStr := strings.TrimSpace(c.Query("cursor")); cursorStr != "" {
		ts, id, err := decodeCursor(cursorStr)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The 'cursor' parameter is not valid.")
			return pageParams{}, false
		}
		p.cursorTS = pgtype.Timestamptz{Time: ts, Valid: true}
		p.cursorID = &id
	}
	return p, true
}

// setNextCursor writes the next-page cursor header when a full page was returned
// (rowCount == limit), signalling that more rows may exist. A short page omits
// the header, which the client reads as "end of list".
func setNextCursor(c *gin.Context, rowCount int, limit int32, last time.Time, lastID uuid.UUID) {
	if rowCount == int(limit) {
		c.Header(nextCursorHeader, encodeCursor(last, lastID))
	}
}

// encodeCursor packs a (created_at, id) position into an opaque, URL-safe token.
func encodeCursor(t time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%d:%s", t.UTC().UnixNano(), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor, rejecting anything malformed.
func decodeCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	nanoStr, idStr, found := strings.Cut(string(raw), ":")
	if !found {
		return time.Time{}, uuid.Nil, fmt.Errorf("cursor: missing separator")
	}
	nanos, err := strconv.ParseInt(nanoStr, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return time.Unix(0, nanos).UTC(), id, nil
}
