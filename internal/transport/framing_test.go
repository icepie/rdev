package transport

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, KindJSON, []byte(`{"type":"register"}`)); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Kind != KindJSON || string(frame.Payload) != `{"type":"register"}` {
		t.Fatalf("unexpected frame: %#v", frame)
	}
}

func TestFrameRejectsTooLarge(t *testing.T) {
	payload := make([]byte, MaxFramePayload+1)
	if err := WriteFrame(&bytes.Buffer{}, KindBinary, payload); err == nil {
		t.Fatal("expected too-large error")
	}
}
