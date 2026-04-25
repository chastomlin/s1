package song

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSongFile creates a minimal test project under a temp dir and
// returns the song.toml path. The caller seeds the file's content.
func writeSongFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	if err := os.WriteFile(filepath.Join(dir, "patterns/a.pat"), []byte(patBody), 0o644); err != nil {
		t.Fatal(err)
	}
	// Tests that don't declare sections themselves still arrange ["a"] — inject
	// a trivial section block so the loader is happy.
	if !strings.Contains(content, "[[sections]]") {
		if idx := strings.Index(content, "[song]"); idx >= 0 {
			content = content[:idx] + "[[sections]]\nname = \"a\"\nbars = 1\nparts = {}\n\n" + content[idx:]
		}
	}
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetBPM_ReplaceExisting(t *testing.T) {
	path := writeSongFile(t, `[project]
title = "t"
bpm = 120
time_signature = "4/4"

[song]
arrangement = ["a"]
`)
	if err := SetBPM(path, 140); err != nil {
		t.Fatalf("SetBPM: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "bpm = 140") {
		t.Errorf("expected 'bpm = 140' in output:\n%s", got)
	}
	if strings.Contains(string(got), "bpm = 120") {
		t.Errorf("old bpm line still present:\n%s", got)
	}
	// Title and time_signature should be untouched.
	if !strings.Contains(string(got), `title = "t"`) {
		t.Error("unrelated title line was modified")
	}

	// Must still load cleanly.
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after SetBPM: %v", err)
	}
	if s.Project.BPM != 140 {
		t.Errorf("parsed bpm = %d, want 140", s.Project.BPM)
	}
}

func TestSetBPM_PreservesComments(t *testing.T) {
	path := writeSongFile(t, `# header comment
[project]
# inline bpm comment
bpm = 120  # trailing

[song]
arrangement = ["a"]
`)
	if err := SetBPM(path, 90); err != nil {
		t.Fatalf("SetBPM: %v", err)
	}
	got, _ := os.ReadFile(path)
	for _, want := range []string{"# header comment", "# inline bpm comment", "# trailing", "bpm = 90"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestSetBPM_InsertsWhenMissing(t *testing.T) {
	path := writeSongFile(t, `[project]
title = "t"

[song]
arrangement = ["a"]
`)
	// Can't load this song (bpm required) but editor should still handle it.
	if err := SetBPM(path, 100); err != nil {
		t.Fatalf("SetBPM: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "bpm = 100") {
		t.Errorf("expected inserted bpm line:\n%s", got)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load after insert: %v", err)
	}
}

func TestSetBPM_ErrorsWithoutProject(t *testing.T) {
	path := writeSongFile(t, `[song]
arrangement = ["a"]
`)
	if err := SetBPM(path, 120); err == nil {
		t.Error("expected error when [project] missing")
	}
}

func TestSetBPM_RejectsOutOfRange(t *testing.T) {
	path := writeSongFile(t, `[project]
bpm = 120

[song]
arrangement = ["a"]
`)
	for _, bad := range []int{0, 10, 500, -5} {
		if err := SetBPM(path, bad); err == nil {
			t.Errorf("bpm=%d: expected rejection", bad)
		}
	}
}

func TestAppendSampleTrack_AddsLoadableEntries(t *testing.T) {
	path := writeSongFile(t, `[project]
bpm = 120

[song]
arrangement = ["a"]
`)
	if err := AppendSampleTrack(path, "k", "samples/kick.wav", 36); err != nil {
		t.Fatalf("AppendSampleTrack: %v", err)
	}

	got, _ := os.ReadFile(path)
	for _, want := range []string{
		`[[samples]]`,
		`[[tracks]]`,
		`id      = "k"`,
		`sample  = "k"`,
		`channel = 10`,
		`note    = 36`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("expected %q in output:\n%s", want, got)
		}
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after append: %v", err)
	}
	trk, ok := s.Tracks["k"]
	if !ok {
		t.Fatal("track 'k' missing after append")
	}
	if trk.Sample != "k" || trk.Channel != 10 || trk.Note != 36 {
		t.Errorf("track = %+v", trk)
	}
}

func TestAppendInstrumentTrack_AddsLoadableEntries(t *testing.T) {
	path := writeSongFile(t, `[project]
bpm = 120

[song]
arrangement = ["a"]
`)
	if err := AppendInstrumentTrack(path, "bass", 1, 33); err != nil {
		t.Fatalf("AppendInstrumentTrack: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load after append: %v", err)
	}
	trk, ok := s.Tracks["bass"]
	if !ok {
		t.Fatal("track 'bass' missing")
	}
	if trk.Instrument != "bass" || trk.Channel != 1 {
		t.Errorf("track = %+v", trk)
	}
	if inst, ok := s.Instruments["bass"]; !ok || inst.Program != 33 || inst.Kind != "midi" {
		t.Errorf("instrument = %+v, ok=%v", inst, ok)
	}
}

func TestAppendSampleTrack_RejectsInvalidID(t *testing.T) {
	path := writeSongFile(t, `[project]
bpm = 120

[song]
arrangement = ["a"]
`)
	if err := AppendSampleTrack(path, "bad id!", "s.wav", 36); err == nil {
		t.Error("expected rejection of id with space and bang")
	}
	if err := AppendSampleTrack(path, "", "s.wav", 36); err == nil {
		t.Error("expected rejection of empty id")
	}
	if err := AppendSampleTrack(path, "ok", "s.wav", 200); err == nil {
		t.Error("expected rejection of out-of-range note")
	}
}

func TestEditAtomically_PreservesPermissions(t *testing.T) {
	path := writeSongFile(t, `[project]
bpm = 120

[song]
arrangement = ["a"]
`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetBPM(path, 130); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("mode = %o, want 640", mode)
	}
}
