package song

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mixTestSong builds a minimal song with a single sample track "k"
// declared and lays the on-disk pattern + sample so Load succeeds.
// Returns the song.toml path.
func mixTestSong(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	if err := os.WriteFile(filepath.Join(dir, "patterns/k.pat"), []byte(patBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendSection(songPath, "verse", 1); err != nil {
		t.Fatal(err)
	}
	if err := AppendSectionPart(songPath, "verse", "k", "k"); err != nil {
		t.Fatal(err)
	}
	return songPath
}

// TestSetTrackPan_RoundTrip writes a pan, reloads, verifies the
// loaded value matches what we wrote (within float precision).
func TestSetTrackPan_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	if err := SetTrackPan(path, "k", -0.45); err != nil {
		t.Fatalf("SetTrackPan: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Pan
	if got != -0.45 {
		t.Errorf("loaded pan = %f, want -0.45", got)
	}
}

// TestSetTrackPan_Clamps verifies absurd inputs are clipped to the
// schema's accepted range rather than written verbatim.
func TestSetTrackPan_Clamps(t *testing.T) {
	path := mixTestSong(t)
	if err := SetTrackPan(path, "k", 5.0); err != nil {
		t.Fatalf("SetTrackPan: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Tracks["k"].Pan != 1.0 {
		t.Errorf("loaded pan = %f, want 1.0 (clamped)", s.Tracks["k"].Pan)
	}
}

// TestSetTrackGain_Insert verifies that inserting (no existing gain
// line) succeeds and the new line lands inside the track's block.
func TestSetTrackGain_Insert(t *testing.T) {
	path := mixTestSong(t)
	if err := SetTrackGain(path, "k", 1.5); err != nil {
		t.Fatalf("SetTrackGain: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "gain = 1.5") {
		t.Errorf("expected gain = 1.5 in toml:\n%s", got)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Tracks["k"].Gain != 1.5 {
		t.Errorf("loaded gain = %f, want 1.5", s.Tracks["k"].Gain)
	}
}

// TestSetTrackEQ_RoundTrip writes a full EQ block and confirms each
// field comes back through Load.
func TestSetTrackEQ_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := EQConfig{
		LowFreq: 250, LowGain: 3.0,
		MidFreq: 800, MidQ: 0.7, MidGain: -2.0,
		HighFreq: 8000, HighGain: 1.0,
	}
	if err := SetTrackEQ(path, "k", want); err != nil {
		t.Fatalf("SetTrackEQ: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].EQ
	if got != want {
		t.Errorf("EQ round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestSetTrackComp_RoundTrip — same shape, different values.
func TestSetTrackComp_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := CompConfig{
		ThresholdDB: -18, Ratio: 6.0,
		AttackMs: 2, ReleaseMs: 120, MakeupDB: 3.0,
	}
	if err := SetTrackComp(path, "k", want); err != nil {
		t.Fatalf("SetTrackComp: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Comp
	if got != want {
		t.Errorf("Comp round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestSetTrack_ReplaceExisting overwrites an already-present `gain`
// line rather than duplicating it.
func TestSetTrack_ReplaceExisting(t *testing.T) {
	path := mixTestSong(t)
	if err := SetTrackGain(path, "k", 0.5); err != nil {
		t.Fatal(err)
	}
	if err := SetTrackGain(path, "k", 1.2); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Count(string(got), "gain =") != 1 {
		t.Errorf("expected exactly one gain= line, got:\n%s", got)
	}
}
