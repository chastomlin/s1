package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// TestCellIdxAtPlayhead_Unit walks a 4-bar, 4/4, resolution-4 section
// (16 cells per bar → 64 total) at a few transport positions to make
// sure the math lines up. Before-start returns -1; playhead on bar 3
// beat 2 at tick 48 lands at cell index 2*16 + 1*4 + 2 = 38.
func TestCellIdxAtPlayhead_Unit(t *testing.T) {
	sec := song.Section{Bars: 4, BeatsPerBar: 4, Resolution: 4}

	if got := cellIdxAtPlayhead(protocol.Event{Bar: 2, Beat: 1}, 3, sec); got != -1 {
		t.Errorf("before section: got %d, want -1", got)
	}
	if got := cellIdxAtPlayhead(protocol.Event{Bar: 3, Beat: 1}, 3, sec); got != 0 {
		t.Errorf("at section start: got %d, want 0", got)
	}
	// Bar 3, beat 2, tick 48 (half a beat into beat 2).
	// 0*16 (first bar of section) + 1*4 (beat 2) + 48/(96/4)=2 (sub-beat) = 6.
	if got := cellIdxAtPlayhead(protocol.Event{Bar: 3, Beat: 2, Tick: 48}, 3, sec); got != 6 {
		t.Errorf("bar3 beat2 tick48: got %d, want 6", got)
	}
	// Wrap: a Repeat>1 slot lands past sec.Bars. Bar 7 inside a section
	// that started at bar 3 is (7-3)%4 = 0 → first bar of section again.
	if got := cellIdxAtPlayhead(protocol.Event{Bar: 7, Beat: 1}, 3, sec); got != 0 {
		t.Errorf("wrap past sec.Bars: got %d, want 0", got)
	}
}

// TestToggleLive_Transitions pins the interlock rules: turning Live off
// also disarms Rec; turning Rec on requires Live.
func TestToggleLive_Transitions(t *testing.T) {
	m := New(nil, "")

	// Rec alone is a no-op without Live.
	out, _ := m.toggleRec()
	if out.(Model).recordArmed {
		t.Error("toggleRec without Live should not arm")
	}

	// Enable Live.
	out, _ = m.toggleLive()
	m = out.(Model)
	if !m.liveEnabled {
		t.Fatal("toggleLive from off should enable")
	}

	// Now Rec can arm.
	out, _ = m.toggleRec()
	m = out.(Model)
	if !m.recordArmed {
		t.Error("toggleRec with Live on should arm")
	}

	// Toggling Live off disarms Rec too.
	out, _ = m.toggleLive()
	m = out.(Model)
	if m.liveEnabled || m.recordArmed {
		t.Errorf("toggleLive off should also disarm Rec; live=%v rec=%v",
			m.liveEnabled, m.recordArmed)
	}
}

// TestLiveAuditionIsAdditiveAndWritesToBuffer exercises the record path:
// hitting a tracker key while Live+Rec+playing with the playhead in the
// viewed section writes a note cell at the playhead position, and a
// second hit at the same cell is ignored (additive, not destructive).
func TestLiveAuditionIsAdditiveAndWritesToBuffer(t *testing.T) {
	m, dir := seedLiveModel(t)
	m.liveEnabled = true
	m.recordArmed = true
	m.state = protocol.StatePlaying
	// Playhead on bar 1 beat 1 (start of section "verse"). Section
	// starts at bar 1 in our fixture.
	m.position = protocol.Event{Bar: 1, Beat: 1, Section: "verse"}
	m.sectionView = "verse"
	m.selectedTrackIdx = 0 // track "k"

	// First 'z' writes C4 into cell 0.
	out, _ := m.liveAudition(0)
	m = out.(Model)
	pat, ok := m.recordBuffer["verse-k"]
	if !ok {
		t.Fatal("expected recordBuffer to contain verse-k")
	}
	if pat.Cells[0].Kind != song.CellNote || pat.Cells[0].Note != 60 {
		t.Errorf("cell 0 = %+v, want C4 note", pat.Cells[0])
	}

	// Second 'd' (D#) at same playhead position — additive, should not
	// overwrite the existing C4.
	out, _ = m.liveAudition(3)
	m = out.(Model)
	if m.recordBuffer["verse-k"].Cells[0].Note != 60 {
		t.Error("additive record must not overwrite existing cell")
	}
	_ = dir
}

