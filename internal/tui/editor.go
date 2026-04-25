package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// uiMode discriminates the top-level view.
//
//	arrangement — song-level: the sequence of sections (the "song" itself)
//	section     — a specific section's tracks + per-track activity bars
//	edit        — horizontal cell-grid editor for the section's patterns
type uiMode int

const (
	modeArrangement uiMode = iota
	modeSection
	modeEdit
	modeMix
)

// editorState holds the scratchpad copy of the section being edited plus
// cursor position, working octave, and dirty tracking per row. It's
// intentionally a copy of the referenced patterns rather than a pointer
// into m.song.Patterns — reloads triggered by the file-watcher rebuild
// m.song wholesale, and we don't want those to stomp on in-progress edits.
type editorState struct {
	sectionName string
	bars        int
	beatsPerBar int
	resolution  int
	rows        []editorRow
	trackIdx    int
	cellIdx     int
	octave      int // working octave for letter-entered notes; typical 3–5
	dirtyRows   map[int]bool
	message     string // transient status message (save ok, errors, prompts)
	exitConfirm bool   // true when esc pressed on dirty state, awaiting y/n/c
}

// editorRow is one track's editable clip inside a section. patternName is
// the .pat file this row writes back to on save.
type editorRow struct {
	trackID     string
	patternName string
	cells       []song.Cell
}

func (e *editorState) totalCells() int { return e.bars * e.beatsPerBar * e.resolution }
func (e *editorState) dirty() bool     { return len(e.dirtyRows) > 0 }

// enterEditor transitions the Model into edit mode on the section the
// playhead is currently inside. If there is no current section (playhead
// before first bar, or song not loaded) sets lastError and stays in main.
// The track currently selected on the main screen becomes the editor's
// initial cursor row — if the section doesn't contain that track, the
// cursor lands on the first row instead.
func (m Model) enterEditor() Model {
	if m.song == nil {
		m.lastError = "no song loaded"
		return m
	}
	name := m.sectionView
	if name == "" {
		// Fall back: whatever section is under the playhead, else the first
		// arranged section. Lets tests that haven't set sectionView work.
		name = m.position.Section
		if name == "" && len(m.song.Arrangement) > 0 {
			name = m.song.Arrangement[0].Section
		}
	}
	sec, ok := m.song.Sections[name]
	if !ok {
		m.lastError = "no section to edit"
		return m
	}

	rows := buildEditorRows(m.song, sec)
	if len(rows) == 0 {
		m.lastError = "section " + name + " has no parts"
		return m
	}

	trackIdx := 0
	if targetID := m.selectedTrackID(); targetID != "" {
		for i, r := range rows {
			if r.trackID == targetID {
				trackIdx = i
				break
			}
		}
	}

	m.mode = modeEdit
	m.editor = editorState{
		sectionName: name,
		bars:        sec.Bars,
		beatsPerBar: sec.BeatsPerBar,
		resolution:  sec.Resolution,
		rows:        rows,
		trackIdx:    trackIdx,
		octave:      4,
		dirtyRows:   map[int]bool{},
	}
	return m
}

// buildEditorRows builds one editorRow per track with a part in the
// section, ordered by the song's TrackOrder (so rendering matches the
// main view). Tracks not in Section.Parts are omitted.
func buildEditorRows(s *song.Song, sec song.Section) []editorRow {
	order := s.TrackOrder
	if len(order) == 0 {
		order = make([]string, 0, len(sec.Parts))
		for id := range sec.Parts {
			order = append(order, id)
		}
		sort.Strings(order)
	}
	var rows []editorRow
	for _, id := range order {
		patName, ok := sec.Parts[id]
		if !ok {
			continue
		}
		pat, ok := s.Patterns[patName]
		if !ok {
			continue
		}
		rows = append(rows, editorRow{
			trackID:     id,
			patternName: patName,
			cells:       append([]song.Cell(nil), pat.Cells...),
		})
	}
	return rows
}

