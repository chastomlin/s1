package song

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseSong is a minimal valid song.toml that leaves room for the
// mutators to make additions. An empty-but-present arrangement makes
// the first AppendSection exercise the empty-list code path, and the
// section "seed" makes RemoveArrangementSlot / MoveArrangementSlot
// tests have something to operate on when they build on top.
func baseSong(t *testing.T, arrangement string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[project]
bpm = 120
time_signature = "4/4"

[[sections]]
name = "seed"
bars = 1
parts = {}

[song]
arrangement = ` + arrangement + `
`
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAppendSection_AddsBlockAndArrangementEntry(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	if err := AppendSection(path, "verse", 4); err != nil {
		t.Fatalf("AppendSection: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `"verse"`) {
		t.Errorf("new section block not added:\n%s", got)
	}
	if !strings.Contains(string(got), `arrangement = ["seed", "verse"]`) {
		t.Errorf("arrangement not appended:\n%s", got)
	}
	// Must still load cleanly.
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after AppendSection: %v", err)
	}
	if _, ok := s.Sections["verse"]; !ok {
		t.Error("section 'verse' not in loaded song")
	}
	if len(s.Arrangement) != 2 || s.Arrangement[1].Section != "verse" {
		t.Errorf("arrangement = %v, want seed, verse", s.Arrangement)
	}
}

func TestAppendSection_IntoEmptyArrangement(t *testing.T) {
	path := baseSong(t, `[]`)
	if err := AppendSection(path, "intro", 2); err != nil {
		t.Fatalf("AppendSection: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Arrangement) != 1 || s.Arrangement[0].Section != "intro" {
		t.Errorf("arrangement = %v, want [intro]", s.Arrangement)
	}
}

func TestAppendSection_RejectsBadInput(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	if err := AppendSection(path, "", 4); err == nil {
		t.Error("expected error for empty name")
	}
	if err := AppendSection(path, "verse", 0); err == nil {
		t.Error("expected error for zero bars")
	}
	if err := AppendSection(path, "bad name", 4); err == nil {
		t.Error("expected error for invalid ident (space)")
	}
}

func TestRemoveArrangementSlot(t *testing.T) {
	path := baseSong(t, `["seed", "seed", "seed"]`)
	if err := RemoveArrangementSlot(path, 1); err != nil {
		t.Fatalf("RemoveArrangementSlot: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Arrangement) != 2 {
		t.Errorf("arrangement len = %d, want 2", len(s.Arrangement))
	}
	// Section library entry is not deleted — the section itself still exists.
	if _, ok := s.Sections["seed"]; !ok {
		t.Error("section 'seed' should still be in the library after removing a slot")
	}
}

func TestRemoveArrangementSlot_OutOfRangeErrors(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	if err := RemoveArrangementSlot(path, 5); err == nil {
		t.Error("expected error for out-of-range index")
	}
}

func TestMoveArrangementSlot(t *testing.T) {
	path := baseSong(t, `["a", "b", "c"]`)
	// Need sections a/b/c for Load to succeed after the move. Declare them.
	body, _ := os.ReadFile(path)
	extra := `
[[sections]]
name = "a"
bars = 1
parts = {}

[[sections]]
name = "b"
bars = 1
parts = {}

[[sections]]
name = "c"
bars = 1
parts = {}
`
	if err := os.WriteFile(path, append([]byte(extra), body...), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveArrangementSlot(path, 0, 2); err != nil {
		t.Fatalf("MoveArrangementSlot: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"b", "c", "a"}
	for i, w := range want {
		if s.Arrangement[i].Section != w {
			t.Errorf("slot %d = %q, want %q", i, s.Arrangement[i].Section, w)
		}
	}
}

func TestSetArrangementRepeat(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	if err := SetArrangementRepeat(path, 0, 4); err != nil {
		t.Fatalf("SetArrangementRepeat: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `"seed*4"`) {
		t.Errorf("expected seed*4 in file:\n%s", got)
	}
	// Bumping back down to 1 strips the *N suffix entirely.
	if err := SetArrangementRepeat(path, 0, 1); err != nil {
		t.Fatalf("SetArrangementRepeat 1: %v", err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), `*1`) || strings.Contains(string(got), `*4`) {
		t.Errorf("expected bare name after reset:\n%s", got)
	}
}

func TestAppendSectionPart_InsertsAndOverwrites(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	// Need a pattern "k" to exist before AppendSectionPart is called —
	// Load() validates part references.
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	patDir := filepath.Join(filepath.Dir(path), "patterns")
	if err := os.WriteFile(filepath.Join(patDir, "k.pat"), []byte(patBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(patDir, "k2.pat"), []byte(patBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a track "k" to song.toml so AppendSectionPart's validation (via
	// Load after the fact) succeeds.
	body, _ := os.ReadFile(path)
	body = append([]byte(`[[samples]]
id = "k"
path = "samples/k.wav"

[[tracks]]
id = "k"
sample = "k"
channel = 10
note = 36

`), body...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AppendSectionPart(path, "seed", "k", "k"); err != nil {
		t.Fatalf("AppendSectionPart: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after first append: %v", err)
	}
	if got := s.Sections["seed"].Parts["k"]; got != "k" {
		t.Errorf("parts.k = %q, want k", got)
	}

	// Overwrite with a different pattern.
	if err := AppendSectionPart(path, "seed", "k", "k2"); err != nil {
		t.Fatalf("AppendSectionPart overwrite: %v", err)
	}
	s, err = Load(path)
	if err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
	if got := s.Sections["seed"].Parts["k"]; got != "k2" {
		t.Errorf("parts.k (overwritten) = %q, want k2", got)
	}
}

func TestAppendSectionPart_SectionNotFound(t *testing.T) {
	path := baseSong(t, `["seed"]`)
	if err := AppendSectionPart(path, "ghost", "k", "k"); err == nil {
		t.Error("expected error for unknown section")
	}
}

func TestCreateNewSong_WritesLoadableShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "newproj")
	path, err := CreateNewSong(dir, "My Song", 140, "3/4")
	if err != nil {
		t.Fatalf("CreateNewSong: %v", err)
	}
	if path != filepath.Join(dir, "song.toml") {
		t.Errorf("path = %q", path)
	}
	// patterns/ dir must exist so the loader can walk it.
	if _, err := os.Stat(filepath.Join(dir, "patterns")); err != nil {
		t.Errorf("patterns/ dir not created: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load newly-created song: %v", err)
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
	if len(s.Arrangement) != 0 {
		t.Errorf("arrangement should be empty, got %v", s.Arrangement)
	}
}

func TestCreateNewSong_RejectsNonEmptyDir(t *testing.T) {
	dir := t.TempDir() // empty by default
	if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNewSong(dir, "t", 120, "4/4"); err == nil {
		t.Error("expected error for non-empty directory")
	}
}
