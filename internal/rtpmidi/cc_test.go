package rtpmidi

import (
	"bytes"
	"io"
	"log"
	"testing"

	"seqone/internal/protocol"
)

// TestBridge_CCForwardedOnTrackChannel verifies that an EvCC published
// by the engine reaches the wire as a standard MIDI Control Change on
// the channel the engine resolved. The bridge does no remapping — it
// just emits 0xB0|ch, cc, value.
func TestBridge_CCForwardedOnTrackChannel(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvCC, Channel: 3, CC: 1, CCValue: 90}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 1 {
		t.Fatalf("want 1 message, got %d", len(m))
	}
	want := []byte{0xB2, 1, 90} // status 0xB0|2 (channel 3 user → 2 wire), cc=1, val=90
	if !bytes.Equal(m[0], want) {
		t.Errorf("got %x, want %x", m[0], want)
	}
}

// TestBridge_CCOutOfRangeChannelFallsBack confirms the bridge follows the
// same fallback rule as note events when the channel is invalid (e.g.
// the engine couldn't resolve a track) — it lands on channel 1 (wire 0)
// rather than producing a malformed status byte.
func TestBridge_CCOutOfRangeChannelFallsBack(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvCC, Channel: 0, CC: 64, CCValue: 127}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 1 {
		t.Fatalf("want 1 message, got %d", len(m))
	}
	if m[0][0] != 0xB0 {
		t.Errorf("status byte = %#x, want 0xB0 (fallback ch 0)", m[0][0])
	}
}

// TestBridge_CCValueClamped asserts ill-formed engine events with values
// outside [0,127] don't escape onto the wire as garbage; the bridge
// clamps them to the valid MIDI range.
func TestBridge_CCValueClamped(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 2)
	ch <- protocol.Event{Event: protocol.EvCC, Channel: 1, CC: 7, CCValue: 250}
	ch <- protocol.Event{Event: protocol.EvCC, Channel: 1, CC: 7, CCValue: -3}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 2 {
		t.Fatalf("want 2 messages, got %d", len(m))
	}
	if m[0][2] != 127 {
		t.Errorf("over-range value = %d, want clamped to 127", m[0][2])
	}
	if m[1][2] != 0 {
		t.Errorf("under-range value = %d, want clamped to 0", m[1][2])
	}
}
