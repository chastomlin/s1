package engine

import (
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestScheduler_CellNoteOnSampleTrackPitchesAsTrigger pins the routing
// rule: a pitched cell (C4, D#3, …) on a sample track should reach the
// mixer as a SampleTrigger carrying the pitch, not a MIDI note-on. This
// is what lets sample tracks be varispeed-pitched while keeping MIDI
// drum routing on the same track.
func TestScheduler_CellNoteOnSampleTrackPitchesAsTrigger(t *testing.T) {
	body := `bars 1
resolution 1
C4 . . .
`
	p := parsePat(t, "kp", body)
	s := &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
		Patterns: map[string]song.Pattern{"kp": p},
		Sections: map[string]song.Section{
			"a": {
				Name:        "a",
				Bars:        1,
				BeatsPerBar: 4,
				Resolution:  1,
				Parts:       map[string]string{"k": "kp"},
			},
		},
		Arrangement: []song.ArrangementSlot{{Section: "a", Repeat: 1}},
	}
	sch := NewScheduler(s)
	ev := sch.Advance(1 << 30)

	if len(ev) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(ev), ev)
	}
	if ev[0].Kind != protocol.SampleTrigger {
		t.Errorf("kind = %v, want SampleTrigger (pitched sample)", ev[0].Kind)
	}
	if ev[0].Note != 60 /* C4 */ {
		t.Errorf("note = %d, want 60 (C4 pitched trigger)", ev[0].Note)
	}
}

// TestEngine_CmdAuditionOnSampleTrackPublishesTrigger drives the full
// CmdAudition path: the engine should translate it into a bus SampleTrigger
// event carrying the requested pitch so the mixer bridge can pick it up
// exactly like a scheduled cell.
func TestEngine_CmdAuditionOnSampleTrackPublishesTrigger(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}

	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdAudition, Track: "k", Note: 72, Vel: 100,
	}); err != nil {
		t.Fatalf("audition: %v", err)
	}

	select {
	case ev := <-sub:
		if ev.Event != protocol.EvNote || ev.NoteKind != protocol.SampleTrigger {
			t.Errorf("got %+v, want SampleTrigger event", ev)
		}
		if ev.Track != "k" || ev.Note != 72 {
			t.Errorf("event = %+v, want track=k note=72", ev)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timeout waiting for audition event")
	}
}

// TestEngine_CmdAuditionOnPitchedTrackEmitsNoteOnAndDelayedOff verifies
// that pitched auditions fire a NoteOn immediately and a NoteOff after
// the auto-release timer so keyboard-held notes don't stick on.
func TestEngine_CmdAuditionOnPitchedTrackEmitsNoteOnAndDelayedOff(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Instruments: map[string]song.Instrument{"bass": {ID: "bass", Kind: "midi", Channel: 1}},
		Tracks: map[string]song.Track{
			"bass": {ID: "bass", Instrument: "bass", Channel: 1},
		},
	}

	if err := eng.Apply(protocol.Command{
		Cmd: protocol.CmdAudition, Track: "bass", Note: 60, Vel: 100,
	}); err != nil {
		t.Fatalf("audition: %v", err)
	}

	// Drain events over enough wall time to see both the NoteOn and the
	// delayed NoteOff (auditionReleaseDuration ~= 400ms).
	deadline := time.After(auditionReleaseDuration + 300*time.Millisecond)
	var kinds []protocol.NoteKind
loop:
	for {
		select {
		case ev := <-sub:
			if ev.Event == protocol.EvNote {
				kinds = append(kinds, ev.NoteKind)
				if len(kinds) >= 2 {
					break loop
				}
			}
		case <-deadline:
			break loop
		}
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	want := []protocol.NoteKind{protocol.NoteOn, protocol.NoteOff}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("got %v, want %v (NoteOn + delayed NoteOff)", kinds, want)
	}
}

// TestEngine_CmdAuditionRespectsMute keeps the audition silent when the
// track is muted (matching what playback would do), so muting works both
// for scheduled events and live auditioning.
func TestEngine_CmdAuditionRespectsMute(t *testing.T) {
	eng := New(log.New(os.Stderr, "", 0))
	sub, unsub := eng.Bus().Subscribe()
	defer unsub()

	eng.song = &song.Song{
		Tracks: map[string]song.Track{
			"k": {ID: "k", Sample: "k_smp", Channel: 10, Note: 36, BaseNote: 60},
		},
	}
	_ = eng.Apply(protocol.Command{Cmd: protocol.CmdMute, Track: "k", Muted: true})
	if err := eng.Apply(protocol.Command{Cmd: protocol.CmdAudition, Track: "k", Note: 0, Vel: 100}); err != nil {
		t.Fatalf("audition: %v", err)
	}

	select {
	case ev := <-sub:
		// Non-note events (state/position) are fine; only fail on a
		// note being published.
		if ev.Event == protocol.EvNote && (ev.NoteKind == protocol.NoteOn || ev.NoteKind == protocol.SampleTrigger) {
			t.Errorf("muted track still emitted %v — %+v", ev.NoteKind, ev)
		}
	case <-time.After(150 * time.Millisecond):
		// Good — silence is the expected behavior.
	}
	_ = strings.Builder{} // keep strings import
}
