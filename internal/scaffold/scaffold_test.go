package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"seqone/internal/song"
)

func TestWrite_CreatesLoadableProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-song")
	if err := Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// song.toml and patterns/a.pat must exist.
	for _, rel := range []string{"song.toml", "patterns/a.pat", "samples"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}

	// The scaffold must load cleanly through the real loader.
	s, err := song.Load(filepath.Join(dir, "song.toml"))
	if err != nil {
		t.Fatalf("scaffolded song failed to load: %v", err)
	}
	if s.Project.Title != "new-song" {
		t.Errorf("title = %q, want new-song (from dir basename)", s.Project.Title)
	}
	if len(s.Arrangement) != 1 || s.Arrangement[0].Section != "a" || s.Arrangement[0].Repeat != 1 {
		t.Errorf("arrangement = %v, want [{a ×1}]", s.Arrangement)
	}
	if _, ok := s.Sections["a"]; !ok {
		t.Errorf("expected scaffolded section %q, sections = %v", "a", s.Sections)
	}
}

func TestWrite_RefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir() // exists and contains nothing by default
	// Put something in it.
	if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir); err == nil {
		t.Error("expected error for non-empty directory, got nil")
	}
}

func TestWrite_AcceptsEmptyDir(t *testing.T) {
	dir := t.TempDir() // empty
	if err := Write(dir); err != nil {
		t.Fatalf("Write on empty dir: %v", err)
	}
}
