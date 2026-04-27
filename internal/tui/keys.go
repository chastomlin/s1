package tui

import (
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// ppqnTicksPerBeat mirrors engine.PPQN so the TUI can convert a
// transport Position to a pattern cell index without taking a
// dependency on the engine package. Keep in sync with engine.PPQN.
const ppqnTicksPerBeat = 96

// updateTransportKey handles keys that work in any non-editor mode: quit,
// transport, seek, tempo, reload, loop, load, edit-toml. Returns the
// updated model, any command to fire, and a handled flag. Mode-specific
// handlers call this first and only fall through when it returns
// handled=false.
func (m Model) updateTransportKey(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "ctrl+c":
		// Defer quitting behind a y/n confirm so stray keystrokes
		// don't drop the session. The actual teardown (closing conn
		// + watcher + flushing the record buffer) happens in
		// updateConfirmQuit once the user confirms.
		m.prompt = promptState{kind: promptConfirmQuit}
		return m, nil, true
	case " ":
		var cmd protocol.Command
		if m.state == protocol.StatePlaying {
			cmd = protocol.Command{Cmd: protocol.CmdStop}
		} else {
			cmd = protocol.Command{Cmd: protocol.CmdPlay}
		}
		return m, sendCommand(m.conn, cmd), true
	case "p":
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdPause}), true
	case "r":
		return m, tea.Batch(
			sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload}),
			loadSongLocal(m.songPath),
		), true
	case "left":
		bar := max(1, m.position.Bar-1)
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdSeek, Bar: bar, Beat: 1}), true
	case "right":
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdSeek, Bar: m.position.Bar + 1, Beat: 1}), true
	case "home":
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdSeek, Bar: 1, Beat: 1}), true
	case "t":
		if m.songPath == "" {
			m.lastError = "no song loaded — press l to load first"
			return m, nil, true
		}
		m.prompt = promptState{kind: promptTempo}
		return m, nil, true
	case "l":
		m.prompt = promptState{kind: promptLoad}
		return m, nil, true
	case "L":
		// Toggle loop mode. If enabling with no bounds set, default to
		// a 4-bar loop starting at the playhead (or bar 1 if stopped).
		if !m.loopEnabled && m.loopToBar <= m.loopFromBar {
			from := m.position.Bar
			if from < 1 {
				from = 1
			}
			m.loopFromBar = from
			m.loopToBar = from + 4
		}
		m.loopEnabled = !m.loopEnabled
		return m, m.sendLoop(), true
	case "[":
		bar := m.position.Bar
		if bar < 1 {
			bar = 1
		}
		m.loopFromBar = bar
		if m.loopToBar <= m.loopFromBar {
			m.loopToBar = m.loopFromBar + 1
		}
		return m, m.sendLoop(), true
	case "]":
		bar := m.position.Bar
		if bar < 1 {
			bar = 1
		}
		m.loopToBar = bar + 1
		if m.loopFromBar >= m.loopToBar {
			m.loopFromBar = m.loopToBar - 1
		}
		if m.loopFromBar < 1 {
			m.loopFromBar = 1
		}
		return m, m.sendLoop(), true
	case "e":
		if m.songPath == "" {
			m.lastError = "no song path — launch with --song <path> first"
			return m, nil, true
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, m.songPath)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			if err != nil {
				return engineErrorMsg{err}
			}
			// On a clean exit, file-watch will emit any reload it needs.
			// But also proactively reload in case the editor didn't
			// actually write (harmless) or the watcher missed the event.
			return fileChangedMsg{path: "(editor exit)"}
		}), true
	}
	return m, nil, false
}

