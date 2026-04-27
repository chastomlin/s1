package engine

import (
	"log"
	"os"
	"testing"
	"time"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestEngine_LiveNoteOnSampleTrackPublishesTrigger pins the live-play
// path for sample tracks: a real-time NoteOn from a hardware controller
// reaches the bus as a SampleTrigger carrying the played pitch, so the
// mixer plays the sample varispeed-pitched to the key the user pressed.
func TestEngine_LiveNoteOnSampleTrackPublishesTrigger(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}

	eng.LiveNote("k", 67, 90, true)

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvNote || ev.NoteKind != protocol.SampleTrigger {
			t.Fatalf("got %+v, want SampleTrigger event", ev)
		}
		if ev.Track != "k" || ev.Note != 67 || ev.Vel != 90 {
			t.Errorf("event = %+v, want track=k note=67 vel=90", ev)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timeout waiting for live-note event")
	}
}

// TestEngine_LiveNoteOffOnSampleTrackIsIgnored asserts releases on a
// sample track do not produce a bus event — sample voices decay on
// their own envelope, so a NoteOff would just be noise.
func TestEngine_LiveNoteOffOnSampleTrackIsIgnored(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}

	eng.LiveNote("k", 67, 0, false)

	select {
	case ev := <-sub:
		if ev.Event == protocol.EvNote {
			t.Errorf("sample-track NoteOff should not publish, got %+v", ev)
		}
	case <-time.After(80 * time.Millisecond):
		// Good — silence is what we want.
	}
}

// TestEngine_LiveNotePitchedEmitsImmediateOnAndOff verifies the
// symmetric live-play path for pitched tracks: NoteOn appears on press,
// NoteOff appears on release, and crucially no auto-release timer fires
// (that's the audition path's behaviour, not LiveNote's).
func TestEngine_LiveNotePitchedEmitsImmediateOnAndOff(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Instruments: map[string]song.Instrument{"bass": {ID: "bass", Kind: "midi", Channel: 1}},
		Tracks: map[string]song.Track{
			"bass": {ID: "bass", Instrument: "bass", Channel: 1},
		},
	}

	eng.LiveNote("bass", 60, 100, true)

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvNote || ev.NoteKind != protocol.NoteOn {
			t.Fatalf("press got %+v, want NoteOn", ev)
		}
		if ev.Track != "bass" || ev.Note != 60 || ev.Vel != 100 {
			t.Errorf("press event = %+v, want track=bass note=60 vel=100", ev)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("timeout waiting for NoteOn")
	}

	// No auto-release: nothing else on the bus until we explicitly release.
	select {
	case ev := <-sub:
		if ev.Event == protocol.EvNote {
			t.Fatalf("unexpected event between press and release: %+v", ev)
		}
	case <-time.After(80 * time.Millisecond):
		// Good — quiet between press and release.
	}

	eng.LiveNote("bass", 60, 0, false)

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvNote || ev.NoteKind != protocol.NoteOff {
			t.Fatalf("release got %+v, want NoteOff", ev)
		}
		if ev.Track != "bass" || ev.Note != 60 {
			t.Errorf("release event = %+v, want track=bass note=60", ev)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("timeout waiting for NoteOff")
	}
}

// TestEngine_CmdSetMidiInTrackRoundTrips drives the protocol path the
// TUI uses to follow the highlighted track: a CmdSetMidiInTrack lands
// in the engine's live-MIDI target slot and is observable via
// MidiInTrack(). Empty Track clears it.
func TestEngine_CmdSetMidiInTrackRoundTrips(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))

	if got := eng.MidiInTrack(); got != "" {
		t.Errorf("initial MidiInTrack = %q, want \"\"", got)
	}
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdSetMidiInTrack, Track: "kick"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := eng.MidiInTrack(); got != "kick" {
		t.Errorf("after set, MidiInTrack = %q, want \"kick\"", got)
	}
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdSetMidiInTrack, Track: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := eng.MidiInTrack(); got != "" {
		t.Errorf("after clear, MidiInTrack = %q, want \"\"", got)
	}
}

// TestEngine_LiveNoteRespectsMute keeps live play silent on a muted
// track — the same rule scheduled events and audition follow.
func TestEngine_LiveNoteRespectsMute(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}
	_ = eng.Apply(protocol.Command{Cmd: protocol.CmdMute, Track: "k", Muted: true})

	eng.LiveNote("k", 67, 100, true)

	select {
	case ev := <-sub:
		if ev.Event == protocol.EvNote && (ev.NoteKind == protocol.NoteOn || ev.NoteKind == protocol.SampleTrigger) {
			t.Errorf("muted track still emitted %v — %+v", ev.NoteKind, ev)
		}
	case <-time.After(120 * time.Millisecond):
		// Good — silence is expected.
	}
}
