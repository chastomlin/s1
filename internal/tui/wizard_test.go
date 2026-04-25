package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/song"
)

// seedModel builds a Model with a freshly-loaded test song and whatever
// initial promptState is supplied. Keeps the wizard tests terse.
func seedModel(t *testing.T, songTOML string, prompt promptState) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns/a.pat"), []byte("bars 1\nresolution 1\n\n. . . .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Tests that don't declare sections themselves still arrange ["a"] — inject
	// a trivial section block so the loader is happy.
	if !strings.Contains(songTOML, "[[sections]]") {
		if idx := strings.Index(songTOML, "[song]"); idx >= 0 {
			songTOML = songTOML[:idx] + "[[sections]]\nname = \"a\"\nbars = 1\nparts = {}\n\n" + songTOML[idx:]
		}
	}
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(songTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := song.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := New(nil, path)
	m.width, m.height = 140, 30
	m.song = s
	m.prompt = prompt
	return m, path
}

// typeString feeds literal rune events through Update. Does not send Enter.
func typeString(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	return m
}

func pressEnter(m Model) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model)
}

func pressKey(m Model, r rune) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(Model)
}

const minimalSong = `[project]
bpm = 120

[song]
arrangement = ["a"]
`

func TestWizard_AddSampleTrack_HappyPath(t *testing.T) {
	m, path := seedModel(t, minimalSong, promptState{kind: promptAddTrackKind})
	// Kind chooser: press 's' → sample wizard.
	m = pressKey(m, 's')
	if m.prompt.kind != promptAddSampleTrack {
		t.Fatalf("after s: kind=%v", m.prompt.kind)
	}

	// Step 0: track id
	m = typeString(m, "k")
	m = pressEnter(m)
	if m.prompt.step != 1 {
		t.Fatalf("after id: step=%d (msg=%q)", m.prompt.step, m.prompt.message)
	}

	// Step 1: sample path
	m = typeString(m, "samples/kick.wav")
	m = pressEnter(m)
	if m.prompt.step != 2 {
		t.Fatalf("after path: step=%d", m.prompt.step)
	}

	// Step 2: note
	m = typeString(m, "36")
	m = pressEnter(m)
	if m.prompt.active() {
		t.Fatalf("wizard did not close: %+v", m.prompt)
	}

	// Verify song.toml now contains the new track and parses.
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `[[tracks]]`) {
		t.Errorf("tracks block not added:\n%s", got)
	}
	s, err := song.Load(path)
	if err != nil {
		t.Fatalf("Load after wizard: %v", err)
	}
	trk, ok := s.Tracks["k"]
	if !ok {
		t.Fatal("track k missing")
	}
	if trk.Sample != "k" || trk.Channel != 10 || trk.Note != 36 {
		t.Errorf("track = %+v", trk)
	}
}

func TestWizard_AddSampleTrack_RejectsDuplicateID(t *testing.T) {
	// Song already has a track "k" from prior edits.
	m, _ := seedModel(t, `[project]
bpm = 120

[[samples]]
id = "k"
path = "samples/k.wav"

[[tracks]]
id = "k"
sample = "k"
channel = 10
note = 36

[song]
arrangement = ["a"]
`, promptState{kind: promptAddSampleTrack})

	m = typeString(m, "k")
	m = pressEnter(m)
	if m.prompt.step != 0 {
		t.Errorf("duplicate id should not advance: step=%d", m.prompt.step)
	}
	if m.prompt.message == "" {
		t.Error("expected error message for duplicate id")
	}
}

func TestWizard_AddInstrumentTrack_HappyPath(t *testing.T) {
	m, path := seedModel(t, minimalSong, promptState{kind: promptAddInstrTrack})

	m = typeString(m, "bass")
	m = pressEnter(m)
	m = typeString(m, "1")
	m = pressEnter(m)
	m = typeString(m, "33")
	m = pressEnter(m)

	if m.prompt.active() {
		t.Fatalf("wizard did not close: %+v", m.prompt)
	}
	s, err := song.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if trk, ok := s.Tracks["bass"]; !ok || trk.Instrument != "bass" || trk.Channel != 1 {
		t.Errorf("track = %+v ok=%v", trk, ok)
	}
	if inst, ok := s.Instruments["bass"]; !ok || inst.Program != 33 {
		t.Errorf("instrument = %+v ok=%v", inst, ok)
	}
}

func TestWizard_InvalidInputs(t *testing.T) {
	cases := []struct {
		name    string
		kind    promptKind
		inputs  []string
		wantMsg string
	}{
		{"bad channel", promptAddInstrTrack, []string{"bass", "99"}, "channel must be 1..16"},
		{"bad note", promptAddSampleTrack, []string{"k", "s.wav", "200"}, "note must be 0..127"},
		{"bad program", promptAddInstrTrack, []string{"bass", "1", "500"}, "program must be 0..127"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := seedModel(t, minimalSong, promptState{kind: c.kind})
			for _, in := range c.inputs {
				m = typeString(m, in)
				m = pressEnter(m)
			}
			if !strings.Contains(m.prompt.message, c.wantMsg) {
				t.Errorf("message = %q, want substring %q", m.prompt.message, c.wantMsg)
			}
			if !m.prompt.active() {
				t.Error("prompt closed despite validation failure")
			}
		})
	}
}

func TestWizard_TempoUpdatesSongToml(t *testing.T) {
	m, path := seedModel(t, minimalSong, promptState{kind: promptTempo})
	m = typeString(m, "140")
	m = pressEnter(m)
	if m.prompt.active() {
		t.Fatalf("tempo prompt did not close: %+v", m.prompt)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "bpm = 140") {
		t.Errorf("expected bpm = 140 in file:\n%s", got)
	}
}

func TestWizard_TempoRejectsNonNumeric(t *testing.T) {
	m, _ := seedModel(t, minimalSong, promptState{kind: promptTempo})
	m = typeString(m, "fast")
	m = pressEnter(m)
	if m.prompt.message == "" {
		t.Error("expected error for non-numeric tempo")
	}
	if !m.prompt.active() {
		t.Error("prompt should stay open for retry")
	}
}

func TestWizard_EscCancelsAtAnyStep(t *testing.T) {
	m, _ := seedModel(t, minimalSong, promptState{kind: promptAddSampleTrack})
	m = typeString(m, "k")
	m = pressEnter(m) // advance to step 1
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.prompt.active() {
		t.Error("Esc should close the prompt")
	}
}