// updateArrangement handles keys in the song-level arrangement view.
// Selection cursor moves with ↑/↓; enter drills into the selected
// section; n opens the new-section wizard; d removes the slot from the
// arrangement (the [[sections]] library entry stays); , / . reorder;
// + / - bump the repeat shorthand.
func (m Model) updateArrangement(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if out, cmd, handled := m.updateTransportKey(msg); handled {
		return out, cmd
	}
	// No song yet → only "n" (new song) and "l" (load) make sense.
	if m.song == nil {
		switch msg.String() {
		case "n":
			m.prompt = promptState{kind: promptNewSongPath}
			return m, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "up", "k", "left":
		if m.arrangementIdx > 0 {
			m.arrangementIdx--
		}
		return m, nil
	case "down", "j", "right":
		if n := len(m.song.Arrangement); m.arrangementIdx < n-1 {
			m.arrangementIdx++
		}
		return m, nil
	case "enter":
		if len(m.song.Arrangement) == 0 || m.arrangementIdx >= len(m.song.Arrangement) {
			return m, nil
		}
		m.sectionView = m.song.Arrangement[m.arrangementIdx].Section
		m.selectedTrackIdx = 0
		m.mode = modeSection
		// Seek the engine to the start of this slot so pressing play
		// auditions the section you drilled into, not bar 1 of the song.
		startBar := m.slotStartBar(m.arrangementIdx)
		return m, tea.Batch(m.syncMidiInTrack(), sendCommand(m.conn, protocol.Command{
			Cmd:  protocol.CmdSeek,
			Bar:  startBar,
			Beat: 1,
		}))
	case "n":
		m.prompt = promptState{kind: promptNewSectionName}
		return m, nil
	case "d":
		if m.songPath == "" || len(m.song.Arrangement) == 0 {
			return m, nil
		}
		if err := song.RemoveArrangementSlot(m.songPath, m.arrangementIdx); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		// Clamp cursor — the old idx may now be past the end.
		if m.arrangementIdx > 0 && m.arrangementIdx >= len(m.song.Arrangement)-1 {
			m.arrangementIdx = len(m.song.Arrangement) - 2
			if m.arrangementIdx < 0 {
				m.arrangementIdx = 0
			}
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	case ",":
		if m.songPath == "" || m.arrangementIdx == 0 {
			return m, nil
		}
		if err := song.MoveArrangementSlot(m.songPath, m.arrangementIdx, m.arrangementIdx-1); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		m.arrangementIdx--
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	case ".":
		if m.songPath == "" || m.arrangementIdx >= len(m.song.Arrangement)-1 {
			return m, nil
		}
		if err := song.MoveArrangementSlot(m.songPath, m.arrangementIdx, m.arrangementIdx+1); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		m.arrangementIdx++
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	case "+", "=":
		if m.songPath == "" || m.arrangementIdx >= len(m.song.Arrangement) {
			return m, nil
		}
		slot := m.song.Arrangement[m.arrangementIdx]
		if err := song.SetArrangementRepeat(m.songPath, m.arrangementIdx, slot.Repeat+1); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	case "-", "_":
		if m.songPath == "" || m.arrangementIdx >= len(m.song.Arrangement) {
			return m, nil
		}
		slot := m.song.Arrangement[m.arrangementIdx]
		if slot.Repeat <= 1 {
			return m, nil
		}
		if err := song.SetArrangementRepeat(m.songPath, m.arrangementIdx, slot.Repeat-1); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	}
	return m, nil
}

// updateSection handles keys in the section-scoped tracks view. ↑/↓
// select a track; enter opens the pattern editor; esc returns to the
// arrangement view; m/s toggle mute/solo; a runs the section-scoped
// add-track wizard (creates a [[tracks]] + a blank pattern + a parts
// entry in one flow). ` (backtick) toggles Live monitoring on the
// selected track; R toggles Record-arm.
func (m Model) updateSection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()
	// When Live is on, tracker keys take precedence — the keyboard
	// becomes a piano. +/_ still shift the working octave. Non-tracker
	// keys (arrows, esc, space, enter, `, R, …) fall through below so
	// navigation and transport keep working while holding "instrument".
	if m.liveEnabled {
		if n, ok := trackerKeyToSemitone(s); ok {
			return m.liveAudition(n)
		}
		switch s {
		case "+", "=":
			if m.liveOctave < 8 {
				m.liveOctave++
			}
			return m, nil
		case "_":
			if m.liveOctave > 0 {
				m.liveOctave--
			}
			return m, nil
		}
	}
	// Live / Rec toggles fire regardless of current state. Rec is
	// silently ignored unless Live is also armed — you can't record
	// without monitoring.
	switch s {
	case "`":
		return m.toggleLive()
	case "R":
		return m.toggleRec()
	}
	// Intercept space in section view so stop parks the playhead at
	// the viewed section's start (the engine's CmdStop otherwise rewinds
	// to song bar 1). With this, space→space cycles the section you're
	// viewing instead of dropping you back at the top of the arrangement.
	// Stop/seek/play are sent via sendCommands (sequential on a single
	// goroutine) — tea.Batch would race them and land Stop after Seek.
	if s == " " {
		startBar := m.slotStartBar(m.arrangementIdx)
		if m.state == protocol.StatePlaying {
			return m, sendCommands(m.conn,
				protocol.Command{Cmd: protocol.CmdStop},
				protocol.Command{Cmd: protocol.CmdSeek, Bar: startBar, Beat: 1},
			)
		}
		// Stopped / paused → play. If the engine is currently parked
		// outside this section, nudge back to the section's start
		// first so playback auditions what the user is looking at.
		if m.position.Bar < startBar || m.position.Bar >= startBar+m.slotLengthBars(m.arrangementIdx) {
			return m, sendCommands(m.conn,
				protocol.Command{Cmd: protocol.CmdSeek, Bar: startBar, Beat: 1},
				protocol.Command{Cmd: protocol.CmdPlay},
			)
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdPlay})
	}
	if out, cmd, handled := m.updateTransportKey(msg); handled {
		return out, cmd
	}
	switch s {
	case "esc":
		// Leaving section view — drop Live/Rec and flush any pending
		// edits so they don't linger invisibly into the arrangement view.
		m.liveEnabled = false
		m.recordArmed = false
		m.flushRecordBuffer()
		m.mode = modeArrangement
		m.sectionView = ""
		return m, nil
	case "enter":
		m = m.enterEditor()
		return m, nil
	case "up", "k":
		if m.selectedTrackIdx > 0 {
			m.selectedTrackIdx--
		}
		return m, m.syncMidiInTrack()
	case "down", "j":
		if n := len(m.displayTracks()); m.selectedTrackIdx < n-1 {
			m.selectedTrackIdx++
		}
		return m, m.syncMidiInTrack()
	case "m":
		id := m.selectedTrackID()
		if id == "" {
			return m, nil
		}
		muted := !m.mutes[id]
		if muted {
			m.mutes[id] = true
		} else {
			delete(m.mutes, id)
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdMute, Track: id, Muted: muted})
	case "s":
		id := m.selectedTrackID()
		if id == "" {
			return m, nil
		}
		soloed := !m.solos[id]
		if soloed {
			m.solos[id] = true
		} else {
			delete(m.solos, id)
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdSolo, Track: id, Soloed: soloed})
	case "a":
		if m.songPath == "" {
			m.lastError = "no song loaded"
			return m, nil
		}
		// Add-track wizard — in section view, the resulting track is
		// automatically bound to the current section (see executeAddTrack).
		m.prompt = promptState{kind: promptAddTrackKind}
		return m, nil
	case "d":
		// Narrow delete: drop the selected track from this section's parts.
		// The [[tracks]] block and the pattern file on disk stay put, so
		// the track can be re-added to this (or another) section later.
		id := m.selectedTrackID()
		if id == "" || m.songPath == "" {
			return m, nil
		}
		if err := song.RemoveSectionPart(m.songPath, m.sectionView, id); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		if m.selectedTrackIdx > 0 {
			m.selectedTrackIdx--
		}
		return m, tea.Batch(
			sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload}),
			m.syncMidiInTrack(),
		)
	case "D":
		// Broad delete — gate behind a y/n confirm since it hits [[tracks]]
		// plus every section that referenced this track.
		id := m.selectedTrackID()
		if id == "" || m.songPath == "" {
			return m, nil
		}
		m.prompt = promptState{
			kind:  promptConfirmDeleteTrack,
			value: id, // stash the target so the confirm handler knows it
		}
		return m, nil
	case "f":
		// FX / mix view — enter the per-track mix editor for the
		// selected sample track. Pitched MIDI tracks bail with an
		// error message inside enterMix.
		m = m.enterMix()
		return m, nil
	}
	return m, nil
}

