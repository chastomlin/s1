package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/song"
)

// TestWizard_NewSong_CreatesLoadableSong drives the full 4-step new-song
// wizard (path → title → bpm → time_signature) and verifies the resulting
// song.toml parses and carries the entered values.
func TestWizard_NewSong_CreatesLoadableSong(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "mysong")

	m := New(nil, "")
	m.width, m.height = 140, 30
	m.prompt = promptState{kind: promptNewSongPath}

	m = typeString(m, dir)
	m = pressEnter(m)
	m = typeString(m, "My Song")
	m = pressEnter(m)
	m = typeString(m, "140")
	m = pressEnter(m)
	m = typeString(m, "3/4")
	m = pressEnter(m)

	if m.prompt.active() {
		t.Fatalf("wizard did not close: %+v", m.prompt)
	}
	if m.songPath != filepath.Join(dir, "song.toml") {
		t.Errorf("songPath = %q, want %s/song.toml", m.songPath, dir)
	}
	s, err := song.Load(m.songPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Project.BPM != 140 {
		t.Errorf("bpm = %d, want 140", s.Project.BPM)
	}
	if s.Project.Title != "My Song" {
		t.Errorf("title = %q, want My Song", s.Project.Title)
	}
	if s.Project.TimeSignature != "3/4" {
		t.Errorf("time_signature = %q, want 3/4", s.Project.TimeSignature)
	}
}

// TestWizard_NewSong_RejectsBadTempo tests the per-step validation.
func TestWizard_NewSong_RejectsBadTempo(t *testing.T) {
	m := New(nil, "")
	m.width, m.height = 140, 30
	m.prompt = promptState{kind: promptNewSongPath}

	m = typeString(m, filepath.Join(t.TempDir(), "a"))
	m = pressEnter(m)
	m = typeString(m, "title")
	m = pressEnter(m)
	// Step 2 is tempo. Enter a nonsense value.
	m = typeString(m, "fast")
	m = pressEnter(m)
	if m.prompt.step != 2 {
		t.Errorf("bad tempo should not advance: step=%d (msg=%q)", m.prompt.step, m.prompt.message)
	}
	if !strings.Contains(m.prompt.message, "20..400") {
		t.Errorf("expected tempo range error, got %q", m.prompt.message)
	}
}

// TestWizard_NewSection_AppendsBlockAndSlot runs the 2-step new-section
// wizard against an already-loaded song and verifies both the library
// entry and the arrangement slot land in the file.
func TestWizard_NewSection_AppendsBlockAndSlot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(songPath)
	if err != nil {
		t.Fatal(err)
	}
	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.prompt = promptState{kind: promptNewSectionName}

	m = typeString(m, "verse")
	m = pressEnter(m)
	m = typeString(m, "4")
	m = pressEnter(m)

	if m.prompt.active() {
		t.Fatalf("wizard did not close: %+v", m.prompt)
	}
	s2, err := song.Load(songPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s2.Sections["verse"]; !ok {
		t.Error("section 'verse' not added")
	}
	if len(s2.Arrangement) != 1 || s2.Arrangement[0].Section != "verse" {
		t.Errorf("arrangement = %v, want [verse]", s2.Arrangement)
	}
}

// TestWizard_NewSection_RejectsDuplicate validates that the name-step
// blocks sections that already exist.
func TestWizard_NewSection_RejectsDuplicate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSection(songPath, "verse", 4); err != nil {
		t.Fatal(err)
	}
	s, _ := song.Load(songPath)

	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.prompt = promptState{kind: promptNewSectionName}

	m = typeString(m, "verse")
	m = pressEnter(m)
	if m.prompt.step != 0 {
		t.Errorf("duplicate should not advance: step=%d", m.prompt.step)
	}
	if !strings.Contains(m.prompt.message, "already exists") {
		t.Errorf("expected duplicate error, got %q", m.prompt.message)
	}
}

// TestSectionView_DeleteTrackRequiresYConfirm pins the two-step gate
// on `D`: the first press only opens the confirm prompt; the track
// only disappears once the user presses `y`. A dangling keystroke on
// `n` or `esc` leaves the song untouched.
func TestSectionView_DeleteTrackRequiresYConfirm(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	// Per-track pattern for the section binding.
	if err := os.WriteFile(filepath.Join(dir, "patterns", "k.pat"),
		[]byte("bars 1\nresolution 1\n\n. . . .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSection(songPath, "verse", 1); err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSectionPart(songPath, "verse", "k", "k"); err != nil {
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
	m.selectedTrackIdx = 0 // track "k"

	// Press D — should open a confirm prompt, not delete yet.
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = out.(Model)
	if m.prompt.kind != promptConfirmDeleteTrack || m.prompt.value != "k" {
		t.Fatalf("after D: prompt=%+v, want confirm-delete for 'k'", m.prompt)
	}
	// Track should still exist on disk at this point.
	s1, _ := song.Load(songPath)
	if _, ok := s1.Tracks["k"]; !ok {
		t.Error("track deleted before confirm — prompt should gate it")
	}

	// Press y — commits the delete.
	out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = out.(Model)
	if m.prompt.active() {
		t.Errorf("prompt should close after y: %+v", m.prompt)
	}
	s2, err := song.Load(songPath)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if _, ok := s2.Tracks["k"]; ok {
		t.Error("track 'k' should be gone after D + y")
	}
	if _, bound := s2.Sections["verse"].Parts["k"]; bound {
		t.Error("verse.parts should no longer reference k")
	}
}

// TestWizard_SectionScoped_AddSampleTrack drives the sample-track wizard
// while in modeSection and verifies the track, the starter .pat file,
// and the section.parts binding all land.
func TestWizard_SectionScoped_AddSampleTrack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := song.CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := song.AppendSection(songPath, "verse", 4); err != nil {
		t.Fatal(err)
	}
	s, _ := song.Load(songPath)

	m := New(nil, songPath)
	m.width, m.height = 140, 30
	m.song = s
	m.mode = modeSection
	m.sectionView = "verse"
	m.prompt = promptState{kind: promptAddSampleTrack}

	m = typeString(m, "k")
	m = pressEnter(m)
	m = typeString(m, "samples/kick.wav")
	m = pressEnter(m)
	m = typeString(m, "36")
	m = pressEnter(m)

	if m.prompt.active() {
		t.Fatalf("wizard did not close: %+v (msg=%q)", m.prompt, m.prompt.message)
	}

	// The starter pattern file should exist: patterns/verse-k.pat.
	patFile := filepath.Join(dir, "patterns", "verse-k.pat")
	if _, err := os.Stat(patFile); err != nil {
		t.Errorf("expected starter pattern at %s: %v", patFile, err)
	}
	s2, err := song.Load(songPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := s2.Tracks["k"]; !ok {
		t.Error("track 'k' not added to [[tracks]]")
	}
	sec, ok := s2.Sections["verse"]
	if !ok {
		t.Fatal("section 'verse' missing from loaded song")
	}
	if sec.Parts["k"] != "verse-k" {
		t.Errorf("section.parts[k] = %q, want verse-k", sec.Parts["k"])
	}
}