// updateEditor dispatches key events while in edit mode. Returns the
// (possibly updated) Model and any tea.Cmd for side effects like saving.
func (m Model) updateEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := &m.editor

	// Dirty-exit confirmation takes priority over everything else.
	if ed.exitConfirm {
		switch msg.String() {
		case "y", "Y":
			return m.saveEditorSection(true)
		case "n", "N":
			m.mode = modeSection
			m.editor = editorState{}
			return m, nil
		case "c", "C", "esc":
			ed.exitConfirm = false
			ed.message = ""
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyEsc:
		if ed.dirty() {
			ed.exitConfirm = true
			ed.message = "save changes? (y/n/c)"
			return m, nil
		}
		m.mode = modeSection
		m.editor = editorState{}
		return m, nil

	case tea.KeyLeft:
		if ed.cellIdx > 0 {
			ed.cellIdx--
		}
		return m, nil
	case tea.KeyRight:
		if ed.cellIdx < ed.totalCells()-1 {
			ed.cellIdx++
		}
		return m, nil
	case tea.KeyUp:
		if ed.trackIdx > 0 {
			ed.trackIdx--
		}
		return m, nil
	case tea.KeyDown:
		if ed.trackIdx < len(ed.rows)-1 {
			ed.trackIdx++
		}
		return m, nil
	case tea.KeyHome:
		ed.cellIdx = 0
		return m, nil
	case tea.KeyEnd:
		ed.cellIdx = ed.totalCells() - 1
		return m, nil
	case tea.KeyPgUp:
		step := ed.beatsPerBar * ed.resolution
		ed.cellIdx -= step
		if ed.cellIdx < 0 {
			ed.cellIdx = 0
		}
		return m, nil
	case tea.KeyPgDown:
		step := ed.beatsPerBar * ed.resolution
		ed.cellIdx += step
		if ed.cellIdx >= ed.totalCells() {
			ed.cellIdx = ed.totalCells() - 1
		}
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		m.writeCell(song.Cell{Kind: song.CellRest})
		return m, nil
	case tea.KeyCtrlS:
		return m.saveEditorSection(false)
	}

	switch msg.String() {
	case ".":
		m.writeCell(song.Cell{Kind: song.CellRest})
		return m, nil
	case "-":
		m.writeCell(song.Cell{Kind: song.CellTie})
		return m, nil
	case "X":
		// Bare sample trigger — plays at the track's base pitch.
		m.writeCell(song.Cell{Kind: song.CellSample, Vel: 100})
		return m, m.auditionRowNote(0)
	case "+", "=":
		if ed.octave < 8 {
			ed.octave++
			ed.message = fmt.Sprintf("octave %d", ed.octave)
		}
		return m, nil
	case "_":
		if ed.octave > 0 {
			ed.octave--
			ed.message = fmt.Sprintf("octave %d", ed.octave)
		}
		return m, nil
	}

	// FastTracker-style keymap: lower-octave white keys on Z row
	// (z/x/c/v/b/n/m), black keys on S row (s/d/g/h/j); upper octave
	// on Q row (q/w/e/r/t/y/u) with 2/3/5/6/7 for black keys. Firing
	// a note key both writes the cell and triggers a live audition
	// through the engine so the user hears what they entered.
	if n, ok := trackerKeyToSemitone(msg.String()); ok {
		midi := (ed.octave+1)*12 + n
		if midi >= 0 && midi <= 127 {
			m.writeCell(song.Cell{Kind: song.CellNote, Note: midi, Vel: 100})
			return m, m.auditionRowNote(midi)
		}
	}
	return m, nil
}

// auditionRowNote sends a CmdAudition for the cell the cursor is on.
// note=0 requests a bare sample trigger at the track's base pitch;
// note>0 is a pitched audition (either a MIDI note-on on pitched tracks
// or a transposed sample trigger on sample tracks, handled engine-side).
func (m Model) auditionRowNote(note int) tea.Cmd {
	ed := &m.editor
	if ed.trackIdx < 0 || ed.trackIdx >= len(ed.rows) {
		return nil
	}
	trackID := ed.rows[ed.trackIdx].trackID
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdAudition,
		Track: trackID,
		Note:  note,
		Vel:   100,
	})
}

// writeCell sets the cursor's cell in the active row and marks that row dirty.
func (m *Model) writeCell(c song.Cell) {
	ed := &m.editor
	if ed.trackIdx < 0 || ed.trackIdx >= len(ed.rows) {
		return
	}
	row := &ed.rows[ed.trackIdx]
	if ed.cellIdx < 0 || ed.cellIdx >= len(row.cells) {
		return
	}
	if row.cells[ed.cellIdx] == c {
		return
	}
	row.cells[ed.cellIdx] = c
	if ed.dirtyRows == nil {
		ed.dirtyRows = map[int]bool{}
	}
	ed.dirtyRows[ed.trackIdx] = true
	ed.message = ""
}