// toggleLive flips live-monitoring mode. Turning it off also disarms
// recording and flushes any buffered edits to disk.
func (m Model) toggleLive() (tea.Model, tea.Cmd) {
	if m.liveEnabled {
		m.liveEnabled = false
		m.recordArmed = false
		m.flushRecordBuffer()
		return m, nil
	}
	m.liveEnabled = true
	if m.liveOctave == 0 {
		m.liveOctave = 4
	}
	return m, nil
}

// toggleRec flips record-arm, but only when Live is already on — record
// always implies monitoring. Disarming flushes pending edits.
func (m Model) toggleRec() (tea.Model, tea.Cmd) {
	if !m.liveEnabled {
		return m, nil
	}
	if m.recordArmed {
		m.recordArmed = false
		m.flushRecordBuffer()
	} else {
		m.recordArmed = true
	}
	return m, nil
}

// liveAudition fires a CmdAudition on the selected track at the given
// semitone offset from the working octave. When the transport is
// playing, the playhead is inside the viewed section, and record is
// armed, the note is also appended to the track's pattern (additive:
// rest cells are filled, cells already carrying a note/sample are
// left alone so loop-record stays non-destructive).
func (m Model) liveAudition(semitone int) (tea.Model, tea.Cmd) {
	trackID := m.selectedTrackID()
	if trackID == "" {
		return m, nil
	}
	midi := (m.liveOctave+1)*12 + semitone
	if midi < 0 || midi > 127 {
		return m, nil
	}
	if m.recordArmed && m.state == protocol.StatePlaying && m.position.Section == m.sectionView {
		m.recordNoteAtPlayhead(trackID, midi)
	}
	return m, sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdAudition,
		Track: trackID,
		Note:  midi,
		Vel:   100,
	})
}

