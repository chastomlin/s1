package engine

import (
	"reflect"
	"strings"
	"testing"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// parsePat is a tiny helper to keep test fixtures compact. It feeds a pattern
// body through the real ParsePattern so tests exercise the same code path as
// Load().
func parsePat(t *testing.T, name, body string) song.Pattern {
	t.Helper()
	p, err := song.ParsePattern(name, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse pattern %q: %v", name, err)
	}
	return p
}

// makeSong wires a song in which one section binds one or more per-track
// patterns. sectionName is also the arrangement-slot name. parts maps a
// track ID to its pattern (the patterns are placed into Song.Patterns
// under their own Name). All patterns must share Bars/BPB/Resolution.
func makeSong(sectionName string, parts map[string]song.Pattern, repeat int) *song.Song {
	sec := song.Section{
		Name:  sectionName,
		Parts: map[string]string{},
	}
	pats := map[string]song.Pattern{}
	first := true
	for id, p := range parts {
		pats[p.Name] = p
		sec.Parts[id] = p.Name
		if first {
			sec.Bars = p.Bars
			sec.BeatsPerBar = p.BeatsPerBar
			sec.Resolution = p.Resolution
			first = false
		}
	}
	return &song.Song{
		Patterns: pats,
		Sections: map[string]song.Section{sectionName: sec},
		Arrangement: []song.ArrangementSlot{
			{Section: sectionName, Repeat: repeat},
		},
	}
}

// makeSongArrangement wires a multi-section arrangement. For each pair
// (sectionName, pattern) given, the section binds trackID to that pattern.
// Convenience for boundary/hold tests that want two sections on one track.
func makeSongArrangement(trackID string, slots []struct {
	Section string
	Pattern song.Pattern
}) *song.Song {
	s := &song.Song{
		Patterns: map[string]song.Pattern{},
		Sections: map[string]song.Section{},
	}
	for _, sp := range slots {
		s.Patterns[sp.Pattern.Name] = sp.Pattern
		s.Sections[sp.Section] = song.Section{
			Name:        sp.Section,
			Bars:        sp.Pattern.Bars,
			BeatsPerBar: sp.Pattern.BeatsPerBar,
			Resolution:  sp.Pattern.Resolution,
			Parts:       map[string]string{trackID: sp.Pattern.Name},
		}
		s.Arrangement = append(s.Arrangement, song.ArrangementSlot{Section: sp.Section, Repeat: 1})
	}
	return s
}

func TestBuildTimeline_SamplesAndNotes(t *testing.T) {
	// 1 bar, res 4 = 16 cells, ticksPerCell = PPQN/4 = 24.
	kBody := `bars 1
X . . . X . . . X . . . X . . .
`
	bBody := `bars 1
C2 - - - . . . . D2 - - - - - - -
`
	s := makeSong("a", map[string]song.Pattern{
		"k": parsePat(t, "kp", kBody),
		"b": parsePat(t, "bp", bBody),
	}, 1)

	sch := NewScheduler(s)
	events := sch.Advance(1 << 30)

	want := []NoteEvent{
		// Both tracks start at tick 0; parts are iterated in sorted track-ID
		// order ("b" before "k").
		{AbsTick: 0, Track: "b", Section: "a", Kind: protocol.NoteOn, Note: 36, Vel: 100},
		{AbsTick: 0, Track: "k", Section: "a", Kind: protocol.SampleTrigger, Vel: 100},
		{AbsTick: 4 * 24, Track: "b", Section: "a", Kind: protocol.NoteOff, Note: 36},
		{AbsTick: 4 * 24, Track: "k", Section: "a", Kind: protocol.SampleTrigger, Vel: 100},
		{AbsTick: 8 * 24, Track: "b", Section: "a", Kind: protocol.NoteOn, Note: 38, Vel: 100},
		{AbsTick: 8 * 24, Track: "k", Section: "a", Kind: protocol.SampleTrigger, Vel: 100},
		{AbsTick: 12 * 24, Track: "k", Section: "a", Kind: protocol.SampleTrigger, Vel: 100},
		// D2 ties through end-of-section; close at section end (16 cells).
		{AbsTick: 16 * 24, Track: "b", Section: "a", Kind: protocol.NoteOff, Note: 38},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("timeline mismatch\n got: %+v\nwant: %+v", events, want)
	}
	if sch.EndTick() != 1*4*PPQN {
		t.Errorf("end tick: got %d want %d", sch.EndTick(), 1*4*PPQN)
	}
}

func TestBuildTimeline_NoteRetriggerReleasesFirst(t *testing.T) {
	// A new note while holding must emit the note-off for the previous note
	// at the same tick as the new note-on, before the note-on.
	// resolution 1 → ticksPerCell = PPQN = 96.
	body := `bars 1
resolution 1
C4 D4 . .
`
	s := makeSong("m", map[string]song.Pattern{
		"m": parsePat(t, "m", body),
	}, 1)
	sch := NewScheduler(s)
	ev := sch.Advance(1 << 30)

	// Cells: C4 at 0, D4 at 96, rest at 192, rest at 288.
	// Expected: on(C4)@0, off(C4)@96, on(D4)@96, off(D4)@192.
	want := []NoteEvent{
		{AbsTick: 0, Track: "m", Section: "m", Kind: protocol.NoteOn, Note: 60, Vel: 100},
		{AbsTick: 96, Track: "m", Section: "m", Kind: protocol.NoteOff, Note: 60},
		{AbsTick: 96, Track: "m", Section: "m", Kind: protocol.NoteOn, Note: 62, Vel: 100},
		{AbsTick: 192, Track: "m", Section: "m", Kind: protocol.NoteOff, Note: 62},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("retrigger mismatch\n got: %+v\nwant: %+v", ev, want)
	}
}

func TestBuildTimeline_HeldNoteClosedAtSectionBoundary(t *testing.T) {
	// A note that ties into the final cell must be closed at the section's
	// end, not leak into the next section.
	body1 := `bars 1
resolution 1
C4 - - -
`
	body2 := `bars 1
resolution 1
. . . .
`
	s := makeSongArrangement("m", []struct {
		Section string
		Pattern song.Pattern
	}{
		{Section: "p1", Pattern: parsePat(t, "p1", body1)},
		{Section: "p2", Pattern: parsePat(t, "p2", body2)},
	})
	sch := NewScheduler(s)
	ev := sch.Advance(1 << 30)

	want := []NoteEvent{
		{AbsTick: 0, Track: "m", Section: "p1", Kind: protocol.NoteOn, Note: 60, Vel: 100},
		// Section p1 length = 1 bar * 4 beats * 96 ticks = 384. Off fires there.
		{AbsTick: 384, Track: "m", Section: "p1", Kind: protocol.NoteOff, Note: 60},
	}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("boundary mismatch\n got: %+v\nwant: %+v", ev, want)
	}
}

