package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func pageCtx(query string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
	return c, w
}

func TestCursorRoundTrip(t *testing.T) {
	id := uuid.New()
	ts := time.Unix(0, 1234567890123456789).UTC()
	gotTS, gotID, err := decodeCursor(encodeCursor(ts, id))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp round-trip: got %v, want %v", gotTS, ts)
	}
	if gotID != id {
		t.Errorf("id round-trip: got %v, want %v", gotID, id)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	bad := []string{
		"",
		"!!!",
		base64.RawURLEncoding.EncodeToString([]byte("no-separator")),
		base64.RawURLEncoding.EncodeToString([]byte("abc:not-a-uuid")),
		base64.RawURLEncoding.EncodeToString([]byte("123:" + uuid.Nil.String()[:10])),
	}
	for _, s := range bad {
		if _, _, err := decodeCursor(s); err == nil {
			t.Errorf("expected an error decoding %q", s)
		}
	}
}

func TestParsePageParamsNoLimitMeansNoPagination(t *testing.T) {
	c, _ := pageCtx("brand=gifting")
	p, ok := parsePageParams(c)
	if !ok || p.paginate {
		t.Fatalf("absent limit should disable pagination: ok=%v paginate=%v", ok, p.paginate)
	}
}

func TestParsePageParamsValidLimit(t *testing.T) {
	c, _ := pageCtx("limit=50")
	p, ok := parsePageParams(c)
	if !ok || !p.paginate || p.limit != 50 || p.cursorID != nil {
		t.Fatalf("unexpected: ok=%v paginate=%v limit=%d cursor=%v", ok, p.paginate, p.limit, p.cursorID)
	}
}

func TestParsePageParamsRejectsBadLimit(t *testing.T) {
	for _, q := range []string{"limit=0", "limit=-1", "limit=abc", "limit=201"} {
		c, w := pageCtx(q)
		if _, ok := parsePageParams(c); ok {
			t.Errorf("expected %q to be rejected", q)
		}
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: want 422, got %d", q, w.Code)
		}
	}
}

func TestParsePageParamsWithCursor(t *testing.T) {
	id := uuid.New()
	cursor := encodeCursor(time.Unix(0, 5).UTC(), id)
	c, _ := pageCtx("limit=10&cursor=" + cursor)
	p, ok := parsePageParams(c)
	if !ok || p.cursorID == nil || *p.cursorID != id || !p.cursorTS.Valid {
		t.Fatalf("cursor not parsed: ok=%v params=%+v", ok, p)
	}
}

func TestParsePageParamsRejectsBadCursor(t *testing.T) {
	c, w := pageCtx("limit=10&cursor=%21%21%21") // "!!!" url-encoded
	if _, ok := parsePageParams(c); ok {
		t.Fatal("expected a bad cursor to be rejected")
	}
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}
