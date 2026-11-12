package httpapi

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func orderWithLines(lines string) gen.Order {
	return gen.Order{OrderNumber: "T3DPS-116477", LineItems: []byte(lines)}
}

func TestPhotoFrameURLsReadsTheTwoPhotos(t *testing.T) {
	l, r, problem := photoFrameURLs(orderWithLines(`[{"properties":[
		{"name":"LEFT NAME","value":"HARDIK"},
		{"name":"LEFT PHOTO","value":"https://x.example/a.jpg"},
		{"name":"RIGHT NAME","value":"BHAVYA"},
		{"name":"RIGHT PHOTO:","value":" https://x.example/b.jpg "}]}]`))
	if problem != "" || l != "https://x.example/a.jpg" || r != "https://x.example/b.jpg" {
		t.Fatalf("got %q %q %q", l, r, problem)
	}
}

func TestPhotoFrameURLsSaysWhatIsMissing(t *testing.T) {
	if _, _, p := photoFrameURLs(orderWithLines(`[{"properties":[{"name":"LEFT PHOTO","value":"https://x/a.jpg"}]}]`)); !strings.Contains(p, "only one photo") {
		t.Fatalf("one photo: %q", p)
	}
	if _, _, p := photoFrameURLs(orderWithLines(`[{"properties":[{"name":"name-1","value":"A"}]}]`)); !strings.Contains(p, "no LEFT PHOTO") {
		t.Fatalf("no photos: %q", p)
	}
}

// A customer-typed URL must not reach this host's own network.
func TestRefusePrivateAddress(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "10.0.0.5:443", "192.168.1.2:443", "169.254.169.254:80", "[::1]:443"} {
		if refusePrivateAddress("tcp", addr, nil) == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
	if err := refusePrivateAddress("tcp", "104.16.1.1:443", nil); err != nil {
		t.Errorf("a public address was refused: %v", err)
	}
}

// T3DPS-116420: a photo plank first, the night lamp second.
func TestNightLampFieldsPicksTheLampLine(t *testing.T) {
	p, text, problem := nightLampFields(orderWithLines(`[
		{"sku":"PHOTO-1","product_name":"Photo Plank","properties":[{"name":"Photo","value":"https://x/plank.jpg"},{"name":"Name","value":"A"}]},
		{"sku":"3DMS01-6","product_name":"Night Lamp","properties":[{"name":"_has_gpo","value":"1"},{"name":"file-1","value":"https://x/moon.jpg"},{"name":"text-moon","value":"6th Dec 2026"}]}]`))
	if problem != "" || p != "https://x/moon.jpg" || text != "6th Dec 2026" {
		t.Fatalf("got %q %q %q", p, text, problem)
	}
	if _, _, problem := nightLampFields(orderWithLines(`[{"properties":[{"name":"file-1","value":"https://x/m.jpg"},{"name":"text-moon","value":"` + strings.Repeat("x", 41) + `"}]}]`)); !strings.Contains(problem, "at most 40") {
		t.Fatalf("long text: %q", problem)
	}
}
