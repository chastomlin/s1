package engine

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seqone/internal/protocol"
)

// writeDemo writes a minimal 2-pattern song to a temp dir and returns the
// song.toml path. Keeps tests self-contained — no dependency on examples/.
func writeDemo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	patDir := filepath.Join(dir, "patterns")
	if err := os.Mkdir(patDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// resolution 1 → one cell per beat; 1 bar * 4 beats = 4 cells.
	// Per-track patterns: one file per (section, track) binding.
	kA := "bars 1\nresolution 1\n\nX . . .\n"
	mA := "bars 1\nresolution 1\n\nC4 - - -\n"
	kB := "bars 1\nresolution 1\n\n. X . .\n"
	mB := "bars 1\nresolution 1\n\n. . . .\n"
	for name, body := range map[string]string{
		"a-k.pat": kA, "a-m.pat": mA, "b-k.pat": kB, "b-m.pat": mB,
	} {
		if err := os.WriteFile(filepath.Join(patDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	samplesDir := filepath.Join(dir, "samples")
	if err := os.MkdirAll(samplesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(samplesDir, "k.wav"), []byte("RIFFdummy"), 0o644); err != nil {
		t.Fatal(err)
	}

	toml := `[project]
title = "t"
bpm = 240
time_signature = "4/4"

[[samples]]
id = "k_smp"
path = "samples/k.wav"

[[instruments]]
id = "m_inst"
kind = "midi"
channel = 1
program = 0

[[tracks]]
id = "k"
sample = "k_smp"
channel = 10
note = 36

[[tracks]]
id = "m"
instrument = "m_inst"
channel = 1

[[sections]]
name = "a"
bars = 1
parts = { k = "a-k", m = "a-m" }

[[sections]]
name = "b"
bars = 1
parts = { k = "b-k", m = "b-m" }

[song]
arrangement = ["a", "b"]
`
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// drain collects events from ch until quiet for `quiet` duration or deadline
// hits, then returns what it saw.
func drain(ch <-chan protocol.Event, quiet, deadline time.Duration) []protocol.Event {
	var out []protocol.Event
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	hardStop := time.After(deadline)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quiet)
		case <-timer.C:
			return out
		case <-hardStop:
			return out
		}
	}
}

func TestEngine_EmitsNoteEventsWhilePlaying(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: writeDemo(t)}); err != nil {
		t.Fatalf("load: %v", err)
	}

	stop := make(chan struct{})
	go eng.Run(stop)
	defer close(stop)

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdPlay}); err != nil {
		t.Fatalf("play: %v", err)
	}

	// At 240 BPM, 1 beat = 250 ms, 1 bar (4 beats) = 1 s. The song is 2
	// bars, so give it ~2.2 s to fire everything plus the close-outs.
	events := drain(sub, 300*time.Millisecond, 3*time.Second)

	// Collect note events only.
	var notes []protocol.Event
	for _, ev := range events {
		if ev.Event == protocol.EvNote {
			notes = append(notes, ev)
		}
	}

	// Expect in order:
	//   all_off (from load)
	//   trigger k @ patA cell 0
	//   note_on m=C4 @ patA cell 0
	//   note_off m=C4 @ end of patA (pattern boundary)
	//   trigger k @ patB cell 1
	// Exact count and ordering matter; we assert kinds and tracks in sequence.
	type want struct {
		kind  protocol.NoteKind
		track string
	}
	expect := []want{
		{protocol.NoteAllOff, ""},
		{protocol.SampleTrigger, "k"},
		{protocol.NoteOn, "m"},
		{protocol.NoteOff, "m"},
		{protocol.SampleTrigger, "k"},
	}
	if len(notes) < len(expect) {
		t.Fatalf("too few note events: got %d, want at least %d\nall: %+v", len(notes), len(expect), notes)
	}
	for i, w := range expect {
		if notes[i].NoteKind != w.kind || notes[i].Track != w.track {
			t.Errorf("note[%d] = {%s %q}, want {%s %q}", i, notes[i].NoteKind, notes[i].Track, w.kind, w.track)
		}
	}
}

