package production

import (
	"strings"
	"testing"
)

// A part uid is the stable, never-reused id for a product part slot. It must carry
// the PART- prefix and never collide, since the whole model keys a physical part on
// it across every reprint.
func TestNewPartUIDFormatAndUniqueness(t *testing.T) {
	const n = 5000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		uid, err := NewPartUID()
		if err != nil {
			t.Fatalf("NewPartUID: %v", err)
		}
		if !strings.HasPrefix(uid, "PART-") {
			t.Fatalf("uid %q missing PART- prefix", uid)
		}
		if _, dup := seen[uid]; dup {
			t.Fatalf("duplicate part uid generated: %q", uid)
		}
		seen[uid] = struct{}{}
	}
}
