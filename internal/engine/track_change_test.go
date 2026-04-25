package engine

import (
	"log"
	"os"
	"testing"
	"time"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestEngine_CmdSetTrackPanUpdatesSong pins the Apply path: a
// CmdSetTrackPan should both mutate the engine's song and publish
// EvTrackChanged so seqoned can reconfigure the mixer.
func TestEngine_CmdSetTrackPanUpdatesSong(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60, Gain: 1, Pan: 0},
		},
	}
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdSetTrackPan, Track: "k", Pan: -0.5,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Engine state: track copy was replaced via clone-and-swap.
	cur := eng.CurrentSong()
	if cur == nil {
		t.Fatal("song went nil")
	}
	if got := cur.Tracks["k"].Pan; got != -0.5 {
		t.Errorf("track Pan = %f, want -0.5", got)
	}

	// Bus event was published.
	select {
	case ev := <-sub:
		if ev.Event != protocol.EvTrackChanged {
			t.Errorf("got %+v, want EvTrackChanged", ev)
		}
		if ev.Track != "k" {
			t.Errorf("event Track = %q, want k", ev.Track)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no EvTrackChanged event published")
	}
}

// TestEngine_CmdSetTrackEQUpdatesAllFields ensures the full EQ struct
// makes it through.
func TestEngine_CmdSetTrackEQUpdatesAllFields(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}

	cfg := protocol.EQConfigCmd{
		LowFreq: 250, LowGain: 3.0,
		MidFreq: 700, MidQ: 0.5, MidGain: -2.0,
		HighFreq: 9000, HighGain: 1.5,
	}
	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdSetTrackEQ, Track: "k", EQ: &cfg,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := eng.CurrentSong().Tracks["k"].EQ
	want := song.EQConfig{
		LowFreq: 250, LowGain: 3.0,
		MidFreq: 700, MidQ: 0.5, MidGain: -2.0,
		HighFreq: 9000, HighGain: 1.5,
	}
	if got != want {
		t.Errorf("EQ mismatch:\n got  %+v\n want %+v", got, want)
	}
}
