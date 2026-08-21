package bambubuddy

import "testing"

// A URL pasted with the trailing slash a browser shows must not turn into a
// double slash on every request. Whether the far end tolerates "//api/v1/..."
// is not something a config value should decide.
func TestNewTrimsTrailingSlashAndSpace(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"trailing slash", "http://opti.tail72d1ae.ts.net:8000/", "http://opti.tail72d1ae.ts.net:8000"},
		{"several slashes", "http://host:8000///", "http://host:8000"},
		{"surrounding space", "  http://host:8000  ", "http://host:8000"},
		{"already clean", "http://host:8000", "http://host:8000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.in, "key").baseURL; got != tc.want {
				t.Fatalf("baseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty URL or key must leave the client unconfigured, so callers skip the
// integration instead of failing every cycle. A value that is only whitespace
// counts as empty - it is a typo, not a configuration.
func TestConfiguredRequiresBoth(t *testing.T) {
	for _, tc := range []struct {
		name, url, key string
		want           bool
	}{
		{"both set", "http://host:8000", "bb_key", true},
		{"no url", "", "bb_key", false},
		{"no key", "http://host:8000", "", false},
		{"whitespace key", "http://host:8000", "   ", false},
		{"neither", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.url, tc.key).Configured(); got != tc.want {
				t.Fatalf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}
