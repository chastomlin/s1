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
		Enabled: true,
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
		Enabled:     true,
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

// TestSetTrackEQ_BypassPreservesSettings flips Enabled off and back
// on, and confirms the band gains survive both writes — the whole
// point of the explicit flag is non-destructive A/B.
func TestSetTrackEQ_BypassPreservesSettings(t *testing.T) {
	path := mixTestSong(t)
	cfg := EQConfig{
		Enabled: true,
		LowFreq: 200, LowGain: 4,
		MidFreq: 1000, MidQ: 1, MidGain: -2,
		HighFreq: 5000, HighGain: 3,
	}
	if err := SetTrackEQ(path, "k", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if err := SetTrackEQ(path, "k", cfg); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].EQ
	if got.Enabled {
		t.Errorf("Enabled = true, want false after bypass")
	}
	if got.LowGain != 4 || got.MidGain != -2 || got.HighGain != 3 {
		t.Errorf("gains lost on bypass: got %+v", got)
	}
	if got.IsActive() {
		t.Errorf("IsActive = true while bypassed")
	}
}

// TestSetTrackFilter_RoundTrip writes a full filter block and confirms
// each field comes back through Load.
func TestSetTrackFilter_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := FilterConfig{
		Enabled:   true,
		Type:      FilterLowpass,
		Cutoff:    800,
		Resonance: 0.6,
	}
	if err := SetTrackFilter(path, "k", want); err != nil {
		t.Fatalf("SetTrackFilter: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Filter
	if got != want {
		t.Errorf("Filter round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestSetTrackDrive_RoundTrip writes a full overdrive block with each
// field set and confirms it survives Load.
func TestSetTrackDrive_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := DriveConfig{
		Enabled: true,
		Type:    DriveHard,
		Drive:   0.6,
		Tone:    0.4,
		Level:   1.2,
	}
	if err := SetTrackDrive(path, "k", want); err != nil {
		t.Fatalf("SetTrackDrive: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Drive
	if got != want {
		t.Errorf("Drive round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestSetTrackReverb_RoundTrip writes a full reverb block and confirms
// each field comes back through Load.
func TestSetTrackReverb_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := ReverbConfig{
		Enabled: true,
		Size:    0.7,
		Damping: 0.3,
		Mix:     0.4,
	}
	if err := SetTrackReverb(path, "k", want); err != nil {
		t.Fatalf("SetTrackReverb: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Reverb
	if got != want {
		t.Errorf("Reverb round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

// TestSetTrackLofi_RoundTrip — same shape for the bitcrush effect.
func TestSetTrackLofi_RoundTrip(t *testing.T) {
	path := mixTestSong(t)
	want := LofiConfig{
		Enabled: true,
		Bits:    8,
		Rate:    22050,
	}
	if err := SetTrackLofi(path, "k", want); err != nil {
		t.Fatalf("SetTrackLofi: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Tracks["k"].Lofi
	if got != want {
		t.Errorf("Lofi round-trip mismatch:\n got  %+v\n want %+v", got, want)
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