// saveEditorSection writes each dirty row back to its .pat file and
// issues an engine reload. If closeAfter is true, exits the editor on success.
func (m Model) saveEditorSection(closeAfter bool) (tea.Model, tea.Cmd) {
	if m.songPath == "" {
		m.editor.message = "no song path — cannot save"
		return m, nil
	}
	patternDir := filepath.Join(filepath.Dir(m.songPath), "patterns")
	saved := 0
	for idx := range m.editor.dirtyRows {
		row := m.editor.rows[idx]
		pat := song.Pattern{
			Name:        row.patternName,
			Bars:        m.editor.bars,
			BeatsPerBar: m.editor.beatsPerBar,
			Resolution:  m.editor.resolution,
			Cells:       row.cells,
		}
		path := filepath.Join(patternDir, row.patternName+".pat")
		if err := song.WritePattern(path, pat); err != nil {
			m.editor.message = "save " + row.patternName + ": " + err.Error()
			return m, nil
		}
		saved++
	}
	m.editor.dirtyRows = map[int]bool{}
	if saved == 0 {
		m.editor.message = "nothing to save"
	} else if saved == 1 {
		m.editor.message = "saved 1 pattern"
	} else {
		m.editor.message = fmt.Sprintf("saved %d patterns", saved)
	}
	if closeAfter {
		m.mode = modeSection
		m.editor = editorState{}
	}
	// File-watcher will pick up the writes and trigger a reload — which
	// rebuilds m.song on the songLoadedMsg path.
	return m, nil
}

// trackerKeyToSemitone maps a single keystroke to a semitone offset
// from the working octave's C, following the FastTracker-II layout
// found in MilkyTracker / OpenMPT / the original Amiga trackers. The
// Z row plus sharps (S/D/G/H/J) covers the working octave; the Q row
// plus sharps (2/3/5/6/7) covers the next octave up. Returns false for
// any other key so the caller can fall through to cell-editing keys.
func trackerKeyToSemitone(s string) (int, bool) {
	switch s {
	// Lower-octave white keys.
	case "z":
		return 0, true
	case "x":
		return 2, true
	case "c":
		return 4, true
	case "v":
		return 5, true
	case "b":
		return 7, true
	case "n":
		return 9, true
	case "m":
		return 11, true
	case ",":
		return 12, true
	// Lower-octave black keys.
	case "s":
		return 1, true
	case "d":
		return 3, true
	case "g":
		return 6, true
	case "h":
		return 8, true
	case "j":
		return 10, true
	// Upper-octave white keys.
	case "q":
		return 12, true
	case "w":
		return 14, true
	case "e":
		return 16, true
	case "r":
		return 17, true
	case "t":
		return 19, true
	case "y":
		return 21, true
	case "u":
		return 23, true
	case "i":
		return 24, true
	// Upper-octave black keys.
	case "2":
		return 13, true
	case "3":
		return 15, true
	case "5":
		return 18, true
	case "6":
		return 20, true
	case "7":
		return 22, true
	}
	return 0, false
}

// --- Rendering -------------------------------------------------------------

var (
	styleEditCursor = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("196")).
			Bold(true)
	styleEditCell      = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleEditRest      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleEditBarLine   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleEditStatus    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleEditDirty     = lipgloss.NewStyle().Foreground(lipgloss.Color("215")).Bold(true)
	styleEditSaved     = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	styleEditMsgPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("228")).Bold(true)
)