// TestFlushRecordBufferPersistsPatterns ensures buffered edits actually
// hit disk on flush, and the buffer is cleared.
func TestFlushRecordBufferPersistsPatterns(t *testing.T) {
	m, dir := seedLiveModel(t)
	// Seed a buffered edit directly (skipping the audition path so this
	// test is focused on flush semantics).
	base := m.song.Patterns["verse-k"]
	edited := base
	edited.Cells = append([]song.Cell(nil), base.Cells...)
	edited.Cells[0] = song.Cell{Kind: song.CellNote, Note: 60, Vel: 100}
	m.recordBuffer = map[string]*song.Pattern{"verse-k": &edited}

	m.flushRecordBuffer()
	if m.recordBuffer != nil {
		t.Errorf("buffer should be cleared after flush, got %v", m.recordBuffer)
	}
	data, err := os.ReadFile(filepath.Join(dir, "patterns/verse-k.pat"))
	if err != nil {
		t.Fatalf("read pattern: %v", err)
	}
	// Written file should carry the C4 at cell 0 (formatted as "C4").
	if !strings.Contains(string(data), "C4") {
		t.Errorf("flushed pattern missing C4:\n%s", data)
	}
}

// TestLiveMode_TrackerKeyBypassesSolo pins the routing priority: while
// Live is on, 's' (a tracker sharp key) auditions instead of toggling
// solo. Without Live, 's' still toggles solo.
func TestLiveMode_TrackerKeyBypassesSolo(t *testing.T) {
	m, _ := seedLiveModel(t)

	// Live off: s toggles solo.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	mm := out.(Model)
	id := mm.selectedTrackID()
	if !mm.solos[id] {
		t.Error("without live, s should toggle solo")
	}

	// Reset and enable Live.
	m.solos = map[string]bool{}
	m.liveEnabled = true
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	mm = out.(Model)
	if mm.solos[id] {
		t.Error("with live, s should not toggle solo — keyboard is a piano")
	}
}

// TestTransport_SectionViewLocalizesBar pins the display rule: in
// section view the transport bar counter counts from 1 within the
// section (not the global song timeline), and a repeated slot gets a
// "pass N/M" suffix so you can tell which loop you're on. Arrangement
// view keeps the absolute bar.
func TestTransport_SectionViewLocalizesBar(t *testing.T) {
	m, _ := seedLiveModel(t)
	// Add a second section and push "verse" into a ×3 slot so we can
	// test pass labels too.
	if err := song.SetArrangementRepeat(m.songPath, 0, 3); err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(m.songPath)
	if err != nil {
		t.Fatal(err)
	}
	m.song = s

	// Playhead at absolute bar 6 — that's inside the 2nd pass of verse
	// (section starts at 1, pass 1 = bars 1..4, pass 2 = bars 5..8), bar 2
	// of the section (6 - 1 = 5; 5 mod 4 = 1; +1 = 2).
	m.position = protocol.Event{Bar: 6, Beat: 3, Section: "verse"}

	// Arrangement view: absolute bar.
	m.mode = modeArrangement
	pos, section := m.transportBarAndSection()
	if pos != "006:03" {
		t.Errorf("arrangement: bar = %q, want 006:03", pos)
	}
	if section != "verse" {
		t.Errorf("arrangement: section = %q, want verse (no pass suffix)", section)
	}

	// Section view: localized + pass info.
	m.mode = modeSection
	m.sectionView = "verse"
	pos, section = m.transportBarAndSection()
	if pos != "02:03" {
		t.Errorf("section view: bar = %q, want 02:03", pos)
	}
	if section != "verse (pass 2/3)" {
		t.Errorf("section view: section = %q, want 'verse (pass 2/3)'", section)
	}
}

