package mixer

import (
	"io"
	"log"
	"math"
	"path/filepath"
	"testing"
	"time"

	"seqone/internal/engine"
	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestEndToEnd_AuditionPitchesSample wires a real engine + mixer +
// bridge over a real loaded song and confirms that a CmdAudition with
// a different Note than the track's base_note actually produces a
// pitched voice in the mixer. This is the path the section-view
// live-audition keystroke goes through; a regression here would mean
// the user hears the sample but never a pitch change.
func TestEndToEnd_AuditionPitchesSample(t *testing.T) {
	// Build a minimal project: sample track "k" with note=36 and no
	// explicit base_note (so BaseNote defaults to 36).
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Tracks["k"].BaseNote != 36 {
		t.Fatalf("BaseNote should default to note (36), got %d", s.Tracks["k"].BaseNote)
	}

	// Seed the mixer with a fake sample so the bridge has something
	// real to trigger. Native sample rate == device rate, so step = 1.0
	// corresponds to native pitch.
	m := NewForTest(map[string]*Sample{
		"k": testSample("k", 4096, 0.5),
	})
	m.deviceRate = 44100

	// Stand up the engine, point the bridge at a resolver that reads
	// from engine.CurrentSong() the same way seqoned does.
	eng := engine.New(log.New(io.Discard, "", 0))
	// Engine.song is unexported; the public way to load is CmdLoad,
	// which also attempts to re-init the scheduler etc. Use it.
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: songPath}); err != nil {
		t.Fatalf("load: %v", err)
	}

	resolve := func(id string) TrackRoute {
		cs := eng.CurrentSong()
		if cs == nil {
			return TrackRoute{}
		}
		t, ok := cs.Tracks[id]
		if !ok {
			return TrackRoute{}
		}
		return TrackRoute{SampleID: t.Sample, BaseNote: t.BaseNote}
	}
	// The track's Sample is "k" (same name as the sample we seeded).
	br := NewBridge(m, resolve, log.New(io.Discard, "", 0))

	// Subscribe the bridge to the engine bus and run it in a goroutine
	// so Apply(audition) → Publish → Run → mixer.Trigger round-trips.
	events, unsub := eng.Bus().Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		br.Run(events)
		close(done)
	}()

	// Audition at C5 (72) — one octave above base (36+12*3=72, actually
	// that's three octaves so ratio = 2^3 = 8.0). Use 48 for +1 octave:
	// 2^((48-36)/12) = 2^1 = 2.0.
	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdAudition, Track: "k", Note: 48, Vel: 100,
	}); err != nil {
		t.Fatalf("audition: %v", err)
	}

	// Give the bridge goroutine time to consume the published event
	// and send a trigger to the mixer channel, then pump onData so
	// the trigger materialises into a voice.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		runCallback(m, 0)
		if m.ActiveVoices() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if m.ActiveVoices() == 0 {
		t.Fatal("no voice activated — audition didn't reach the mixer")
	}
	var step float64
	for i := range m.voices {
		if m.voices[i].active {
			step = m.voices[i].step
			break
		}
	}
	want := 2.0
	if math.Abs(step-want) > 1e-6 {
		t.Errorf("voice step = %f, want %f (one octave up from note=36 to note=48)", step, want)
	}
	// Clean up so the bridge goroutine exits.
	unsub()
	// Drain the closed channel (bridge will exit).
	<-done
}
