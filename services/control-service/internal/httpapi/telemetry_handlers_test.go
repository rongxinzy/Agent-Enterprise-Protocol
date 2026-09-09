package httpapi

import (
	"testing"
	"time"
)

func TestTelemetryCursorRoundTrip(t *testing.T) {
	want := telemetryCursor{OccurredAt: time.Date(2026, 9, 9, 8, 7, 6, 123456789, time.UTC), EventID: "event-42"}
	got, err := decodeTelemetryCursor(encodeTelemetryCursor(want))
	if err != nil {
		t.Fatalf("decodeTelemetryCursor returned error: %v", err)
	}
	if !got.OccurredAt.Equal(want.OccurredAt) || got.EventID != want.EventID {
		t.Fatalf("decoded cursor = %#v, want %#v", got, want)
	}
}

func TestTelemetryCursorRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"not-base64", "", "ZXhhbXBsZQ"} {
		if _, err := decodeTelemetryCursor(value); err == nil {
			t.Errorf("decodeTelemetryCursor(%q) succeeded", value)
		}
	}
}
