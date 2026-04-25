package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// loadDemo returns the real demo song so render tests exercise the full
// stack — schema, patterns, arrangement.
func loadDemo(t *testing.T) *song.Song {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "..", "..", "examples", "demo", "song.toml")
	s, err := song.Load(path)
	if err != nil {
		t.Fatalf("load demo: %v", err)
	}
	return s
}

func TestView_ZeroWidthShowsInitializing(t *testing.T) {
	m := New(nil, "")
	if got := m.View(); got != "initializing..." {
		t.Errorf("got %q, want initializing...", got)
	}
}

func TestView_RendersSectionAndTransport(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.state = protocol.StatePlaying
	// Bar 8 is inside the first "chorus" slot (bars 7..10 in the demo).
	m.position = protocol.Event{Bar: 8, Beat: 2, Section: "chorus"}
	m.bpm = 120
	m.mode = modeSection
	m.sectionView = "chorus"

	out := m.View()

	// Section-view column headers.
	for _, s := range []string{"#", "CH", "NAME"} {
		if !strings.Contains(out, s) {
			t.Errorf("expected column header %q in view", s)
		}
	}
	// Chorus binds all five demo tracks, by display name.
	for _, name := range []string{"Kick", "Snare", "Hat", "Bass", "Lead"} {
		if !strings.Contains(out, name) {
			t.Errorf("expected track name %q in view", name)
		}
	}
	// Transport strip.
	if !strings.Contains(out, "playing") {
		t.Error("expected 'playing' in transport line")
	}
	if !strings.Contains(out, "bpm 120") {
		t.Error("expected 'bpm 120' in transport line")
	}
	// Section header shows the section name and bar count.
	if !strings.Contains(out, "section: chorus") {
		t.Error("expected 'section: chorus' header")
	}
	// Playhead caret appears somewhere.
	if !strings.Contains(out, "▲") {
		t.Error("expected playhead marker ▲")
	}
}

func TestView_ArrangementShowsSectionRibbon(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)

	out := m.View()
	if !strings.Contains(out, "Arrangement") {
		t.Error("expected 'Arrangement' heading in arrangement view")
	}
	for _, name := range []string{"intro", "verse", "chorus", "bridge", "outro"} {
		if !strings.Contains(out, name) {
			t.Errorf("expected section %q in arrangement ribbon", name)
		}
	}
}

func TestView_FlashHighlightsRecentTriggers(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.mode = modeSection
	m.sectionView = "chorus"
	m.flash["k"] = time.Now()

	out := m.View()
	// The flash glyph ● should appear at least once — the kick row is hot.
	if !strings.Contains(out, "●") {
		t.Error("expected flash glyph ● for recently-triggered track")
	}
}

func TestUpdate_ArrowsMoveTrackSelection(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.mode = modeSection
	m.sectionView = "chorus" // 5-track section

	// Chorus has 5 tracks. Start at 0; down 3× → 3; up 1× → 2.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(Model).selectedTrackIdx; got != 3 {
		t.Errorf("after 3×down: selected=%d want 3", got)
	}
	next, _ = next.(Model).Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := next.(Model).selectedTrackIdx; got != 2 {
		t.Errorf("after up: selected=%d want 2", got)
	}
}

func TestUpdate_MuteKeyTogglesAndCommands(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.mode = modeSection
	m.sectionView = "chorus"

	// Selection defaults to track 0. Press 'm' to mute it.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	mm := next.(Model)
	id := mm.selectedTrackID()
	if id == "" {
		t.Fatal("no track selected")
	}
	if !mm.mutes[id] {
		t.Errorf("expected mutes[%s] = true", id)
	}
	if cmd == nil {
		t.Error("expected a tea.Cmd carrying the mute command")
	}

	// Second press clears the mute.
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if next.(Model).mutes[id] {
		t.Errorf("expected mutes[%s] cleared after second toggle", id)
	}
}