func TestAdvance_IsIncremental(t *testing.T) {
	body := `bars 1
X . . . X . . . X . . . X . . .
`
	s := makeSong("a", map[string]song.Pattern{
		"k": parsePat(t, "a", body),
	}, 1)
	sch := NewScheduler(s)

	// Advance to tick 25 — should flush triggers at 0 and 24 (cells 0 and 1).
	// But cell 1 is '.', so only cell 0.
	got := sch.Advance(25)
	if len(got) != 1 || got[0].AbsTick != 0 {
		t.Fatalf("first window: got %+v", got)
	}
	// Advance to 100 — cells 2,3,4 would be at 48,72,96. Only cell 4 is 'X'.
	got = sch.Advance(100)
	if len(got) != 1 || got[0].AbsTick != 96 {
		t.Fatalf("second window: got %+v", got)
	}
	// Flush rest.
	got = sch.Advance(1 << 30)
	if len(got) != 2 {
		t.Fatalf("final window: want 2 triggers, got %d: %+v", len(got), got)
	}
}

func TestSeekTo_SkipsPastEvents(t *testing.T) {
	body := `bars 1
X . . . X . . . X . . . X . . .
`
	s := makeSong("a", map[string]song.Pattern{
		"k": parsePat(t, "a", body),
	}, 1)
	sch := NewScheduler(s)
	sch.SeekTo(97) // past cells 0, 1 (trigger at 0) and 4 (trigger at 96).
	got := sch.Advance(1 << 30)
	if len(got) != 2 {
		t.Fatalf("after seek: want 2 remaining triggers, got %d: %+v", len(got), got)
	}
	if got[0].AbsTick != 192 || got[1].AbsTick != 288 {
		t.Errorf("after seek: unexpected ticks %d,%d", got[0].AbsTick, got[1].AbsTick)
	}
}

// A slot with Repeat=3 should lay the section down three times back-to-back.
func TestBuildTimeline_SlotRepeats(t *testing.T) {
	body := `bars 1
resolution 1
X . . .
`
	s := makeSong("a", map[string]song.Pattern{
		"k": parsePat(t, "a", body),
	}, 3)
	sch := NewScheduler(s)
	ev := sch.Advance(1 << 30)

	// One bar = 4 beats × 96 ticks = 384. Trigger at 0, 384, 768.
	if len(ev) != 3 {
		t.Fatalf("want 3 triggers across 3 repeats, got %d: %+v", len(ev), ev)
	}
	wantTicks := []int{0, 384, 768}
	for i, want := range wantTicks {
		if ev[i].AbsTick != want {
			t.Errorf("repeat %d: AbsTick = %d, want %d", i, ev[i].AbsTick, want)
		}
		if ev[i].Section != "a" {
			t.Errorf("repeat %d: section = %q, want %q", i, ev[i].Section, "a")
		}
	}
	if sch.EndTick() != 3*384 {
		t.Errorf("end tick = %d, want %d", sch.EndTick(), 3*384)
	}
}

func TestNilSongYieldsNoopScheduler(t *testing.T) {
	sch := NewScheduler(nil)
	if got := sch.Advance(1000); got != nil {
		t.Errorf("nil scheduler advance: got %+v", got)
	}
	sch.SeekTo(500)
	sch.Reset()
	if sch.EndTick() != 0 {
		t.Errorf("nil scheduler end tick: got %d", sch.EndTick())
	}
}
