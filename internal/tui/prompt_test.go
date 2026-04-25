package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSongPath_DirWithSongToml(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte("[project]\nbpm=120\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSongPath(dir)
	if err != nil {
		t.Fatalf("resolveSongPath(dir): %v", err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Errorf("dir path: got %q want %q", got, want)
	}
}

func TestResolveSongPath_FilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mysong.toml")
	if err := os.WriteFile(path, []byte("[project]\nbpm=120\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSongPath(path)
	if err != nil {
		t.Fatalf("resolveSongPath(file): %v", err)
	}
	want, _ := filepath.Abs(path)
	if got != want {
		t.Errorf("file path: got %q want %q", got, want)
	}
}

func TestResolveSongPath_DirMissingSong(t *testing.T) {
	dir := t.TempDir() // no song.toml
	_, err := resolveSongPath(dir)
	if err == nil {
		t.Error("expected error for dir without song.toml")
	}
}

func TestResolveSongPath_Nonexistent(t *testing.T) {
	if _, err := resolveSongPath("/definitely/not/here"); err == nil {
		t.Error("expected error for nonexistent path")
	}
	if _, err := resolveSongPath(""); err == nil {
		t.Error("expected error for empty path")
	}
}

// setupCompletionTree creates a small directory tree used by the tab-complete
// tests. Returns the root dir.
func setupCompletionTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(path string, isDir bool) {
		full := filepath.Join(root, path)
		if isDir {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("examples", true)
	mk("example-song", true)
	mk("extra", true)
	mk("notes.txt", false)
	mk(".hidden", true)
	return root
}

func TestCompletePath_SingleMatchCompletes(t *testing.T) {
	root := setupCompletionTree(t)
	p := promptState{kind: promptLoad, value: filepath.Join(root, "not")}
	out := completePath(p)
	// "not" only matches "notes.txt" in this tree.
	if !filepath.IsAbs(out.value) {
		t.Fatalf("value not absolute: %q", out.value)
	}
	if filepath.Base(out.value) != "notes.txt" {
		t.Errorf("single match: got basename %q, want notes.txt", filepath.Base(out.value))
	}
	if out.suggest != nil {
		t.Errorf("single match: expected no suggestions, got %v", out.suggest)
	}
}

func TestCompletePath_LongestCommonPrefixAdvances(t *testing.T) {
	root := setupCompletionTree(t)
	p := promptState{kind: promptLoad, value: filepath.Join(root, "ex")}
	out := completePath(p)
	// "ex" matches example-song/, examples/, extra/. LCP is "ex".
	// Wait — LCP of example-song/, examples/, extra/ is "ex" itself (since
	// extra/ shares only "ex"). So no advance happens; suggestions show up.
	if out.suggest == nil {
		t.Errorf("expected suggestions when LCP doesn't advance, got none. value=%q", out.value)
	}
}

func TestCompletePath_TwoWayAdvances(t *testing.T) {
	root := setupCompletionTree(t)
	p := promptState{kind: promptLoad, value: filepath.Join(root, "exa")}
	out := completePath(p)
	// "exa" matches example-song/ and examples/. LCP is "example".
	if filepath.Base(out.value) != "example" {
		t.Errorf("expected LCP to advance value basename to 'example', got %q", filepath.Base(out.value))
	}
	if out.suggest != nil {
		t.Errorf("expected no suggestions after LCP advance, got %v", out.suggest)
	}
}

func TestCompletePath_DirectoryGetsTrailingSlash(t *testing.T) {
	root := setupCompletionTree(t)
	// After LCP advance to "example", another tab with "example-" completes
	// to "example-song" — a directory — which should get a trailing slash.
	p := promptState{kind: promptLoad, value: filepath.Join(root, "example-")}
	out := completePath(p)
	if !filepath.IsAbs(out.value) {
		t.Fatal("value should still be absolute")
	}
	if !hasSuffix(out.value, "example-song/") {
		t.Errorf("directory completion missing trailing slash: %q", out.value)
	}
}

func TestCompletePath_HidesDotfiles(t *testing.T) {
	root := setupCompletionTree(t)
	// ".hidden" exists but shouldn't surface when typing just the dir.
	p := promptState{kind: promptLoad, value: root + "/"}
	out := completePath(p)
	for _, s := range out.suggest {
		if s == ".hidden/" {
			t.Error("dotfile leaked into suggestions without explicit dot")
		}
	}
}

func TestCompletePath_ShowsDotfilesWhenPrefixed(t *testing.T) {
	root := setupCompletionTree(t)
	// Use literal string concatenation so the trailing "." survives —
	// filepath.Join would normalize it away.
	p := promptState{kind: promptLoad, value: root + "/."}
	out := completePath(p)
	if !hasSuffix(out.value, ".hidden/") {
		t.Errorf("explicit dot prefix should complete to .hidden/, got %q", out.value)
	}
}

func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

func TestLongestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"abc", "abd"}, "ab"},
		{[]string{"abc", "def"}, ""},
		{[]string{"examples/", "example-song/"}, "example"},
	}
	for _, c := range cases {
		if got := longestCommonPrefix(c.in); got != c.want {
			t.Errorf("LCP(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
