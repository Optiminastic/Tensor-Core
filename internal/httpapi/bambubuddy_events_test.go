package httpapi

// The push endpoint: who may call it, and what it does with what arrives.

import (
	"strings"
	"testing"
)

// The real payload, captured from a live print_start. Every event carries a
// base64 JPEG of the plate, which is the bulk of the body.
const liveEventBody = `{"title":"Print Started",` +
	`"message":"H1: BLUE-114807-114808.stl\nEstimated: Unknown",` +
	`"timestamp":"2026-09-04T12:25:29.860639","source":"Bambuddy",` +
	`"event":"print_start","printer":"H1",` +
	`"filename":"BLUE-114807-114808-114812-114819-114820.stl",` +
	`"estimated_time":"Unknown","eta":"Unknown","app_name":"Bambuddy",` +
	`"image":"/9j//gAPTGF2YzYzLjguMTAxAP/bAEMACAQEBAQEBQUFBQUFBgYGBgYG"}`

// The thumbnail never reaches the log. A few events per print would otherwise
// bury every field that matters under megabytes nobody can grep.
func TestPayloadForLogDropsTheThumbnail(t *testing.T) {
	got := payloadForLog([]byte(liveEventBody))
	if strings.Contains(got, "/9j//gAP") {
		t.Error("the base64 thumbnail was logged")
	}
	if strings.Contains(got, `"image"`) {
		t.Error("the image field was logged")
	}
	for _, want := range []string{"print_start", "H1", "BLUE-114807"} {
		if !strings.Contains(got, want) {
			t.Errorf("log payload lost %q: %s", want, got)
		}
	}
}

// A body that cannot be read as an object is logged as it arrived - that is
// exactly the one worth seeing.
func TestPayloadForLogKeepsAnUnreadableBody(t *testing.T) {
	if got := payloadForLog([]byte("not json")); got != "not json" {
		t.Errorf("payloadForLog = %q, want the raw body", got)
	}
}

// The parsed fields must survive the real payload, since the first test event
// arrived parsing cleanly with every field empty - a failure that looks like
// success in a log printing only what it understood.
func TestEventPayloadParsesALiveEvent(t *testing.T) {
	var p bambuBuddyEventPayload
	if err := unmarshalEvent([]byte(liveEventBody), &p); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.eventName() != "print_start" {
		t.Errorf("event = %q, want print_start", p.eventName())
	}
	if p.printerName() != "H1" {
		t.Errorf("printer = %q, want H1", p.printerName())
	}
	if !strings.HasPrefix(p.Filename, "BLUE-114807") {
		t.Errorf("filename = %q", p.Filename)
	}
}

// Only state changes trigger a fleet read. Progress ticks arrive throughout a
// print, and refreshing on those would turn the push back into a poll - at a
// rate the fleet refresh cannot keep up with.
func TestChangesFleetState(t *testing.T) {
	for event, want := range map[string]bool{
		"print_start":          true,
		"print_complete":       true,
		"print_failed":         true,
		"printer_offline":      true,
		"queue_job_started":    true,
		"print_progress":       false,
		"first_layer_complete": false,
		"queue_job_added":      false,
		"":                     false,
	} {
		if got := changesFleetState(event); got != want {
			t.Errorf("changesFleetState(%q) = %v, want %v", event, got, want)
		}
	}
}