// renderEditor draws the horizontal tracker-grid view for the section.
func (m Model) renderEditor() string {
	ed := &m.editor

	rendered := make([][]string, len(ed.rows))
	cellW := 2
	for i, r := range ed.rows {
		row := make([]string, len(r.cells))
		for j, c := range r.cells {
			s := renderEditCell(c)
			if w := lipgloss.Width(s); w > cellW {
				cellW = w
			}
			row[j] = s
		}
		rendered[i] = row
	}

	// Label width: max "trackID/patternName" length + 2 of padding.
	labelW := 4
	for _, r := range ed.rows {
		n := lipgloss.Width(rowLabel(r)) + 2
		if n > labelW {
			labelW = n
		}
	}

	stepsPerBar := ed.beatsPerBar * ed.resolution
	avail := m.width - labelW - 4
	if avail < 20 {
		avail = 20
	}
	cellsVisible := avail / (cellW + 1)
	if cellsVisible < 4 {
		cellsVisible = 4
	}
	startCell := ed.cellIdx - cellsVisible/2
	if startCell < 0 {
		startCell = 0
	}
	endCell := startCell + cellsVisible
	if endCell > ed.totalCells() {
		endCell = ed.totalCells()
		startCell = endCell - cellsVisible
		if startCell < 0 {
			startCell = 0
		}
	}

	var b strings.Builder

	b.WriteString(styleTitle.Render("edit: " + ed.sectionName))
	b.WriteString("    ")
	b.WriteString(styleEditStatus.Render(fmt.Sprintf(
		"bar %d beat %d step %d    track %s    oct %d",
		ed.cellIdx/stepsPerBar+1,
		(ed.cellIdx%stepsPerBar)/ed.resolution+1,
		ed.cellIdx%ed.resolution+1,
		currentRowLabel(ed),
		ed.octave,
	)))
	if ed.dirty() {
		b.WriteString("    ")
		b.WriteString(styleEditDirty.Render(fmt.Sprintf("● DIRTY (%d)", len(ed.dirtyRows))))
	}
	if ed.message != "" {
		b.WriteString("    ")
		if ed.exitConfirm {
			b.WriteString(styleEditMsgPrompt.Render(ed.message))
		} else {
			b.WriteString(styleEditSaved.Render(ed.message))
		}
	}
	b.WriteString("\n\n")

	// Beat-number ruler row.
	b.WriteString(strings.Repeat(" ", labelW))
	for j := startCell; j < endCell; j++ {
		if j > startCell && j%stepsPerBar == 0 {
			b.WriteString(styleEditBarLine.Render("║ "))
		}
		tok := " "
		if j%ed.resolution == 0 {
			tok = fmt.Sprintf("%d", (j%stepsPerBar)/ed.resolution+1)
		}
		b.WriteString(styleEditStatus.Render(padRightVisual(tok, cellW)))
		b.WriteByte(' ')
	}
	b.WriteByte('\n')

	// Track rows.
	for i, r := range ed.rows {
		label := " " + rowLabel(r) + strings.Repeat(" ", labelW-lipgloss.Width(rowLabel(r))-1)
		b.WriteString(styleTrackNm.Render(label))
		for j := startCell; j < endCell; j++ {
			if j > startCell && j%stepsPerBar == 0 {
				b.WriteString(styleEditBarLine.Render("║ "))
			}
			cell := padRightVisual(rendered[i][j], cellW)
			if i == ed.trackIdx && j == ed.cellIdx {
				b.WriteString(styleEditCursor.Render(cell))
			} else if r.cells[j].Kind == song.CellRest || r.cells[j].Kind == song.CellTie {
				b.WriteString(styleEditRest.Render(cell))
			} else {
				b.WriteString(styleEditCell.Render(cell))
			}
			b.WriteByte(' ')
		}
		b.WriteByte('\n')
	}

	b.WriteString("\n")
	if ed.exitConfirm {
		b.WriteString(styleHelp.Render("    y: save & exit    n: discard    c/esc: cancel"))
	} else {
		b.WriteString(styleHelp.Render(
			"    ← → ↑ ↓: nav    pgup/pgdn: ±bar    home/end: ends    z/s/x/d...: notes (Z row lower oct, Q row upper)    .: rest    -: tie    X: sample    +/_: octave    ctrl+s: save    esc: back"))
	}
	b.WriteByte('\n')
	return b.String()
}

// rowLabel is the left-hand column shown for a row: "<trackID> <patternName>"
// if the pattern name differs from the track id, else just the track id.
// Keeps the editor's identity clear when patterns are named per section
// (e.g. track "bass" playing pattern "verse-bass" in section "verse").
func rowLabel(r editorRow) string {
	if r.patternName == r.trackID {
		return r.trackID
	}
	return r.trackID + " " + r.patternName
}

func currentRowLabel(ed *editorState) string {
	if ed.trackIdx < 0 || ed.trackIdx >= len(ed.rows) {
		return "?"
	}
	return ed.rows[ed.trackIdx].trackID
}

// renderEditCell is the display form of a cell inside the editor — short
// and legible at a glance. Distinct from writepattern.formatCell (which
// produces the file-level token) so the editor can use · for rests etc.
func renderEditCell(c song.Cell) string {
	switch c.Kind {
	case song.CellRest:
		return "·"
	case song.CellTie:
		return "-"
	case song.CellSample:
		return "X"
	case song.CellNote:
		return song.FormatPitch(c.Note)
	}
	return "?"
}

// padRightVisual right-pads s with spaces so it occupies w display columns.
// Byte-based padRight mis-handles multi-byte glyphs like "·" (U+00B7, 2
// bytes but 1 column), which throws track-row cells out of alignment with
// the beat-number ruler.
func padRightVisual(s string, w int) string {
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}
