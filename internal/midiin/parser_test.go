package midiin

import "testing"

// TestParser_ControlChange covers the bread-and-butter case: a CC
// arrives as three bytes (status + cc# + value) and parses as one
// Message with Channel translated from 0-based wire form to 1-based.
func TestParser_ControlChange(t *testing.T) {
	var p Parser
	got := p.Feed([]byte{0xB0, 0x0D, 0x40}, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d (%+v)", len(got), got)
	}
	want := Message{Kind: KindControlChange, Channel: 1, Data1: 13, Data2: 64}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

// TestParser_RunningStatus pins the spec-required behaviour where a
// repeated message-kind can omit the status byte. A bursty knob will
// usually send running status to save bytes.
func TestParser_RunningStatus(t *testing.T) {
	var p Parser
	stream := []byte{
		0xB1, 0x0D, 0x10, // first CC, channel 2
		0x0D, 0x20, // running-status second CC
		0x0D, 0x30, // running-status third CC
	}
	got := p.Feed(stream, nil)
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d (%+v)", len(got), got)
	}
	for i, exp := range []uint8{0x10, 0x20, 0x30} {
		if got[i].Channel != 2 || got[i].Data1 != 13 || got[i].Data2 != exp {
			t.Errorf("msg %d = %+v, want CC ch=2 cc=13 val=%d", i, got[i], exp)
		}
	}
}

// TestParser_NoteOnZeroVelocityIsNoteOff codifies the convention that
// most controllers use to send note-off via velocity-0 note-on (saves
// running-status churn). We normalise so downstream listeners only
// need to handle one off form.
func TestParser_NoteOnZeroVelocityIsNoteOff(t *testing.T) {
	var p Parser
	got := p.Feed([]byte{0x90, 60, 0}, nil)
	if len(got) != 1 || got[0].Kind != KindNoteOff {
		t.Errorf("want NoteOff, got %+v", got)
	}
}

// TestParser_SysExSkipped checks SysEx delimiters consume their
// payload without surfacing any messages. The final CC after the
// SysEx end byte should still parse normally — running status is
// reset by the system message.
func TestParser_SysExSkipped(t *testing.T) {
	var p Parser
	stream := []byte{
		0xF0, 0x7E, 0x01, 0x02, 0x03, 0xF7, // SysEx
		0xB0, 0x07, 0x55, // CC after
	}
	got := p.Feed(stream, nil)
	if len(got) != 1 || got[0].Kind != KindControlChange {
		t.Fatalf("want one CC after sysex, got %+v", got)
	}
	if got[0].Data1 != 7 || got[0].Data2 != 0x55 {
		t.Errorf("CC fields wrong: %+v", got[0])
	}
}

// TestParser_RealtimeBytesIgnored ensures interleaved real-time bytes
// (clock, active-sensing) don't break a CC that's mid-transmission.
// MIDI 1.0 explicitly allows this interleaving.
func TestParser_RealtimeBytesIgnored(t *testing.T) {
	var p Parser
	stream := []byte{0xB0, 0xF8, 0x0D, 0xFE, 0x44}
	got := p.Feed(stream, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %+v", got)
	}
	if got[0].Data1 != 13 || got[0].Data2 != 0x44 {
		t.Errorf("CC fields wrong: %+v", got[0])
	}
}

// TestParser_ChunkedDelivery checks state survives across Feed calls
// — bytes can split anywhere, including mid-message, since the
// kernel's read boundary is arbitrary.
func TestParser_ChunkedDelivery(t *testing.T) {
	var p Parser
	got := p.Feed([]byte{0xB0}, nil)
	if len(got) != 0 {
		t.Errorf("status byte alone should not yield a message")
	}
	got = p.Feed([]byte{0x0D}, got)
	if len(got) != 0 {
		t.Errorf("one data byte should not complete a CC")
	}
	got = p.Feed([]byte{0x77}, got)
	if len(got) != 1 || got[0].Data2 != 0x77 {
		t.Errorf("third byte should complete the CC, got %+v", got)
	}
}

// TestParser_PitchBend pins the LSB-first / MSB-second wire order.
// Most controllers send bend on every detent so we want this nailed
// down for when we wire bend-to-param mappings later.
func TestParser_PitchBend(t *testing.T) {
	var p Parser
	got := p.Feed([]byte{0xE0, 0x7F, 0x40}, nil)
	if len(got) != 1 || got[0].Kind != KindPitchBend {
		t.Fatalf("want PitchBend, got %+v", got)
	}
	if got[0].Data1 != 0x7F || got[0].Data2 != 0x40 {
		t.Errorf("bend fields wrong: %+v", got[0])
	}
}