func TestUpdate_SoloKeyTogglesAndCommands(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.mode = modeSection
	m.sectionView = "chorus"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	mm := next.(Model)
	id := mm.selectedTrackID()
	if !mm.solos[id] {
		t.Errorf("expected solos[%s] = true", id)
	}
	if cmd == nil {
		t.Error("expected a tea.Cmd carrying the solo command")
	}
}

func TestUpdate_LoopBracketsSetBounds(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.position = protocol.Event{Bar: 3}

	// "[" sets loop-in at current bar.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	mm := next.(Model)
	if mm.loopFromBar != 3 {
		t.Errorf("after '[': loopFromBar = %d, want 3", mm.loopFromBar)
	}
	if mm.loopToBar <= mm.loopFromBar {
		t.Errorf("after '[': loopToBar (%d) should be > from (%d)", mm.loopToBar, mm.loopFromBar)
	}

	// Advance playhead and set loop-out.
	mm.position = protocol.Event{Bar: 6}
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	mm = next.(Model)
	if mm.loopToBar != 7 {
		t.Errorf("after ']': loopToBar = %d, want 7 (bar+1, exclusive)", mm.loopToBar)
	}
}

func TestUpdate_LoopToggleEnablesAndDisables(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.position = protocol.Event{Bar: 1}

	// 'L' with no prior bounds should establish a default loop and enable.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	mm := next.(Model)
	if !mm.loopEnabled {
		t.Error("first L press: want loopEnabled=true")
	}
	if mm.loopToBar <= mm.loopFromBar {
		t.Errorf("first L press: bounds invalid (from=%d, to=%d)", mm.loopFromBar, mm.loopToBar)
	}
	if cmd == nil {
		t.Error("expected a tea.Cmd carrying CmdLoop")
	}

	// Second press disables.
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	mm = next.(Model)
	if mm.loopEnabled {
		t.Error("second L press: want loopEnabled=false")
	}
}

func TestView_ShowsLoopBadge(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.loopFromBar = 3
	m.loopToBar = 7
	m.loopEnabled = true

	out := m.View()
	// Range should display as 3..6 (inclusive form, to-1).
	if !strings.Contains(out, "3..6") {
		t.Errorf("expected loop range '3..6' in view, got:\n%s", out)
	}
	if !strings.Contains(out, "⟲") {
		t.Error("expected loop glyph ⟲ when enabled")
	}
}

func TestView_HighlightsSelectedTrack(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.song = loadDemo(t)
	m.mode = modeSection
	m.sectionView = "chorus"

	// Selected-row arrow indicator should appear in the output.
	if !strings.Contains(m.View(), "▶") {
		t.Error("expected selected-track arrow ▶ in view")
	}
	// Column header for the mute/solo indicators.
	if !strings.Contains(m.View(), "MS") {
		t.Error("expected 'MS' column header in view")
	}
}

func TestUpdate_EvNoteRecordsFlash(t *testing.T) {
	m := New(nil, "")
	updated, _ := m.Update(engineEventMsg(protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.NoteOn,
		Track:    "bass",
		Note:     36,
		Vel:      100,
	}))
	mm := updated.(Model)
	if mm.flash["bass"].IsZero() {
		t.Error("NoteOn should set flash timestamp")
	}

	// Sample trigger also flashes.
	updated2, _ := mm.Update(engineEventMsg(protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.SampleTrigger,
		Track:    "k",
	}))
	mm2 := updated2.(Model)
	if mm2.flash["k"].IsZero() {
		t.Error("SampleTrigger should set flash timestamp")
	}

	// NoteOff and AllOff should not flash.
	updated3, _ := mm2.Update(engineEventMsg(protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.NoteOff,
		Track:    "new",
	}))
	mm3 := updated3.(Model)
	if _, has := mm3.flash["new"]; has {
		t.Error("NoteOff should not record flash")
	}
}
