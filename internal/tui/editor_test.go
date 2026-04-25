package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/song"
)

// seedEditorModel builds a TUI model with a live on-disk test song loaded
// and the editor opened on section "a" (which binds two tracks, "k" and
// "bass", to matching per-track patterns). Returns the model and the
// song's root dir so tests can read files back.
func seedEditorModel(t *testing.T) (Model, string) {
	t.Helper()
	dir := writeTwoTrackSection(t, "", "")
	songPath := filepath.Join(dir, "song.toml")
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m = m.enterEditor()
	if m.mode != modeEdit {
		t.Fatal("failed to enter editor")
	}
	return m, dir
}

// writeTwoTrackSection writes a minimal project with two sample tracks (k,
// bass), two single-row 16-cell patterns (one per track), and a single
// section "a" that binds them together. Empty bodies default to all-rest
// rows. Returns the project dir.
func writeTwoTrackSection(t *testing.T, kBody, bassBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	sampleDir := filepath.Join(dir, "samples")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "k.wav"), []byte("RIFFdummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "bass.wav"), []byte("RIFFdummy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if kBody == "" {
		kBody = "bars 1\nresolution 4\n\n. . . . . . . . . . . . . . . .\n"
	}
	if bassBody == "" {
		bassBody = "bars 1\nresolution 4\n\n. . . . . . . . . . . . . . . .\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns/k.pat"), []byte(kBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns/bass.pat"), []byte(bassBody), 0o644); err != nil {
		t.Fatal(err)
	}

	tomlBody := `[project]
bpm = 120

[[samples]]
id = "k_smp"
path = "samples/k.wav"

[[samples]]
id = "bass_smp"
path = "samples/bass.wav"

[[tracks]]
id = "k"
sample = "k_smp"
note = 36

[[tracks]]
id = "bass"
sample = "bass_smp"
note = 48

[[sections]]
name = "a"
bars = 1
parts = { k = "k", bass = "bass" }

[song]
arrangement = ["a"]
`
	if err := os.WriteFile(filepath.Join(dir, "song.toml"), []byte(tomlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sendKey(m Model, s string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(Model)
}

func sendType(m Model, t tea.KeyType) Model {
	next, _ := m.Update(tea.KeyMsg{Type: t})
	return next.(Model)
}

func TestEditor_EnterOnCurrentSection(t *testing.T) {
	m, _ := seedEditorModel(t)
	if m.editor.sectionName != "a" {
		t.Errorf("section = %q, want a", m.editor.sectionName)
	}
	if len(m.editor.rows) != 2 {
		t.Errorf("rows = %d, want 2", len(m.editor.rows))
	}
}

// The main screen's track selection becomes the editor's initial cursor row.
func TestEditor_EnterHonorsMainScreenSelection(t *testing.T) {
	dir := writeTwoTrackSection(t, "", "")
	songPath := filepath.Join(dir, "song.toml")
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.selectedTrackIdx = 1 // main-screen selection on "bass"

	m = m.enterEditor()
	if m.editor.trackIdx != 1 {
		t.Errorf("editor.trackIdx = %d, want 1 (carried from main-screen selection)", m.editor.trackIdx)
	}
	if id := m.editor.rows[m.editor.trackIdx].trackID; id != "bass" {
		t.Errorf("selected row trackID = %q, want %q", id, "bass")
	}
}

// A track declared in song.toml but absent from the section's parts is
// not shown as an editable row. The cursor lands on the first available row.
func TestEditor_SkipsTracksNotInSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	sampleDir := filepath.Join(dir, "samples")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "k.wav"), []byte("RIFFdummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "bass.wav"), []byte("RIFFdummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pattern only has "k" — "bass" is declared in toml but absent from section.
	patK := "bars 1\nresolution 4\n\n. . . . . . . . . . . . . . . .\n"
	if err := os.WriteFile(filepath.Join(dir, "patterns/k.pat"), []byte(patK), 0o644); err != nil {
		t.Fatal(err)
	}

	tomlBody := `[project]
bpm = 120

[[samples]]
id = "k_smp"
path = "samples/k.wav"

[[samples]]
id = "bass_smp"
path = "samples/bass.wav"

[[tracks]]
id = "k"
sample = "k_smp"
note = 36

[[tracks]]
id = "bass"
sample = "bass_smp"
note = 48

[[sections]]
name = "a"
bars = 1
parts = { k = "k" }

[song]
arrangement = ["a"]
`
	songPath := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(songPath, []byte(tomlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.selectedTrackIdx = 1 // "bass" — declared but not in the section

	m = m.enterEditor()
	if got := len(m.editor.rows); got != 1 {
		t.Fatalf("want 1 editable row (just k — bass isn't in the section), got %d", got)
	}
	if id := m.editor.rows[m.editor.trackIdx].trackID; id != "k" {
		t.Errorf("cursor should land on k (the only row), got %q", id)
	}
}

func TestEditor_CursorMovement(t *testing.T) {
	m, _ := seedEditorModel(t)

	m = sendType(m, tea.KeyRight)
	m = sendType(m, tea.KeyRight)
	m = sendType(m, tea.KeyRight)
	if m.editor.cellIdx != 3 {
		t.Errorf("after 3×right: cell=%d want 3", m.editor.cellIdx)
	}
	m = sendType(m, tea.KeyLeft)
	if m.editor.cellIdx != 2 {
		t.Errorf("after left: cell=%d want 2", m.editor.cellIdx)
	}
	m = sendType(m, tea.KeyDown)
	if m.editor.trackIdx != 1 {
		t.Errorf("after down: track=%d want 1", m.editor.trackIdx)
	}
	m = sendType(m, tea.KeyUp)
	if m.editor.trackIdx != 0 {
		t.Errorf("after up: track=%d want 0", m.editor.trackIdx)
	}
	m = sendType(m, tea.KeyEnd)
	if want := m.editor.totalCells() - 1; m.editor.cellIdx != want {
		t.Errorf("after end: cell=%d want %d", m.editor.cellIdx, want)
	}
	m = sendType(m, tea.KeyHome)
	if m.editor.cellIdx != 0 {
		t.Errorf("after home: cell=%d want 0", m.editor.cellIdx)
	}
}

func TestEditor_PageJumpsByBar(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendType(m, tea.KeyPgDown)
	if m.editor.cellIdx == 0 {
		t.Error("pgdown did not advance")
	}
	m = sendType(m, tea.KeyPgUp)
	if m.editor.cellIdx != 0 {
		t.Errorf("pgup should return to 0, got %d", m.editor.cellIdx)
	}
}

func TestEditor_CursorClampsAtBounds(t *testing.T) {
	m, _ := seedEditorModel(t)
	for i := 0; i < 5; i++ {
		m = sendType(m, tea.KeyLeft)
	}
	if m.editor.cellIdx != 0 {
		t.Errorf("left at 0: cell=%d want 0", m.editor.cellIdx)
	}
	for i := 0; i < 5; i++ {
		m = sendType(m, tea.KeyUp)
	}
	if m.editor.trackIdx != 0 {
		t.Errorf("up at 0: track=%d want 0", m.editor.trackIdx)
	}
	for i := 0; i < 100; i++ {
		m = sendType(m, tea.KeyRight)
	}
	if want := m.editor.totalCells() - 1; m.editor.cellIdx != want {
		t.Errorf("right past end: cell=%d want %d", m.editor.cellIdx, want)
	}
}

func TestEditor_NoteEntry(t *testing.T) {
	m, _ := seedEditorModel(t)
	// Move cursor to the bass track.
	m = sendType(m, tea.KeyDown)
	// Tracker keymap: 'z' at octave 4 → C4 → MIDI 60.
	m = sendKey(m, "z")
	if !m.editor.dirty() {
		t.Error("dirty flag not set after edit")
	}
	got := m.editor.rows[1].cells[0]
	if got.Kind != song.CellNote || got.Note != 60 {
		t.Errorf("bass[0] = %+v want note C4 (60)", got)
	}
	// Bump octave to 5, then 'c' at octave 5 → E5 → MIDI 76 at the next cell.
	m = sendKey(m, "+")
	m = sendType(m, tea.KeyRight)
	m = sendKey(m, "c")
	got = m.editor.rows[1].cells[1]
	if got.Kind != song.CellNote || got.Note != 76 {
		t.Errorf("bass[1] after oct+ = %+v want E5 (76)", got)
	}
}

func TestEditor_RestAndTieEntry(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendKey(m, "z") // cell 0: note C4
	m = sendType(m, tea.KeyRight)
	m = sendKey(m, "-") // cell 1: tie
	m = sendType(m, tea.KeyRight)
	m = sendKey(m, ".") // cell 2: rest

	ks := m.editor.rows[0].cells
	if ks[0].Kind != song.CellNote {
		t.Errorf("cell 0: %+v", ks[0])
	}
	if ks[1].Kind != song.CellTie {
		t.Errorf("cell 1: %+v", ks[1])
	}
	if ks[2].Kind != song.CellRest {
		t.Errorf("cell 2: %+v", ks[2])
	}
}

func TestEditor_SampleTrigger(t *testing.T) {
	m, _ := seedEditorModel(t)
	// Uppercase X is bare sample trigger (lowercase x now maps to D in the
	// tracker keymap as a pitched note).
	m = sendKey(m, "X")
	got := m.editor.rows[0].cells[0]
	if got.Kind != song.CellSample || got.Vel != 100 {
		t.Errorf("cell after X: %+v", got)
	}
}

func TestEditor_SavePersistsAndClearsDirty(t *testing.T) {
	m, dir := seedEditorModel(t)
	m = sendKey(m, "X")
	if !m.editor.dirty() {
		t.Fatal("expected dirty after edit")
	}
	m = sendType(m, tea.KeyCtrlS)
	if m.editor.dirty() {
		t.Errorf("dirty still set after save: message=%q", m.editor.message)
	}
	// Only the k row was edited, so only patterns/k.pat should have content;
	// patterns/bass.pat should remain untouched.
	data, _ := os.ReadFile(filepath.Join(dir, "patterns/k.pat"))
	if !strings.Contains(string(data), "X") {
		t.Errorf("saved k.pat missing X trigger:\n%s", data)
	}
	if _, err := song.Load(filepath.Join(dir, "song.toml")); err != nil {
		t.Errorf("song no longer loads after save: %v", err)
	}
}

func TestEditor_EscOnCleanExits(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendType(m, tea.KeyEsc)
	if m.mode != modeSection {
		t.Error("esc on clean editor should return to main")
	}
}

func TestEditor_EscOnDirtyPromptsConfirm(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendKey(m, "X") // dirty (sample trigger)
	m = sendType(m, tea.KeyEsc)
	if m.mode != modeEdit {
		t.Error("esc on dirty should stay in editor for confirmation")
	}
	if !m.editor.exitConfirm {
		t.Error("exitConfirm should be true")
	}
	// 'n' discards and exits.
	m = sendKey(m, "n")
	if m.mode != modeSection {
		t.Error("n on confirm should exit")
	}
}

func TestEditor_EscConfirmCancel(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendKey(m, "X")
	m = sendType(m, tea.KeyEsc)
	m = sendKey(m, "c") // cancel
	if m.mode != modeEdit || m.editor.exitConfirm {
		t.Error("c should cancel the exit confirmation")
	}
}

func TestEditor_EscConfirmSaveAndExit(t *testing.T) {
	m, dir := seedEditorModel(t)
	m = sendKey(m, "X")
	m = sendType(m, tea.KeyEsc)
	m = sendKey(m, "y") // save + exit
	if m.mode != modeSection {
		t.Error("y should exit after saving")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "patterns/k.pat"))
	if !strings.Contains(string(data), "X") {
		t.Errorf("expected X saved to k.pat:\n%s", data)
	}
}

func TestEditor_RendersCursorAndGrid(t *testing.T) {
	m, _ := seedEditorModel(t)
	m = sendKey(m, "z")
	view := m.View()
	for _, want := range []string{"edit: a", "C4", "bar 1", "DIRTY"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in view", want)
		}
	}
}