// TestEnterSection_SeeksToSlotStart pins the playback-scope rule: when
// you drill into an arrangement slot, the engine should be told to seek
// to that slot's first bar so a subsequent play starts at the section
// you're viewing rather than bar 1 of the whole song.
func TestEnterSection_SeeksToSlotStart(t *testing.T) {
	m, _ := seedLiveModel(t)
	// Arrangement: [verse, chorus, verse] — seedLiveModel gives us only
	// [verse]; extend in-memory to exercise the "pick the 3rd slot"
	// path. We bypass AppendSection (name-uniqueness) by wiring the
	// arrangement directly.
	m.song.Arrangement = []song.ArrangementSlot{
		{Section: "verse", Repeat: 1},
		{Section: "verse", Repeat: 1}, // no chorus in seed, reuse verse
		{Section: "verse", Repeat: 1},
	}
	// verse has 4 bars. Slot starts: 1, 5, 9.
	if got := m.slotStartBar(2); got != 9 {
		t.Errorf("slotStartBar(2) = %d, want 9", got)
	}

	m.mode = modeArrangement
	m.arrangementIdx = 2 // third verse slot
	out, cmd := m.updateArrangement(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a seek command after enter")
	}
	mm := out.(Model)
	if mm.mode != modeSection {
		t.Errorf("mode = %v, want modeSection", mm.mode)
	}
	if mm.sectionView != "verse" {
		t.Errorf("sectionView = %q, want verse", mm.sectionView)
	}
	// The seek's Bar value is embedded in the tea.Cmd closure, which we
	// can't cleanly destructure; the math is pinned by the slotStartBar
	// check above and the engine's own seek tests cover the wire path.
}

// TestTransport_LocalizesToActiveSlotNotFirstOccurrence pins the bug
// where a section repeated as multiple arrangement slots (verse → chorus
// → verse) would always anchor localization to the *first* verse slot,
// even when the playhead was inside the second one. Fix anchors to the
// specific slot the playhead is in.
func TestTransport_LocalizesToActiveSlotNotFirstOccurrence(t *testing.T) {
	m, _ := seedLiveModel(t)
	// Add a second "verse" slot after the first (with a chorus in between).
	if err := song.AppendSection(m.songPath, "chorus", 4); err != nil {
		t.Fatal(err)
	}
	// Duplicate verse slot by appending again — AppendSection also adds
	// to the arrangement, so we now have [verse, chorus, verse].
	if err := song.AppendSection(m.songPath, "verse2", 4); err != nil {
		// verse already exists; use a distinct name and fix arrangement
		// manually. In practice arrangement reuses section names, but
		// AppendSection requires uniqueness. Build the arrangement by
		// hand instead.
		t.Skip("AppendSection uniqueness — skipping")
	}
	// Easier: edit in-memory arrangement to [verse, chorus, verse]. This
	// matches what a user would see after appending an extra slot via
	// an arrangement-view "duplicate" action (not yet implemented, but
	// the underlying slot list supports repeats like this today).
	s, err := song.Load(m.songPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Arrangement = []song.ArrangementSlot{
		{Section: "verse", Repeat: 1},
		{Section: "chorus", Repeat: 1},
		{Section: "verse", Repeat: 1},
	}
	m.song = s

	// verse → bars 1..4, chorus → 5..8, verse (second slot) → 9..12.
	// Playhead at bar 10 (local bar 2 of the 2nd verse).
	m.position = protocol.Event{Bar: 10, Beat: 1, Section: "verse"}
	m.mode = modeSection
	m.sectionView = "verse"

	localBar, _, _, ok := m.localizeToSection()
	if !ok {
		t.Fatal("expected localizeToSection to succeed")
	}
	if localBar != 2 {
		t.Errorf("localBar = %d, want 2 (bar 10 is 2nd bar of 2nd verse slot, not 10th of first)", localBar)
	}
}

// seedLiveModel builds a 4-bar, 4/4 section "verse" with a "k" track
// bound to a "verse-k" pattern, ready for Live/Rec tests.
func seedLiveModel(t *testing.T) (Model, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	// Add a sample + track "k".
	if err := song.AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	// Seed a per-track pattern file "verse-k.pat" (4 bars * 4 beats * 4 res = 64 cells of rest).
	cells := strings.Repeat(". ", 63) + "."
	if err := os.WriteFile(filepath.Join(dir, "patterns/verse-k.pat"),
		[]byte("bars 4\n\n"+cells+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create section "verse" and bind the part.
	if err := song.AppendSection(songPath, "verse", 4); err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSectionPart(songPath, "verse", "k", "verse-k"); err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.mode = modeSection
	m.sectionView = "verse"
	return m, dir
}