// recordNoteAtPlayhead writes the note into the in-memory record buffer
// at the cell the playhead is currently on. Additive: only rest cells
// are filled, so looping over a section during record extends what's
// there without overwriting earlier passes.
func (m *Model) recordNoteAtPlayhead(trackID string, note int) {
	if m.song == nil {
		return
	}
	sec, ok := m.song.Sections[m.sectionView]
	if !ok {
		return
	}
	patName, ok := sec.Parts[trackID]
	if !ok {
		return
	}
	if m.recordBuffer == nil {
		m.recordBuffer = map[string]*song.Pattern{}
	}
	pat, buffered := m.recordBuffer[patName]
	if !buffered {
		base, ok := m.song.Patterns[patName]
		if !ok {
			return
		}
		p := base
		p.Cells = append([]song.Cell(nil), base.Cells...)
		pat = &p
		m.recordBuffer[patName] = pat
	}
	idx := cellIdxAtPlayhead(m.position, m.sectionAbsStartBar(), sec)
	if idx < 0 || idx >= len(pat.Cells) {
		return
	}
	if pat.Cells[idx].Kind != song.CellRest {
		return
	}
	pat.Cells[idx] = song.Cell{Kind: song.CellNote, Note: note, Vel: 100}
}

// flushRecordBuffer writes each buffered pattern to disk and clears
// the buffer. Called when record is disarmed, the transport stops,
// Live is turned off, or the user leaves the section view. Errors
// populate lastError; the buffer is always cleared so we don't write
// the same edits twice.
func (m *Model) flushRecordBuffer() {
	if len(m.recordBuffer) == 0 {
		return
	}
	dir := filepath.Join(filepath.Dir(m.songPath), "patterns")
	for name, pat := range m.recordBuffer {
		path := filepath.Join(dir, name+".pat")
		if err := song.WritePattern(path, *pat); err != nil {
			m.lastError = "record flush " + name + ": " + err.Error()
		}
	}
	m.recordBuffer = nil
}

