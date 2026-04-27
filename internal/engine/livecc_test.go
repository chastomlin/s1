package engine

import (
	"log"
	"os"
	"testing"
	"time"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestEngine_LiveCCModWheelPassesThroughOnPitchedTrack verifies the
// pass-through path for CC 1 (mod wheel): a pitched track's channel is
// looked up from the song, and an EvCC carrying that channel is
// published so the rtpmidi bridge can forward it to the synth.
func TestEngine_LiveCCModWheelPassesThroughOnPitchedTrack(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Instruments: map[string]song.Instrument{"bass": {ID: "bass", Kind: "midi", Channel: 3}},
		Tracks: map[string]song.Track{
			"bass": {ID: "bass", Instrument: "bass", Channel: 3},
		},
	}

	eng.LiveCC("bass", 1, 90)

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvCC {
			t.Fatalf("got %+v, want EvCC", ev)
		}
		if ev.Channel != 3 || ev.CC != 1 || ev.CCValue != 90 {
			t.Errorf("event = %+v, want channel=3 cc=1 ccvalue=90", ev)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("timeout waiting for EvCC")
	}
}

// TestEngine_LiveCCModWheelOnSampleTrackDropped ensures pass-through
// CCs against sample tracks produce no event — sample tracks have no
// instrument-channel out-bound destination, so forwarding would just
// confuse downstream synths.
func TestEngine_LiveCCModWheelOnSampleTrackDropped(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}

	eng.LiveCC("k", 1, 90)

	select {
	case ev := <-sub:
		if ev.Event == protocol.EvCC {
			t.Errorf("sample track produced EvCC: %+v", ev)
		}
	case <-time.After(80 * time.Millisecond):
		// Good — no event is the expected behaviour.
	}
}

// TestEngine_LiveCCVolumeUpdatesGain locks the GM mapping for CC 7:
// linear, halfway = unity. value 64 → gain 1.0.
func TestEngine_LiveCCVolumeUpdatesGain(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60, Gain: 0.5},
		},
	}

	eng.LiveCC("k", 7, 64)

	got := eng.CurrentSong().Tracks["k"].Gain
	if got < 0.99 || got > 1.01 {
		t.Errorf("CC7 value 64 produced gain %v, want ≈1.0 (unity)", got)
	}

	eng.LiveCC("k", 7, 0)
	if g := eng.CurrentSong().Tracks["k"].Gain; g != 0 {
		t.Errorf("CC7 value 0 produced gain %v, want 0", g)
	}
}

// TestEngine_LiveCCPanCentersAt64 locks CC 10's centre point: value 64
// must produce exactly 0.0 pan, not a fractional drift, so a controller
// at rest doesn't slowly wander the image.
func TestEngine_LiveCCPanCentersAt64(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60, Pan: -0.5},
		},
	}

	eng.LiveCC("k", 10, 64)
	if p := eng.CurrentSong().Tracks["k"].Pan; p != 0 {
		t.Errorf("CC10 value 64 produced pan %v, want exactly 0", p)
	}

	eng.LiveCC("k", 10, 0)
	if p := eng.CurrentSong().Tracks["k"].Pan; p != -1 {
		t.Errorf("CC10 value 0 produced pan %v, want -1 (full left)", p)
	}

	eng.LiveCC("k", 10, 127)
	if p := eng.CurrentSong().Tracks["k"].Pan; p != 1 {
		t.Errorf("CC10 value 127 produced pan %v, want 1 (full right)", p)
	}
}

// TestEngine_LiveCCPanicEmitsAllOff confirms CC 120 / 123 act as panic
// commands regardless of which track is configured — they should clear
// stuck notes everywhere, mirroring what the engine does on stop/seek.
func TestEngine_LiveCCPanicEmitsAllOff(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	// No song — panic CCs must work even when nothing is loaded.
	eng.LiveCC("", 123, 0)

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvNote || ev.NoteKind != protocol.NoteAllOff {
			t.Errorf("got %+v, want NoteAllOff", ev)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("timeout waiting for NoteAllOff from panic CC")
	}
}
