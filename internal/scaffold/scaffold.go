// Package scaffold writes a minimal, valid seqone project to disk so the
// user can start composing without hand-writing TOML from scratch.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const songTemplate = `[project]
title          = "{{title}}"
bpm            = 120
time_signature = "4/4"

# --- Samples ---------------------------------------------------------------
# Uncomment and add audio files here, then reference them from [[tracks]].
# [[samples]]
# id   = "k"
# path = "samples/kick.wav"

# --- Instruments -----------------------------------------------------------
# MIDI instruments. Referenced from [[tracks]] for pitched-note tracks.
# [[instruments]]
# id      = "bass"
# kind    = "midi"
# channel = 1
# program = 33

# --- Tracks ----------------------------------------------------------------
# Each track binds a pattern-track ID (e.g. "k", "bass") to either a sample
# (drum) or an instrument (pitched MIDI). Sample tracks require a note.
# [[tracks]]
# id      = "k"
# name    = "Kick"
# sample  = "k"
# channel = 10
# note    = 36

# [[tracks]]
# id         = "bass"
# name       = "Bass"
# instrument = "bass"
# channel    = 1

# --- Sections --------------------------------------------------------------
# A section is an N-bar arrangement unit. The 'parts' map binds a track id
# to the pattern file (patterns/<name>.pat) that track plays for the
# section. Tracks not listed here are silent.
[[sections]]
name  = "a"
bars  = 1
parts = {}

[song]
arrangement = ["a"]
`

const patternTemplate = `# Per-track pattern — 1 bar, 16 cells (4 beats * resolution 4).
# Tokens:
#   .     rest
#   -     tie (hold previous note)
#   X     sample trigger
#   C4    note-on (pitch + octave). Optional ".vel" for 1..9 velocity.
# '|' characters are cosmetic bar/beat separators and are ignored.
#
# Reference this file from a [[sections]].parts entry in song.toml.

bars 1
resolution 4

. . . . | . . . . | . . . . | . . . .
`

// Write creates a new seqone project at dir. dir must not already exist
// (or must be an empty directory). Writes song.toml, patterns/a.pat, and
// an empty samples/ dir for convenience.
func Write(dir string) error {
	if err := ensureEmptyOrNew(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "patterns"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "samples"), 0o755); err != nil {
		return err
	}

	title := filepath.Base(dir)
	if title == "." || title == "/" {
		title = "untitled"
	}
	song := []byte(renderTemplate(songTemplate, map[string]string{"title": title}))
	if err := os.WriteFile(filepath.Join(dir, "song.toml"), song, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "patterns", "a.pat"), []byte(patternTemplate), 0o644); err != nil {
		return err
	}
	return nil
}

// ensureEmptyOrNew fails if dir exists and is non-empty, and succeeds if it
// doesn't exist (it will be created) or exists but contains nothing.
func ensureEmptyOrNew(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty — refusing to overwrite", dir)
	}
	return nil
}

// renderTemplate substitutes {{key}} placeholders with values from vars.
func renderTemplate(body string, vars map[string]string) string {
	out := body
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}
