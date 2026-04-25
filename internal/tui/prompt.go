package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// promptKind discriminates the active prompt. promptNone means no prompt
// is open and normal keybindings are in effect.
type promptKind int

const (
	promptNone promptKind = iota
	promptLoad
	promptAddTrackKind    // initial "(s)ample or (i)nstrument?" chooser
	promptAddSampleTrack  // wizard: id → path → note
	promptAddInstrTrack   // wizard: id → channel → program
	promptTempo
	promptNewSongPath        // new-song wizard: path → title → bpm → time_sig
	promptNewSectionName     // new-section wizard: name → bars
	promptConfirmDeleteTrack // y/n confirm before DeleteTrack wipes it everywhere
	promptConfirmQuit        // y/n confirm before exiting the TUI
)

// promptState drives the inline input mode at the bottom of the TUI. It
// handles the load-song prompt plus the add-track and tempo wizards.
// suggest holds tab-completion candidates; message is a transient error
// or hint cleared on the next keystroke.
//
// For multi-step wizards, step indexes into the kind's step list and
// answers accumulates the validated responses so we can execute the
// wizard's write action once the last step is confirmed.
type promptState struct {
	kind    promptKind
	value   string
	suggest []string
	message string

	step    int
	answers []string
}

func (p promptState) active() bool { return p.kind != promptNone }

// resolveSongPath accepts either a directory (expects song.toml inside) or
// a file path, and returns an absolute path to the song.toml to load.
// ~ is expanded. Returns an error if nothing matches.
func resolveSongPath(typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(expandTilde(typed))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", typed, err)
	}
	if info.IsDir() {
		candidate := filepath.Join(abs, "song.toml")
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("no song.toml in %s", typed)
		}
		return candidate, nil
	}
	return abs, nil
}

// expandTilde resolves a leading "~/" to the user's home directory. A bare
// "~" (no slash) is returned unchanged — it might be a literal filename.
func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// completePath does one step of tab completion on p.value. If the typed
// basename matches exactly one entry, that entry is filled in (with a
// trailing "/" for directories). If several entries match, it completes to
// the longest common prefix if that advances; otherwise the candidates are
// stashed in p.suggest for the renderer to display. Absolute, relative,
// and tilde-prefixed paths all work.
func completePath(p promptState) promptState {
	dir, base, prefixPreserved := splitForCompletion(p.value)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}

	var matches []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// Hide dotfiles unless the user already typed a dot.
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		p.suggest = nil
		return p
	}
	if len(matches) == 1 {
		p.value = prefixPreserved + matches[0]
		p.suggest = nil
		return p
	}
	lcp := longestCommonPrefix(matches)
	if len(lcp) > len(base) {
		p.value = prefixPreserved + lcp
		p.suggest = nil
	} else {
		p.suggest = matches
	}
	return p
}

// splitForCompletion returns the directory to list, the basename prefix to
// match against its entries, and the portion of the typed input that must
// be preserved when reconstructing the completed value (everything up to
// and including the last "/"). Handles "~/..." by expanding for the read
// but keeping the literal "~/" in the reconstruction prefix.
func splitForCompletion(typed string) (dir, base, prefixPreserved string) {
	idx := strings.LastIndex(typed, "/")
	if idx < 0 {
		return ".", typed, ""
	}
	prefixPreserved = typed[:idx+1]
	base = typed[idx+1:]
	dir = expandTilde(prefixPreserved)
	if dir == "" {
		dir = "."
	}
	return dir, base, prefixPreserved
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}