func TestEngine_StopEmitsAllOffAndRewinds(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: writeDemo(t)}); err != nil {
		t.Fatalf("load: %v", err)
	}
	stop := make(chan struct{})
	go eng.Run(stop)
	defer close(stop)

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdPlay}); err != nil {
		t.Fatalf("play: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdStop}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	events := drain(sub, 200*time.Millisecond, 2*time.Second)

	// Count all_off notes — must be at least 2 (load + stop).
	var allOffs int
	for _, ev := range events {
		if ev.Event == protocol.EvNote && ev.NoteKind == protocol.NoteAllOff {
			allOffs++
		}
	}
	if allOffs < 2 {
		t.Errorf("expected at least 2 all_off events (load + stop), got %d", allOffs)
	}
}

// At non-round BPMs the old float-based seek math landed ~1 ns below the
// target, and ElapsedToPosition then truncated one tick short — seek(2,1)
// at 112 BPM visibly ended up at {bar 1, beat 4, tick 95}. This test pins
// the correct integer-math behaviour so we don't regress.
func TestEngine_SeekExactAtAwkwardBPM(t *testing.T) {
	dir := t.TempDir()
	patDir := filepath.Join(dir, "patterns")
	if err := os.Mkdir(patDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 4 bars * 4 beats * 4 res = 64 cells of rest.
	dots := ""
	for i := 0; i < 64; i++ {
		if i > 0 {
			dots += " "
		}
		dots += "."
	}
	if err := os.WriteFile(filepath.Join(patDir, "a.pat"), []byte("bars 4\n\n"+dots+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	songToml := `[project]
bpm = 112

[[sections]]
name = "a"
bars = 4
parts = {}

[song]
arrangement = ["a"]
`
	songPath := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(songPath, []byte(songToml), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := New(log.New(os.Stderr, "", 0))
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: songPath}); err != nil {
		t.Fatalf("load: %v", err)
	}
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	// A sequence of seeks as the TUI would send them. Each must land
	// exactly on the target bar, beat 1, tick 0.
	for _, bar := range []int{2, 3, 4} {
		if err := eng.Apply(protocol.Command{Cmd: protocol.CmdSeek, Bar: bar, Beat: 1}); err != nil {
			t.Fatalf("seek %d: %v", bar, err)
		}
	}
	events := drain(sub, 100*time.Millisecond, 500*time.Millisecond)

	var positions []protocol.Event
	for _, ev := range events {
		if ev.Event == protocol.EvPosition {
			positions = append(positions, ev)
		}
	}
	if len(positions) < 3 {
		t.Fatalf("want 3 position events, got %d", len(positions))
	}
	// Keep only the last 3 — any prior ones come from load.
	got := positions[len(positions)-3:]
	for i, want := range []int{2, 3, 4} {
		if got[i].Bar != want || got[i].Beat != 1 || got[i].Tick != 0 {
			t.Errorf("seek %d: position = {bar:%d beat:%d tick:%d}, want {bar:%d beat:1 tick:0}",
				want, got[i].Bar, got[i].Beat, got[i].Tick, want)
		}
	}
}

// Seeking while the transport is stopped must still publish an EvPosition
// event so the TUI bar/beat display updates.
func TestEngine_SeekWhileStoppedPublishesPosition(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: writeDemo(t)}); err != nil {
		t.Fatalf("load: %v", err)
	}
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdSeek, Bar: 3, Beat: 1}); err != nil {
		t.Fatalf("seek: %v", err)
	}

	events := drain(sub, 100*time.Millisecond, 500*time.Millisecond)
	var pos *protocol.Event
	for i := range events {
		if events[i].Event == protocol.EvPosition {
			pos = &events[i]
		}
	}
	if pos == nil {
		t.Fatal("no EvPosition event after seek while stopped")
	}
	if pos.Bar != 3 || pos.Beat != 1 {
		t.Errorf("seek target not reached: bar=%d beat=%d, want bar=3 beat=1", pos.Bar, pos.Beat)
	}
}

func TestEngine_LoopStoresRegionAndDisables(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))

	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdLoop, FromBar: 3, ToBar: 7, Enabled: true,
	}); err != nil {
		t.Fatalf("loop enable: %v", err)
	}
	eng.mu.Lock()
	enabled, from, to := eng.loopEnabled, eng.loopFromBar, eng.loopToBar
	eng.mu.Unlock()
	if !enabled || from != 3 || to != 7 {
		t.Errorf("after enable: enabled=%v from=%d to=%d, want true 3 7", enabled, from, to)
	}

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoop, Enabled: false}); err != nil {
		t.Fatalf("loop disable: %v", err)
	}
	eng.mu.Lock()
	enabled = eng.loopEnabled
	eng.mu.Unlock()
	if enabled {
		t.Error("after disable: want enabled=false")
	}
}

func TestEngine_LoopRejectsInvalidBounds(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	// to <= from is not a valid loop region.
	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdLoop, FromBar: 5, ToBar: 5, Enabled: true,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eng.mu.Lock()
	enabled := eng.loopEnabled
	eng.mu.Unlock()
	if enabled {
		t.Error("loop with to<=from should not enable")
	}
}

// The transport must wrap from loopToBar back to loopFromBar while playing.
// We use a tight 1-bar loop at a fast tempo and verify the position never
// exceeds bar 2 over a duration that would otherwise sweep past it.
func TestEngine_LoopWrapsPlayhead(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: writeDemo(t)}); err != nil {
		t.Fatalf("load: %v", err)
	}
	// Loop [1,2): replay bar 1 forever.
	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdLoop, FromBar: 1, ToBar: 2, Enabled: true,
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	stop := make(chan struct{})
	go eng.Run(stop)
	defer close(stop)

	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdPlay}); err != nil {
		t.Fatalf("play: %v", err)
	}
	// At 240 BPM 1 bar = 1s. Play for ~2.5s — without looping this would
	// reach bar 3. With the loop we must never exceed bar 2.
	events := drain(sub, 300*time.Millisecond, 2800*time.Millisecond)

	maxBar := 0
	for _, ev := range events {
		if ev.Event == protocol.EvPosition && ev.Bar > maxBar {
			maxBar = ev.Bar
		}
	}
	if maxBar == 0 {
		t.Fatal("no position events observed")
	}
	if maxBar >= 2 {
		t.Errorf("loop wrap failed: max observed bar = %d, want < 2", maxBar)
	}
}
