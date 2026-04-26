package song

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPresetLibraryParses walks the in-repo `presets/` tree and parses every
// `.pat` file through ParsePattern. Catches typos, miscounted cells, and
// invalid pitch/velocity tokens in shipped preset content.
func TestPresetLibraryParses(t *testing.T) {
	root := filepath.Join("..", "..", "presets")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("no presets/ directory at %s", root)
	}

	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".pat") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("%s: open: %v", path, err)
			return nil
		}
		defer f.Close()
		name := strings.TrimSuffix(filepath.Base(path), ".pat")
		if _, err := ParsePattern(name, f); err != nil {
			t.Errorf("%s: parse: %v", path, err)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count == 0 {
		t.Fatal("no .pat files found under presets/")
	}
	t.Logf("parsed %d preset .pat files", count)
}

// TestPresetAuditionSongsLoad loads each `examples/preset-audition-*` song
// through the full LoadSong pipeline. Catches mismatches between the
// audition song.toml and the bundle's .pat files (track IDs, pattern names,
// bar counts, missing sample WAVs).
func TestPresetAuditionSongsLoad(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	matches, err := filepath.Glob(filepath.Join(root, "preset-audition-*", "song.toml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("no preset-audition-* example songs")
	}
	for _, path := range matches {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			s, err := Load(path)
			if err != nil {
				t.Fatalf("Load %s: %v", path, err)
			}
			for id, samp := range s.Samples {
				if _, err := os.Stat(samp.Path); err != nil {
					t.Errorf("sample %q path %s: %v", id, samp.Path, err)
				}
			}
		})
	}
}
