package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestParseOrderNumbersSplitsOnCommasAndSpaces(t *testing.T) {
	got := parseOrderNumbers(" 116324,115443  117584;116324 , T3DPS-1 ")
	want := []string{"116324", "115443", "117584", "T3DPS-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func sheetLine(sku string, qty int, props ...string) production.LineItem {
	li := production.LineItem{SKU: &sku, Quantity: qty}
	for i := 0; i+1 < len(props); i += 2 {
		li.Properties = append(li.Properties, production.LineProp{Name: props[i], Value: props[i+1]})
	}
	return li
}

// The sizes are the ones measured off the shop's sample sheet.
func TestEachProductPrintsAtItsOwnSize(t *testing.T) {
	cases := []struct {
		li   production.LineItem
		want photoSpec
	}{
		{sheetLine("T3DPS-DUO-3", 1), photoSpec{w: 65, h: 105}},
		{sheetLine("PFM-2", 1), photoSpec{w: 56.6, h: 59.7}},
		{sheetLine("FCF-BLU", 1, "photo_frames", "3"), photoSpec{w: 106, h: 80}},
		{sheetLine("FCF-BLU", 1, "photo_frames", "4"), photoSpec{w: 89, h: 67}},
		{sheetLine("FCF-BLU", 1, "photo_frames", "6"), photoSpec{w: 71, h: 54}},
		{sheetLine("TSM-BLK", 1, "batch_size", "Large"), photoSpec{w: 51, h: 51, circle: true}},
		{sheetLine("TSM-BLK", 1, "batch_size", "Small"), photoSpec{w: 41, h: 41, circle: true}},
	}
	for _, c := range cases {
		got, problem, ok := photoSpecFor(c.li)
		if !ok || problem != "" || got != c.want {
			t.Errorf("%s: %+v %q %v, want %+v", *c.li.SKU, got, problem, ok, c.want)
		}
	}
	if _, _, ok := photoSpecFor(sheetLine("DNPF-GLD", 1)); ok {
		t.Error("a DNP frame is not a sheet photo product")
	}
	if _, problem, _ := photoSpecFor(sheetLine("FCF-BLU", 1, "photo_frames", "5")); problem == "" {
		t.Error("an unknown frame count must be named, not guessed")
	}
}

func TestOrderPhotosTakesEveryPhotoOncePerCopy(t *testing.T) {
	lines := []production.LineItem{
		sheetLine("PFM-1", 2, "PHOTO - 1", "https://x/a.jpg", "TEXT - 1", "HI"),
		sheetLine("T3DPS-DUO-1", 1, "_has_gpo", "https://x/hidden", "PHOTO", "https://x/b.jpg"),
		sheetLine("FCF-RED", 1, "photo_frames", "6"),
		sheetLine("DNPF-GLD", 1, "LEFT PHOTO", "https://x/c.jpg"),
	}
	raw, _ := json.Marshal(lines)
	photos, skipped := orderPhotos(gen.Order{OrderNumber: "T3DPS-116894", LineItems: raw})
	if len(photos) != 3 || photos[0].url != "https://x/a.jpg" || photos[2].url != "https://x/b.jpg" {
		t.Fatalf("photos = %+v", photos)
	}
	if photos[0].label != "116894 PFM-1" {
		t.Fatalf("label = %q", photos[0].label)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "carries no photo") {
		t.Fatalf("skipped = %v", skipped)
	}
}
