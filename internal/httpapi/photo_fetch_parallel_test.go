package httpapi

import (
	"testing"
	"time"
)

func TestContentRangeTotal(t *testing.T) {
	for h, want := range map[string]int{"bytes 0-524287/3555160": 3555160, "bytes 0-9/*": 7, "": 7} {
		if got := contentRangeTotal(h, 7); got != want {
			t.Errorf("%q = %d, want %d", h, got, want)
		}
	}
}

// The cache drops the oldest photo when a new one would not fit, and never
// serves one past its hour.
func TestPhotoCacheEvictsOldestAndExpires(t *testing.T) {
	c := &bytesCache{entries: map[string]cacheEntry{}}
	big := make([]byte, photoCacheBytes/2)
	c.put("a", big)
	c.put("b", big)
	c.put("c", big)
	if _, ok := c.get("a"); ok {
		t.Fatal("the oldest photo should have been dropped")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("the newest photo should be kept")
	}
	c.entries["c"] = cacheEntry{data: big, at: time.Now().Add(-2 * photoCacheTTL)}
	if _, ok := c.get("c"); ok {
		t.Fatal("an expired photo must not be served")
	}
}
