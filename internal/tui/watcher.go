package tui

import (
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// fileChangedMsg is emitted when the watched song.toml or patterns/ dir
// has any filesystem event — write, create, rename, remove. The TUI
// responds by re-parsing the song locally and issuing a reload to the
// engine. Coalesced by the natural message-at-a-time flow of bubbletea.
type fileChangedMsg struct{ path string }

// startWatcher begins watching songPath's containing directory and its
// patterns subdirectory. Returns the watcher (which callers must close) and
// an error. On a nil/empty path, returns (nil, nil) — file-watch is
// optional and disabled without a song path.
func startWatcher(songPath string) (*fsnotify.Watcher, error) {
	if songPath == "" {
		return nil, nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	baseDir := filepath.Dir(songPath)
	// Watching the containing directory catches song.toml writes even when
	// the editor saves via rename-over (which is most of them).
	if err := w.Add(baseDir); err != nil {
		w.Close()
		return nil, err
	}
	patternsDir := filepath.Join(baseDir, "patterns")
	// Patterns dir is optional in case a freshly-scaffolded project hasn't
	// been set up yet, but the scaffolder creates it by default.
	if err := w.Add(patternsDir); err != nil {
		// Not fatal — song might not have a patterns dir yet.
		_ = err
	}
	return w, nil
}
