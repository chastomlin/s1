package song

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveSectionPart_Drops unbinds the track from a section while
// leaving the [[tracks]] block and other parts intact.
func TestRemoveSectionPart_Drops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	// Two sample tracks + a section with both bound.
	if err := AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	if err := AppendSampleTrack(songPath, "s", "samples/snare.wav", 38); err != nil {
		t.Fatal(err)
	}
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	for _, n := range []string{"k", "s"} {
		if err := os.WriteFile(filepath.Join(dir, "patterns", n+".pat"), []byte(patBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendSection(songPath, "verse", 1); err != nil {
		t.Fatal(err)
	}
	if err := AppendSectionPart(songPath, "verse", "k", "k"); err != nil {
		t.Fatal(err)
	}
	if err := AppendSectionPart(songPath, "verse", "s", "s"); err != nil {
		t.Fatal(err)
	}

	// Drop "s" from verse. "k" should remain bound.
	if err := RemoveSectionPart(songPath, "verse", "s"); err != nil {
		t.Fatalf("RemoveSectionPart: %v", err)
	}
	s, err := Load(songPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sec := s.Sections["verse"]
	if _, bound := sec.Parts["s"]; bound {
		t.Error("snare should no longer be bound to verse")
	}
	if sec.Parts["k"] != "k" {
		t.Errorf("kick should still be bound: %+v", sec.Parts)
	}
	// [[tracks]] entry for 's' must still exist.
	if _, ok := s.Tracks["s"]; !ok {
		t.Error("snare track block was removed by RemoveSectionPart — should only touch the section")
	}
}

// TestRemoveSectionPart_NonexistentKey is a no-op, not an error.
func TestRemoveSectionPart_NonexistentKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, _ := CreateNewSong(dir, "t", 120, "4/4")
	if err := AppendSection(songPath, "verse", 1); err != nil {
		t.Fatal(err)
	}
	// Remove something that isn't there.
	if err := RemoveSectionPart(songPath, "verse", "ghost"); err != nil {
		t.Errorf("expected no-op, got %v", err)
	}
}

// TestDeleteTrack_RemovesBlockAndAllPartsRefs wipes a track from both
// the [[tracks]] library and every section's parts map.
func TestDeleteTrack_RemovesBlockAndAllPartsRefs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendSampleTrack(songPath, "k", "samples/kick.wav", 36); err != nil {
		t.Fatal(err)
	}
	if err := AppendSampleTrack(songPath, "s", "samples/snare.wav", 38); err != nil {
		t.Fatal(err)
	}
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	for _, n := range []string{"k", "s"} {
		if err := os.WriteFile(filepath.Join(dir, "patterns", n+".pat"), []byte(patBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Two sections reference "k".
	for _, name := range []string{"verse", "chorus"} {
		if err := AppendSection(songPath, name, 1); err != nil {
			t.Fatal(err)
		}
		if err := AppendSectionPart(songPath, name, "k", "k"); err != nil {
			t.Fatal(err)
		}
		if err := AppendSectionPart(songPath, name, "s", "s"); err != nil {
			t.Fatal(err)
		}
	}

	if err := DeleteTrack(songPath, "k"); err != nil {
		t.Fatalf("DeleteTrack: %v", err)
	}
	s, err := Load(songPath)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if _, ok := s.Tracks["k"]; ok {
		t.Error("[[tracks]] block for k should be gone")
	}
	// Snare untouched.
	if _, ok := s.Tracks["s"]; !ok {
		t.Error("snare track should still exist")
	}
	for _, name := range []string{"verse", "chorus"} {
		if _, bound := s.Sections[name].Parts["k"]; bound {
			t.Errorf("%s.parts still contains k after delete", name)
		}
		if s.Sections[name].Parts["s"] != "s" {
			t.Errorf("%s.parts[s] should remain: %+v", name, s.Sections[name].Parts)
		}
	}
	// Sample / pattern file are left on disk for recovery.
	if _, err := os.Stat(filepath.Join(dir, "patterns", "k.pat")); err != nil {
		t.Errorf("k.pat should remain on disk after delete: %v", err)
	}
	// [[samples]] entry left alone (might be reused). Verify by re-loading
	// and checking Samples map — tolerates formatBlock's column alignment.
	if _, ok := s.Samples["k"]; !ok {
		raw, _ := os.ReadFile(songPath)
		t.Errorf("[[samples]] id=k should still exist after DeleteTrack:\n%s", raw)
	}
}

// TestDeleteTrack_NonexistentIsNoop — deleting a track that never
// existed must not corrupt the file.
func TestDeleteTrack_NonexistentIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	songPath, err := CreateNewSong(dir, "t", 120, "4/4")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(songPath)
	if err := DeleteTrack(songPath, "ghost"); err != nil {
		t.Fatalf("DeleteTrack: %v", err)
	}
	after, _ := os.ReadFile(songPath)
	if string(before) != string(after) {
		t.Errorf("file changed when deleting a non-existent track:\n-- before --\n%s\n-- after --\n%s", before, after)
	}
}
