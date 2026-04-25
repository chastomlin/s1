package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// --- New-song wizard: path → title → bpm → time signature ------------------

var newSongSteps = []addTrackStep{
	{
		label: "new song — directory (created if absent, must be empty)",
		validate: func(s string, _ Model) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("path required")
			}
			return nil
		},
	},
	{
		label: "new song — title",
		validate: func(_ string, _ Model) error { return nil },
	},
	{
		label: "new song — tempo (bpm, 20-400)",
		validate: func(s string, _ Model) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 20 || n > 400 {
				return fmt.Errorf("tempo must be 20..400")
			}
			return nil
		},
	},
	{
		label: "new song — time signature (e.g. 4/4, 3/4) — enter to accept default 4/4",
		validate: func(s string, _ Model) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			// Accept anything of the form "N/D" with positive integers.
			slash := strings.IndexByte(s, '/')
			if slash <= 0 || slash == len(s)-1 {
				return fmt.Errorf("time signature must look like N/D")
			}
			if n, err := strconv.Atoi(s[:slash]); err != nil || n <= 0 {
				return fmt.Errorf("time signature numerator must be a positive integer")
			}
			if d, err := strconv.Atoi(s[slash+1:]); err != nil || d <= 0 {
				return fmt.Errorf("time signature denominator must be a positive integer")
			}
			return nil
		},
	},
}

func (m Model) updateNewSongWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyEnter {
		return m, nil
	}
	answer := strings.TrimSpace(m.prompt.value)
	if err := newSongSteps[m.prompt.step].validate(answer, m); err != nil {
		m.prompt.message = err.Error()
		return m, nil
	}
	m.prompt.answers = append(m.prompt.answers, answer)
	m.prompt.value = ""
	m.prompt.step++
	if m.prompt.step < len(newSongSteps) {
		return m, nil
	}

	dir := expandTilde(m.prompt.answers[0])
	abs, err := filepath.Abs(dir)
	if err != nil {
		m.prompt.message = err.Error()
		m.prompt.step--
		m.prompt.answers = m.prompt.answers[:len(m.prompt.answers)-1]
		return m, nil
	}
	title := m.prompt.answers[1]
	if title == "" {
		title = filepath.Base(abs)
	}
	bpm, _ := strconv.Atoi(m.prompt.answers[2])
	timeSig := m.prompt.answers[3]
	if timeSig == "" {
		timeSig = "4/4"
	}

	path, err := song.CreateNewSong(abs, title, bpm, timeSig)
	if err != nil {
		m.prompt.message = err.Error()
		// Roll back the last step so the user can fix and retry.
		m.prompt.step--
		m.prompt.answers = m.prompt.answers[:len(m.prompt.answers)-1]
		return m, nil
	}

	m.prompt = promptState{}
	// Swap the file-watcher onto the new song and load it everywhere.
	if m.watcher != nil {
		_ = m.watcher.Close()
		m.watcher = nil
	}
	if w, werr := startWatcher(path); werr == nil {
		m.watcher = w
	}
	m.songPath = path
	cmds := []tea.Cmd{
		sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdLoad, Path: path}),
		loadSongLocal(path),
	}
	if m.watcher != nil {
		cmds = append(cmds, awaitFileChange(m.watcher))
	}
	return m, tea.Batch(cmds...)
}

// --- New-section wizard: name → bars ---------------------------------------

var newSectionSteps = []addTrackStep{
	{
		label: "new section — name",
		validate: func(s string, m Model) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return fmt.Errorf("name required")
			}
			if m.song != nil {
				if _, dup := m.song.Sections[s]; dup {
					return fmt.Errorf("section %q already exists", s)
				}
			}
			return nil
		},
	},
	{
		label: "new section — bars",
		validate: func(s string, _ Model) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n <= 0 {
				return fmt.Errorf("bars must be a positive integer")
			}
			return nil
		},
	},
}

func (m Model) updateNewSectionWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyEnter {
		return m, nil
	}
	answer := strings.TrimSpace(m.prompt.value)
	if err := newSectionSteps[m.prompt.step].validate(answer, m); err != nil {
		m.prompt.message = err.Error()
		return m, nil
	}
	m.prompt.answers = append(m.prompt.answers, answer)
	m.prompt.value = ""
	m.prompt.step++
	if m.prompt.step < len(newSectionSteps) {
		return m, nil
	}

	name := m.prompt.answers[0]
	bars, _ := strconv.Atoi(m.prompt.answers[1])
	if err := song.AppendSection(m.songPath, name, bars); err != nil {
		m.prompt.message = err.Error()
		m.prompt.step--
		m.prompt.answers = m.prompt.answers[:len(m.prompt.answers)-1]
		return m, nil
	}
	m.prompt = promptState{}
	// Watcher will reload, but send an explicit reload so the engine
	// picks up the new section without waiting for fsnotify.
	return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
}

// executeSectionScopedAddTrack is called after the existing add-track
// wizard finishes, when it was invoked from modeSection. It creates a
// starter pattern file matching the section's bars and binds the new
// track to the section via AppendSectionPart. Errors here leave the
// track added to [[tracks]] but unbound — the user can retry with 'a'.
func (m Model) executeSectionScopedAddTrack(trackID string) error {
	if m.sectionView == "" {
		return nil
	}
	sec, ok := m.song.Sections[m.sectionView]
	if !ok {
		// Song wasn't reloaded yet — fall back to defaults. The watcher
		// will normalize on reload.
		sec = song.Section{Bars: 4, BeatsPerBar: 4, Resolution: 4}
	}
	patternName := m.sectionView + "-" + trackID
	patternPath := filepath.Join(filepath.Dir(m.songPath), "patterns", patternName+".pat")

	bpb := sec.BeatsPerBar
	if bpb <= 0 {
		bpb = 4
	}
	res := sec.Resolution
	if res <= 0 {
		res = 4
	}
	cellCount := sec.Bars * bpb * res
	cells := make([]song.Cell, cellCount)
	for i := range cells {
		cells[i] = song.Cell{Kind: song.CellRest}
	}
	pat := song.Pattern{
		Name:        patternName,
		Bars:        sec.Bars,
		BeatsPerBar: bpb,
		Resolution:  res,
		Cells:       cells,
	}
	// Only write the pattern file if it doesn't exist yet — avoid
	// clobbering a user-authored file that happens to share the name.
	if _, err := os.Stat(patternPath); os.IsNotExist(err) {
		if err := song.WritePattern(patternPath, pat); err != nil {
			return fmt.Errorf("write %s: %w", patternName+".pat", err)
		}
	}
	return song.AppendSectionPart(m.songPath, m.sectionView, trackID, patternName)
}