// cellIdxAtPlayhead converts a transport Position (1-based bar/beat +
// tick within beat) into the 0-based cell index inside sec's pattern.
// Returns -1 when the playhead is before the section start. Wraps
// modulo sec.Bars so a Repeat>1 slot records into the same pattern on
// every pass.
func cellIdxAtPlayhead(pos protocol.Event, sectionStartBar int, sec song.Section) int {
	if sectionStartBar == 0 || sec.Bars <= 0 || sec.BeatsPerBar <= 0 || sec.Resolution <= 0 {
		return -1
	}
	barInSlot := pos.Bar - sectionStartBar
	if barInSlot < 0 {
		return -1
	}
	barInSection := barInSlot % sec.Bars
	beatInBar := pos.Beat - 1
	if beatInBar < 0 {
		beatInBar = 0
	}
	ticksPerCell := ppqnTicksPerBeat / sec.Resolution
	subBeat := 0
	if ticksPerCell > 0 {
		subBeat = pos.Tick / ticksPerCell
	}
	return barInSection*sec.BeatsPerBar*sec.Resolution + beatInBar*sec.Resolution + subBeat
}

// displayTracks returns the ordered list of track IDs to render in the
// current view. In modeSection it's just the tracks bound by the
// section's parts, in TrackOrder order. In modeArrangement it's all
// declared tracks.
func (m Model) displayTracks() []string {
	if m.song == nil {
		return nil
	}
	if m.mode == modeSection && m.sectionView != "" {
		sec, ok := m.song.Sections[m.sectionView]
		if !ok {
			return nil
		}
		var out []string
		for _, id := range m.song.TrackOrder {
			if _, bound := sec.Parts[id]; bound {
				out = append(out, id)
			}
		}
		return out
	}
	if len(m.song.TrackOrder) > 0 {
		return m.song.TrackOrder
	}
	return uniqueTrackIDs(m.song)
}

// sectionAbsStartBar returns the 1-based bar at which the currently-
// arranged section (under m.arrangementIdx) begins in the song timeline,
// or 0 if no section is viewed. Used by the section view's playhead
// alignment.
func (m Model) sectionAbsStartBar() int {
	if m.song == nil || m.sectionView == "" {
		return 0
	}
	bar := 1
	for _, slot := range m.song.Arrangement {
		sec, ok := m.song.Sections[slot.Section]
		if !ok {
			continue
		}
		if slot.Section == m.sectionView {
			return bar
		}
		bar += sec.Bars * slot.Repeat
	}
	return 0
}

// slotStartBar returns the 1-based absolute song bar at which the idx-th
// arrangement slot starts. Sums the spans (bars × repeat) of all
// preceding slots. Returns 1 for idx <= 0 or out-of-range idx.
func (m Model) slotStartBar(idx int) int {
	if m.song == nil || idx <= 0 {
		return 1
	}
	bar := 1
	for i := 0; i < idx && i < len(m.song.Arrangement); i++ {
		prev := m.song.Arrangement[i]
		s, ok := m.song.Sections[prev.Section]
		if !ok {
			continue
		}
		bar += s.Bars * prev.Repeat
	}
	return bar
}

// slotLengthBars returns the span in bars of the idx-th arrangement
// slot (section.Bars × slot.Repeat). Zero when the slot or its section
// can't be found.
func (m Model) slotLengthBars(idx int) int {
	if m.song == nil || idx < 0 || idx >= len(m.song.Arrangement) {
		return 0
	}
	slot := m.song.Arrangement[idx]
	sec, ok := m.song.Sections[slot.Section]
	if !ok {
		return 0
	}
	r := slot.Repeat
	if r < 1 {
		r = 1
	}
	return sec.Bars * r
}

// currentSlotIndex returns the arrangement-slot index the playhead is
// currently inside (0-based), or -1 if the transport isn't playing or
// the playhead is outside the arranged range. Disambiguates which of
// multiple slots referencing the same section is active — unlike
// EvPosition.Section, which only carries the section name.
func (m Model) currentSlotIndex() int {
	if m.song == nil || m.position.Bar <= 0 {
		return -1
	}
	bar := 1
	for i, slot := range m.song.Arrangement {
		sec, ok := m.song.Sections[slot.Section]
		if !ok {
			continue
		}
		end := bar + sec.Bars*slot.Repeat
		if m.position.Bar >= bar && m.position.Bar < end {
			return i
		}
		bar = end
	}
	return -1
}

