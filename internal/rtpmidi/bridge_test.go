package rtpmidi

import (
	"bytes"
	"io"
	"log"
	"sync"
	"testing"

	"seqone/internal/protocol"
)

// captureSender records every Send call.
type captureSender struct {
	mu   sync.Mutex
	sent [][]byte
}

func (c *captureSender) Send(midi []byte) error {
	c.mu.Lock()
	c.sent = append(c.sent, append([]byte(nil), midi...))
	c.mu.Unlock()
	return nil
}

func (c *captureSender) Msgs() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.sent))
	copy(out, c.sent)
	return out
}

// staticResolver is a fixed map for tests.
func staticResolver(m map[string]TrackRouting) Resolver {
	return func(id string) (TrackRouting, bool) {
		r, ok := m[id]
		return r, ok
	}
}

func TestBridge_PitchedTrackUsesRoutedChannel(t *testing.T) {
	cap := &captureSender{}
	resolve := staticResolver(map[string]TrackRouting{
		"bass": {Channel: 3, IsSample: false},
	})
	br := NewBridge(cap, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 4)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteOn, Track: "bass", Note: 36, Vel: 100}
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteOff, Track: "bass", Note: 36}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 2 {
		t.Fatalf("want 2 msgs, got %d: %v", len(m), m)
	}
	// Channel 3 user-facing = wire 0x02, so note-on byte is 0x92.
	if !bytes.Equal(m[0], []byte{0x92, 36, 100}) {
		t.Errorf("note-on on ch3: got %x want 92 24 64", m[0])
	}
	if !bytes.Equal(m[1], []byte{0x82, 36, 0}) {
		t.Errorf("note-off on ch3: got %x want 82 24 00", m[1])
	}
}

func TestBridge_SampleTriggerUsesRoutedNoteAndChannel(t *testing.T) {
	cap := &captureSender{}
	resolve := staticResolver(map[string]TrackRouting{
		"k": {Channel: 10, Note: 36, IsSample: true},
	})
	br := NewBridge(cap, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 1 {
		t.Fatalf("want 1 msg, got %d", len(m))
	}
	// Channel 10 user-facing = wire 0x09, note = 36, vel = 100.
	if !bytes.Equal(m[0], []byte{0x99, 36, 100}) {
		t.Errorf("drum trigger: got %x want 99 24 64", m[0])
	}
}

func TestBridge_UnresolvedSampleTriggerDropped(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	close(ch)
	br.Run(ch)

	if got := cap.Msgs(); len(got) != 0 {
		t.Errorf("expected unresolved sample trigger to be dropped, got %x", got)
	}
}

func TestBridge_UnresolvedPitchedFallsBackToChannel1(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteOn, Track: "unknown", Note: 60, Vel: 100}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 1 {
		t.Fatalf("want 1 msg, got %d", len(m))
	}
	if m[0][0] != 0x90 { // wire channel 0 = user channel 1
		t.Errorf("fallback channel: got status %x want 90", m[0][0])
	}
}

func TestBridge_AllOffFanout(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteAllOff}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 16 {
		t.Fatalf("want 16 all-off msgs, got %d", len(m))
	}
	if !bytes.Equal(m[0], []byte{0xB0, 123, 0}) {
		t.Errorf("first all-off: %x", m[0])
	}
	if !bytes.Equal(m[15], []byte{0xBF, 123, 0}) {
		t.Errorf("last all-off: %x", m[15])
	}
}

func TestBridge_SendProgramChanges(t *testing.T) {
	cap := &captureSender{}
	br := NewBridge(cap, nil, log.New(io.Discard, "", 0))
	br.SendProgramChanges([]ProgramAssignment{
		{Channel: 1, Program: 33},  // bass → wire 0xC0 0x21
		{Channel: 2, Program: 81},  // lead → wire 0xC1 0x51
		{Channel: 0, Program: 5},   // invalid channel — skip
		{Channel: 17, Program: 5},  // invalid channel — skip
		{Channel: 3, Program: -1},  // invalid program — skip
		{Channel: 3, Program: 128}, // invalid program — skip
	})

	m := cap.Msgs()
	if len(m) != 2 {
		t.Fatalf("want 2 PCs (others skipped), got %d: %x", len(m), m)
	}
	if !bytes.Equal(m[0], []byte{0xC0, 33}) {
		t.Errorf("ch1 program 33: got %x want C0 21", m[0])
	}
	if !bytes.Equal(m[1], []byte{0xC1, 81}) {
		t.Errorf("ch2 program 81: got %x want C1 51", m[1])
	}
}

func TestBridge_PitchedTrackWithSampleRoutingIgnoresSampleMapping(t *testing.T) {
	// If a track has sample routing but the engine sends a pitched note
	// event for it (unusual but possible during schema errors), we fall
	// back rather than conflate. Verified by using fallback channel 0.
	cap := &captureSender{}
	resolve := staticResolver(map[string]TrackRouting{
		"k": {Channel: 10, Note: 36, IsSample: true},
	})
	br := NewBridge(cap, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteOn, Track: "k", Note: 60, Vel: 100}
	close(ch)
	br.Run(ch)

	m := cap.Msgs()
	if len(m) != 1 || m[0][0] != 0x90 {
		t.Errorf("expected fallback ch1 for mismatched type: %x", m)
	}
}
