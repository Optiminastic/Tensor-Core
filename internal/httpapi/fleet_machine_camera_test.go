package httpapi

import "testing"

// The frame rate is a bandwidth control on a link that runs through someone's
// home internet, so a hand-edited query string must not be able to raise it,
// and a malformed one must not cost the operator the picture.
func TestCameraFPS(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     int
	}{
		{"absent", "", defaultCameraFPS},
		{"not a number", "fast", defaultCameraFPS},
		{"zero", "0", defaultCameraFPS},
		{"negative", "-5", defaultCameraFPS},
		{"in range", "10", 10},
		{"at ceiling", "15", maxCameraFPS},
		{"above ceiling", "120", maxCameraFPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cameraFPS(tc.in); got != tc.want {
				t.Fatalf("cameraFPS(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
