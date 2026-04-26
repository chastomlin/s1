// Package tui is the bubbletea front-end that connects to the engine over a
// Unix socket and renders a Sequencer-One-style tracks-first main view.
package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fsnotify/fsnotify"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// flashDuration is how long a track row stays highlighted after a note-on
// or sample trigger. 150ms feels lively without being distracting.
const flashDuration = 150 * time.Millisecond

// redrawInterval is the period of background redraws so flashes decay even
// when no new events arrive. 60ms ≈ 16 fps, plenty for an ASCII UI.
const redrawInterval = 60 * time.Millisecond

// --- Messages ---------------------------------------------------------------

type engineEventMsg protocol.Event
type engineErrorMsg struct{ err error }
type songLoadedMsg struct{ s *song.Song }
type decayTickMsg time.Time

// --- Model ------------------------------------------------------------------

type Model struct {
	conn     net.Conn
	songPath string // set via --song flag; cached so we can reload locally for rendering

	song      *song.Song
	state     protocol.TransportState
	bpm       int
	position  protocol.Event
	lastError string

	// flash timestamps keyed by pattern track ID. Empty map is fine.
	flash map[string]time.Time

	// Meter readings — last snapshot received from seqoned at ~20 Hz.
	// Master is the post-master-gain L/R; meterTracks holds per-track
	// L/R for tracks the engine is reporting (silent tracks may be
	// absent from the map and render as zero).
	meterMaster protocol.MeterReadings
	meterTracks map[string]protocol.MeterReadings

	// watcher monitors song.toml and the patterns directory for external
	// edits. Nil when no song path was provided.
	watcher *fsnotify.Watcher

	// lastSelfEdit is set by mix-view apply paths just before they
	// rewrite song.toml. The fileChangedMsg handler swallows the
	// would-be-redundant engine reload when this is recent — the engine
	// already has the change in memory (we sent the Cmd directly), and a
	// reload would emit AllOff and cut in-flight voices. External edits
	// (no recent self-edit) still trigger a full reload.
	lastSelfEdit time.Time

	// prompt holds state for the load-song input prompt opened by "l".
	prompt promptState

	// mode + editor drive the pattern-edit sub-view entered via Enter.
	mode   uiMode
	editor editorState
	mix    mixState

	// arrangementIdx is the cursor on the arrangement ribbon in
	// modeArrangement. Enter drills into the section at this slot.
	arrangementIdx int

	// sectionView is the section currently being viewed in modeSection
	// (set when the user drills in from the arrangement). Empty in
	// modeArrangement.
	sectionView string

	// selectedTrackIdx indexes into displayTracks() on the section screen.
	// Navigated with ↑/↓; carried into the editor on Enter.
	selectedTrackIdx int

	// Live-play state on the section view. liveEnabled routes the tracker
	// keymap to CmdAudition on the currently-selected track; recordArmed
	// additionally buffers the keyed notes into the track's pattern at
	// the playhead, flushing to disk when rec is disarmed, the transport
	// stops, live is turned off, or the user exits section mode.
	liveEnabled  bool
	recordArmed  bool
	liveOctave   int
	recordBuffer map[string]*song.Pattern // pattern name -> in-memory edits

	// mutes / solos mirror the engine's per-track routing state so the UI
	// can render indicators. The engine is authoritative; these are our
	// best guess based on the commands we've sent.
	mutes map[string]bool
	solos map[string]bool

	// Loop mode mirror (engine is authoritative). loopToBar is exclusive —
	// i.e. "[from, to)" bars are inside the loop.
	loopEnabled bool
	loopFromBar int
	loopToBar   int

	width, height int
}

func New(conn net.Conn, songPath string) Model {
	w, err := startWatcher(songPath)
	m := Model{
		conn:       conn,
		songPath:   songPath,
		state:      protocol.StateStopped,
		bpm:        120,
		flash:      map[string]time.Time{},
		mutes:      map[string]bool{},
		solos:      map[string]bool{},
		watcher:    w,
		liveOctave: 4, // default working octave for live-audition mode
	}
	if err != nil {
		m.lastError = "watcher: " + err.Error()
	}
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		listenEngine(m.conn),
		scheduleDecay(),
	}
	if m.songPath != "" {
		cmds = append(cmds,
			sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdLoad, Path: m.songPath}),
			loadSongLocal(m.songPath),
		)
	}
	if m.watcher != nil {
		cmds = append(cmds, awaitFileChange(m.watcher))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.prompt.active() {
			return m.updatePrompt(msg)
		}
		switch m.mode {
		case modeEdit:
			return m.updateEditor(msg)
		case modeMix:
			return m.updateMix(msg)
		case modeArrangement:
			return m.updateArrangement(msg)
		case modeSection:
			return m.updateSection(msg)
		}

	case engineEventMsg:
		ev := protocol.Event(msg)
		switch ev.Event {
		case protocol.EvState:
			m.state = ev.State
			if ev.State == protocol.StateStopped {
				// Transport stopping ends any in-progress live recording —
				// flush so the .pat file has the partial take.
				m.flushRecordBuffer()
			}
		case protocol.EvPosition:
			m.position = ev
		case protocol.EvLoaded:
			// Nothing to update locally; songLoadedMsg brings the parsed Song.
		case protocol.EvNote:
			if ev.Track != "" && (ev.NoteKind == protocol.NoteOn || ev.NoteKind == protocol.SampleTrigger) {
				m.flash[ev.Track] = time.Now()
			}
		case protocol.EvMeter:
			m.meterMaster = ev.Master
			m.meterTracks = ev.MeterTracks
		case protocol.EvError:
			m.lastError = ev.Msg
		}
		return m, listenEngine(m.conn)

	case engineErrorMsg:
		m.lastError = "engine: " + msg.err.Error()
		return m, nil

	case songLoadedMsg:
		m.song = msg.s
		if msg.s != nil {
			m.bpm = msg.s.Project.BPM
		}
		// Clamp track selection in case a reload removed tracks.
		if n := len(m.displayTracks()); m.selectedTrackIdx >= n {
			if n > 0 {
				m.selectedTrackIdx = n - 1
			} else {
				m.selectedTrackIdx = 0
			}
		}
		// Refresh the mix view's working copy after a reload — picks
		// up external edits to song.toml and keeps the displayed
		// values in sync with what the engine sees. If the track is
		// gone, drop back to the section view.
		if m.mode == modeMix && msg.s != nil {
			if t, ok := msg.s.Tracks[m.mix.trackID]; ok {
				m.mix.track = t
			} else {
				m.mode = modeSection
				m.mix = mixState{}
			}
		}
		return m, nil

	case decayTickMsg:
		// Tick alone triggers a re-render so flashes visibly decay.
		return m, scheduleDecay()

	case fileChangedMsg:
		// Always refresh our own copy of the song so the renderer sees
		// the latest values. Whether we ALSO ask the engine to reload
		// depends on whether this looks like an external edit: when the
		// TUI itself just wrote (mix-view tweaks, etc.) the engine has
		// already been told via a direct Cmd, so a CmdReload here would
		// just trigger an unnecessary AllOff and stop in-flight voices.
		cmds := []tea.Cmd{loadSongLocal(m.songPath)}
		if time.Since(m.lastSelfEdit) > 500*time.Millisecond {
			cmds = append(cmds, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload}))
		}
		if m.watcher != nil {
			cmds = append(cmds, awaitFileChange(m.watcher))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// --- Commands ---------------------------------------------------------------

func listenEngine(conn net.Conn) tea.Cmd {
	return func() tea.Msg {
		var ev protocol.Event
		dec := newLineDecoder(conn)
		if err := dec.Decode(&ev); err != nil {
			return engineErrorMsg{err}
		}
		return engineEventMsg(ev)
	}
}

func sendCommand(conn net.Conn, c protocol.Command) tea.Cmd {
	return func() tea.Msg {
		if err := protocol.WriteCommand(conn, c); err != nil {
			return engineErrorMsg{err}
		}
		return nil
	}
}

// sendCommands writes a sequence of commands to the engine in order on
// a single goroutine. Use this when the commands' effects depend on
// ordering (stop-then-seek, seek-then-play) — tea.Batch runs Cmds in
// separate goroutines, so two sendCommand calls in a Batch can arrive
// at the engine in any order.
func sendCommands(conn net.Conn, cmds ...protocol.Command) tea.Cmd {
	return func() tea.Msg {
		for _, c := range cmds {
			if err := protocol.WriteCommand(conn, c); err != nil {
				return engineErrorMsg{err}
			}
		}
		return nil
	}
}

func loadSongLocal(path string) tea.Cmd {
	return func() tea.Msg {
		if path == "" {
			return nil
		}
		s, err := song.Load(path)
		if err != nil {
			return engineErrorMsg{err}
		}
		return songLoadedMsg{s: s}
	}
}

func scheduleDecay() tea.Cmd {
	return tea.Tick(redrawInterval, func(t time.Time) tea.Msg {
		return decayTickMsg(t)
	})
}

// awaitFileChange blocks until the watcher emits a filesystem event, then
// returns a single fileChangedMsg. The Update handler re-issues this
// command to keep the pump going. Editor saves typically produce a burst
// of events (write + chmod or create-rename); successive reloads are cheap
// so we don't bother debouncing.
func awaitFileChange(w *fsnotify.Watcher) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return nil // watcher closed
				}
				// Skip CHMOD-only events — they're noisy and rarely indicate
				// content change. Treat WRITE, CREATE, RENAME, REMOVE as
				// potentially interesting.
				if ev.Op == fsnotify.Chmod {
					continue
				}
				return fileChangedMsg{path: ev.Name}
			case err, ok := <-w.Errors:
				if !ok {
					return nil
				}
				return engineErrorMsg{err}
			}
		}
	}
}

// --- Styles ----------------------------------------------------------------

var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))
	styleHeader   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleColHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true)
	styleActive   = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	styleInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleBlock    = lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	styleBlockLo  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	styleTrackNo  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleTrackCh  = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	styleTrackNm  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleFlashHot = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleFlashDim = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stylePlayhead = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)

	// Selection highlight bar for the focused track row on the main screen.
	// Applied per-segment rather than wrapping the row — nested lipgloss
	// styles reset the background when inner styles reset colors.
	colorTrackSelBg = lipgloss.Color("238")
	styleSelPad     = lipgloss.NewStyle().Background(colorTrackSelBg)
	// Mute ("M") and solo ("S") indicators.
	styleMute = lipgloss.NewStyle().Foreground(lipgloss.Color("215")).Bold(true)
	styleSolo = lipgloss.NewStyle().Foreground(lipgloss.Color("118")).Bold(true)
	styleOff  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	// Loop-enabled badge in the transport strip.
	styleLoopOn = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)

	// Live/Rec badges in the section-view header.
	styleLiveBadge = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	styleRecBadge  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)

	// Amiga-Workbench-inspired chrome: blue + white title bar around an
	// outer window border. The hue choice leans closer to Workbench 1.x
	// blue (~#0055AA) rather than the later gray palette.
	colorAmigaBlue      = lipgloss.Color("25")  // dark Amiga blue
	colorAmigaBlueLight = lipgloss.Color("33")  // brighter blue (border/accent)
	colorAmigaWhite     = lipgloss.Color("231") // bright white
	colorAmigaAccent    = lipgloss.Color("117") // light blue accent

	styleAppBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAmigaBlueLight).
			Padding(0, 1)

	// styleLogoBar is the blue title-bar stripe. Width is set at render
	// time to fill the available content width inside the border.
	styleLogoBar = lipgloss.NewStyle().
			Background(colorAmigaBlue).
			Foreground(colorAmigaWhite).
			Bold(true).
			Align(lipgloss.Center)
	styleLogoAccent = lipgloss.NewStyle().
			Background(colorAmigaBlue).
			Foreground(colorAmigaAccent).
			Bold(true)
)

// --- Rendering --------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 {
		return "initializing..."
	}
	var body string
	if m.mode == modeEdit {
		body = m.renderEditor()
	} else {
		body = m.renderMainBody()
	}
	content := m.renderLogo() + "\n" + body
	return styleAppBorder.Render(strings.TrimRight(content, "\n"))
}

// renderLogo is the blue-and-white Amiga-Workbench-style title bar shown
// at the top of every mode. Fills the available width inside the border
// and centers a "SEQUENCER 1" banner with decorative shading blocks.
func (m Model) renderLogo() string {
	// Border adds 2 chars each side; Padding(0, 1) adds another 2. So
	// the inside of the logo bar is m.width - 4.
	innerW := m.width - 4
	if innerW < 20 {
		innerW = 20
	}
	banner := styleLogoAccent.Render("░▒▓█") +
		styleLogoBar.Render(" SEQUENCER 1 ") +
		styleLogoAccent.Render("█▓▒░")
	// Width() on a style forces the final rendered line to that length;
	// centering handles the padding inside the blue stripe. Render an
	// empty string with the stripe background to paint the full bar,
	// then overlay the banner by centering via styleLogoBar.Width().
	return styleLogoBar.Width(innerW).Render(
		// Sandwich the accent shading against a central label. The
		// outer style fills the rest of the line with blue bg.
		banner,
	)
}

// renderMainBody assembles the non-editor body: the song-identity line,
// the mode-specific content (arrangement ribbon or section tracks),
// the transport strip, any active prompt, the last error, and the help
// line. renderEditor produces its own body.
func (m Model) renderMainBody() string {
	var b strings.Builder

	title := "seqone"
	if m.song != nil && m.song.Project.Title != "" {
		title = "seqone — " + m.song.Project.Title
	}
	b.WriteString(styleTitle.Render(title))
	b.WriteString("\n")
	if m.songPath != "" {
		b.WriteString(styleHeader.Render("  " + m.songPath))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if m.song == nil {
		b.WriteString(m.renderEmptyState())
	} else {
		switch m.mode {
		case modeArrangement:
			b.WriteString(m.renderArrangementBody())
		case modeSection:
			b.WriteString(m.renderSectionBody())
		case modeMix:
			b.WriteString(m.renderMixBody())
		}
	}
	b.WriteString("\n")

	b.WriteString(m.renderTransport())
	b.WriteString("\n")

	if m.prompt.active() {
		b.WriteString(m.renderPrompt())
	}

	if m.lastError != "" {
		b.WriteString(styleErr.Render("  ! " + m.lastError))
		b.WriteString("\n")
	}

	b.WriteString(m.renderHelp())
	return b.String()
}

// renderHelp produces the per-mode hint line. Transport keys appear in
// every mode; mode-specific keys are listed first for discoverability.
func (m Model) renderHelp() string {
	common := "space: play/stop   p: pause   ←/→: seek   [: loop-in   ]: loop-out   L: loop   t: tempo…   l: load…   r: reload   e: edit toml   q: quit"
	var body string
	switch {
	case m.song == nil:
		body = "n: new song    l: load song    q: quit"
	case m.mode == modeArrangement:
		body = "↑/↓: select   enter: open section   n: new section   d: remove slot   , / .: reorder   +/-: repeat\n  " + common
	case m.mode == modeSection:
		body = "↑/↓: select   enter: edit pattern   f: fx/mix   a: add   d: drop   D: delete   m/s: mute/solo   `: live   R: rec   esc: back\n  " + common
	case m.mode == modeMix:
		body = "↑/↓: select param   ←/→: adjust (shift = coarse)   esc: back to section\n  " + common
	default:
		body = common
	}
	return styleHelp.Render("  " + body)
}

// renderPrompt draws the inline input — load, tempo, or add-track wizard —
// along with any tab-completion candidates and transient status message.
func (m Model) renderPrompt() string {
	label, hint := m.promptLabels()
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styleColHead.Render("  " + label + ": "))
	b.WriteString(styleTrackNm.Render(m.prompt.value))
	b.WriteString(stylePlayhead.Render("▎"))
	if m.prompt.message != "" {
		b.WriteString("   ")
		b.WriteString(styleErr.Render(m.prompt.message))
	}
	b.WriteString("\n")
	if len(m.prompt.suggest) > 0 {
		const maxShown = 12
		show := m.prompt.suggest
		more := 0
		if len(show) > maxShown {
			more = len(show) - maxShown
			show = show[:maxShown]
		}
		b.WriteString("    ")
		b.WriteString(styleInactive.Render(strings.Join(show, "  ")))
		if more > 0 {
			b.WriteString(styleInactive.Render(fmt.Sprintf("  … (+%d more)", more)))
		}
		b.WriteString("\n")
	}
	b.WriteString(styleHelp.Render("    " + hint))
	b.WriteString("\n")
	return b.String()
}

// promptLabels returns the line-one label and the line-two hint shown
// under the active prompt.
func (m Model) promptLabels() (label, hint string) {
	switch m.prompt.kind {
	case promptLoad:
		return "load song", "enter: load   tab: complete   esc: cancel"
	case promptTempo:
		return "tempo (bpm, 20-400)", "enter: set   esc: cancel"
	case promptAddTrackKind:
		return "add track — (s)ample or (i)nstrument", "s / i   esc: cancel"
	case promptAddSampleTrack:
		step := addSampleSteps[m.prompt.step]
		return "new sample track — " + step.label, "enter: next   esc: cancel"
	case promptAddInstrTrack:
		step := addInstrSteps[m.prompt.step]
		return "new instrument track — " + step.label, "enter: next   esc: cancel"
	case promptNewSongPath:
		step := newSongSteps[m.prompt.step]
		return step.label, "enter: next   esc: cancel"
	case promptNewSectionName:
		step := newSectionSteps[m.prompt.step]
		return step.label, "enter: next   esc: cancel"
	case promptConfirmDeleteTrack:
		return fmt.Sprintf("delete track %q from the whole song?", m.prompt.value),
			"y: delete   n / esc: cancel"
	case promptConfirmQuit:
		return "quit seqone?", "y: quit   n / esc: cancel"
	}
	return "", ""
}

// updateConfirmQuit handles the y/n gate on exit. Any existing pending
// state (record buffer, open conn, file watcher) is torn down here so
// the bubbletea Run loop's worker goroutines unblock cleanly — same
// teardown the old inline quit path ran.
func (m Model) updateConfirmQuit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.prompt = promptState{}
		m.flushRecordBuffer()
		if m.conn != nil {
			_ = m.conn.Close()
		}
		if m.watcher != nil {
			_ = m.watcher.Close()
			m.watcher = nil
		}
		return m, tea.Quit
	case "n", "N", "esc":
		m.prompt = promptState{}
		return m, nil
	}
	return m, nil
}

// updateConfirmDeleteTrack handles the y/n gate for DeleteTrack. The
// target track ID is stashed in prompt.value when the prompt is opened.
func (m Model) updateConfirmDeleteTrack(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		trackID := m.prompt.value
		m.prompt = promptState{}
		if trackID == "" || m.songPath == "" {
			return m, nil
		}
		if err := song.DeleteTrack(m.songPath, trackID); err != nil {
			m.lastError = err.Error()
			return m, nil
		}
		if m.selectedTrackIdx > 0 {
			m.selectedTrackIdx--
		}
		return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdReload})
	case "n", "N", "esc":
		m.prompt = promptState{}
		return m, nil
	}
	return m, nil
}


// sendLoop pushes the current loop state to the engine.
func (m Model) sendLoop() tea.Cmd {
	return sendCommand(m.conn, protocol.Command{
		Cmd:     protocol.CmdLoop,
		FromBar: m.loopFromBar,
		ToBar:   m.loopToBar,
		Enabled: m.loopEnabled,
	})
}

// selectedTrackID returns the ID of the row currently highlighted on the
// main screen, or "" if there are no tracks.
func (m Model) selectedTrackID() string {
	ids := m.displayTracks()
	if m.selectedTrackIdx < 0 || m.selectedTrackIdx >= len(ids) {
		return ""
	}
	return ids[m.selectedTrackIdx]
}

// trackDisplayName prefers the [[tracks]].name, falls back to the raw ID.
func (m Model) trackDisplayName(id string) string {
	if m.song == nil {
		return id
	}
	if t, ok := m.song.Tracks[id]; ok && t.Name != "" {
		return t.Name
	}
	return id
}

// channelLabel returns the MIDI channel as a string, or blank if the track
// has no binding.
func (m Model) channelLabel(id string) string {
	if m.song == nil {
		return ""
	}
	if t, ok := m.song.Tracks[id]; ok {
		return fmt.Sprintf("%d", t.Channel)
	}
	return "—"
}

// updatePrompt dispatches keystrokes for whatever prompt kind is active.
// Esc is a universal cancel; everything else routes to a kind-specific
// handler. Enter triggers each handler's commit step.
func (m Model) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc {
		m.prompt = promptState{}
		return m, nil
	}
	// Kind chooser is special: a single keystroke picks a sub-wizard.
	if m.prompt.kind == promptAddTrackKind {
		return m.updateAddTrackKindChooser(msg)
	}
	// Confirm-delete is also a single-keystroke gate (y/n/esc).
	if m.prompt.kind == promptConfirmDeleteTrack {
		return m.updateConfirmDeleteTrack(msg)
	}
	if m.prompt.kind == promptConfirmQuit {
		return m.updateConfirmQuit(msg)
	}

	// For text-input prompts: common editing keys first.
	switch msg.Type {
	case tea.KeyBackspace:
		if len(m.prompt.value) > 0 {
			r := []rune(m.prompt.value)
			m.prompt.value = string(r[:len(r)-1])
		}
		m.prompt.suggest = nil
		m.prompt.message = ""
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		if len(msg.Runes) > 0 {
			m.prompt.value += string(msg.Runes)
			m.prompt.suggest = nil
			m.prompt.message = ""
		}
		return m, nil
	}

	switch m.prompt.kind {
	case promptLoad:
		return m.updateLoadPrompt(msg)
	case promptTempo:
		return m.updateTempoPrompt(msg)
	case promptAddSampleTrack, promptAddInstrTrack:
		return m.updateAddTrackWizard(msg)
	case promptNewSongPath:
		return m.updateNewSongWizard(msg)
	case promptNewSectionName:
		return m.updateNewSectionWizard(msg)
	}
	return m, nil
}

// updateLoadPrompt implements Tab completion + Enter to load.
func (m Model) updateLoadPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyTab:
		m.prompt = completePath(m.prompt)
		return m, nil
	case tea.KeyEnter:
		path, err := resolveSongPath(m.prompt.value)
		if err != nil {
			m.prompt.message = err.Error()
			return m, nil
		}
		m.prompt = promptState{}
		if m.watcher != nil {
			_ = m.watcher.Close()
			m.watcher = nil
		}
		w, werr := startWatcher(path)
		if werr != nil {
			m.lastError = "watcher: " + werr.Error()
		}
		m.watcher = w
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
	return m, nil
}

// --- Kind chooser for add-track ---

// updateAddTrackKindChooser reads a single 's' or 'i' keystroke to select
// the sub-wizard, or any other key leaves the prompt as-is with a hint.
func (m Model) updateAddTrackKindChooser(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(msg.Runes) == 0 {
		return m, nil
	}
	switch msg.Runes[0] {
	case 's', 'S':
		m.prompt = promptState{kind: promptAddSampleTrack}
		return m, nil
	case 'i', 'I':
		m.prompt = promptState{kind: promptAddInstrTrack}
		return m, nil
	}
	m.prompt.message = "press s for sample or i for instrument (esc to cancel)"
	return m, nil
}

// --- Tempo prompt ---

func (m Model) updateTempoPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyEnter {
		return m, nil
	}
	bpm, err := strconv.Atoi(strings.TrimSpace(m.prompt.value))
	if err != nil {
		m.prompt.message = "enter an integer"
		return m, nil
	}
	if err := song.SetBPM(m.songPath, bpm); err != nil {
		m.prompt.message = err.Error()
		return m, nil
	}
	m.prompt = promptState{}
	// File watcher will fire a reload. We also issue an immediate
	// CmdSetTempo so the change is audible without waiting for reload.
	return m, sendCommand(m.conn, protocol.Command{Cmd: protocol.CmdSetTempo, BPM: bpm})
}

// --- Add-track wizard ---

// addTrackStep describes one question in the add-track wizard.
type addTrackStep struct {
	label    string
	validate func(answer string, m Model) error
}

// addSampleSteps collects: id, relative sample path, MIDI note.
var addSampleSteps = []addTrackStep{
	{
		label: "track id",
		validate: func(s string, m Model) error {
			if s == "" {
				return fmt.Errorf("id required")
			}
			return checkTrackIDUnique(s, m)
		},
	},
	{
		label: "sample path (relative to song dir)",
		validate: func(s string, _ Model) error {
			if s == "" {
				return fmt.Errorf("path required")
			}
			return nil
		},
	},
	{
		label: "midi note (0-127)",
		validate: func(s string, _ Model) error {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > 127 {
				return fmt.Errorf("note must be 0..127")
			}
			return nil
		},
	},
}

// addInstrSteps collects: id, MIDI channel (1-16), GM program (0-127).
var addInstrSteps = []addTrackStep{
	{
		label: "track id",
		validate: func(s string, m Model) error {
			if s == "" {
				return fmt.Errorf("id required")
			}
			return checkTrackIDUnique(s, m)
		},
	},
	{
		label: "midi channel (1-16)",
		validate: func(s string, _ Model) error {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 16 {
				return fmt.Errorf("channel must be 1..16")
			}
			return nil
		},
	},
	{
		label: "gm program (0-127)",
		validate: func(s string, _ Model) error {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > 127 {
				return fmt.Errorf("program must be 0..127")
			}
			return nil
		},
	},
}

// checkTrackIDUnique rejects an id already used by a track, sample, or
// instrument in the currently-loaded song — otherwise the Load would fail.
func checkTrackIDUnique(id string, m Model) error {
	if m.song == nil {
		return nil
	}
	if _, dup := m.song.Tracks[id]; dup {
		return fmt.Errorf("track %q already exists", id)
	}
	if _, dup := m.song.Samples[id]; dup {
		return fmt.Errorf("sample %q already exists", id)
	}
	if _, dup := m.song.Instruments[id]; dup {
		return fmt.Errorf("instrument %q already exists", id)
	}
	return nil
}

func (m Model) updateAddTrackWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type != tea.KeyEnter {
		return m, nil
	}
	steps := addSampleSteps
	if m.prompt.kind == promptAddInstrTrack {
		steps = addInstrSteps
	}
	answer := strings.TrimSpace(m.prompt.value)
	if err := steps[m.prompt.step].validate(answer, m); err != nil {
		m.prompt.message = err.Error()
		return m, nil
	}
	m.prompt.answers = append(m.prompt.answers, answer)
	m.prompt.value = ""
	m.prompt.step++
	if m.prompt.step < len(steps) {
		return m, nil
	}
	// All answers collected — execute.
	err := m.executeAddTrack()
	if err != nil {
		m.prompt.message = err.Error()
		// Keep the wizard open on the last step so the user can retry
		// (rare — this is usually a TOML write failure, not validation).
		m.prompt.step--
		m.prompt.answers = m.prompt.answers[:len(m.prompt.answers)-1]
		return m, nil
	}
	// In section view, also create a starter pattern and bind the new
	// track to the current section's parts.
	trackID := m.prompt.answers[0]
	m.prompt = promptState{}
	if m.mode == modeSection && m.sectionView != "" {
		if err := m.executeSectionScopedAddTrack(trackID); err != nil {
			m.lastError = "bind to section: " + err.Error()
		}
	}
	// File watcher reload will refresh the Song; no extra command needed.
	return m, nil
}

func (m Model) executeAddTrack() error {
	a := m.prompt.answers
	switch m.prompt.kind {
	case promptAddSampleTrack:
		note, _ := strconv.Atoi(a[2])
		return song.AppendSampleTrack(m.songPath, a[0], a[1], note)
	case promptAddInstrTrack:
		ch, _ := strconv.Atoi(a[1])
		prog, _ := strconv.Atoi(a[2])
		return song.AppendInstrumentTrack(m.songPath, a[0], ch, prog)
	}
	return fmt.Errorf("unknown wizard kind")
}

func (m Model) renderTransport() string {
	var indicator string
	switch m.state {
	case protocol.StatePlaying:
		indicator = styleActive.Render("▶ playing")
	case protocol.StatePaused:
		indicator = styleHeader.Render("‖ paused")
	default:
		indicator = styleInactive.Render("■ stopped")
	}

	pos, section := m.transportBarAndSection()
	loop := m.renderLoopBadge()
	// Master L/R meter: 8 chars per channel, stereo, peak-program
	// overlay. Lives on the same row as the transport indicator so the
	// visual centre of the screen always carries the playback signal.
	master := "   " + styleColHead.Render("L") + " " +
		renderMeter(m.meterMaster.LL, m.meterMaster.LP, 8) + "  " +
		styleColHead.Render("R") + " " +
		renderMeter(m.meterMaster.RL, m.meterMaster.RP, 8)
	return fmt.Sprintf("  %s   bar %s   bpm %d   section: %s%s%s",
		indicator, pos, m.bpm, section, master, loop)
}

// transportBarAndSection renders the "bar" and "section" parts of the
// transport strip. In the arrangement (song-level) and editor views,
// these are absolute bars of the whole song timeline. In section view
// they localise: "03:02" is bar 3 beat 2 *within the viewed section*
// (so a 4-bar section always reads 01..04 regardless of where in the
// arrangement it sits), with a "pass N/M" suffix on repeated slots.
// Seek / loop commands still operate on absolute bars under the hood —
// only the display is localised.
func (m Model) transportBarAndSection() (pos, section string) {
	pos = "—"
	section = m.position.Section
	if section == "" {
		section = "—"
	}
	if m.position.Bar <= 0 {
		return pos, section
	}

	if m.mode == modeSection && m.sectionView != "" && m.song != nil {
		if localBar, pass, repeat, ok := m.localizeToSection(); ok {
			pos = fmt.Sprintf("%02d:%02d", localBar, m.position.Beat)
			if repeat > 1 {
				section = fmt.Sprintf("%s (pass %d/%d)", m.sectionView, pass, repeat)
			} else {
				section = m.sectionView
			}
			return pos, section
		}
	}

	pos = fmt.Sprintf("%03d:%02d", m.position.Bar, m.position.Beat)
	return pos, section
}

// localizeToSection converts the absolute playhead bar to a 1-based
// bar index inside m.sectionView plus the current pass index when the
// playhead is in a repeated slot. Finds the specific arrangement slot
// the playhead is in (not just the first reference to the section)
// so repeated sections like verse-verse-verse localise correctly
// regardless of which slot is playing. Returns ok=false when the
// playhead is in a different section entirely — callers fall back
// to absolute display.
func (m Model) localizeToSection() (localBar, pass, repeat int, ok bool) {
	if m.song == nil {
		return 0, 0, 0, false
	}
	sec, secOK := m.song.Sections[m.sectionView]
	if !secOK || sec.Bars <= 0 {
		return 0, 0, 0, false
	}
	idx := m.currentSlotIndex()
	if idx < 0 {
		return 0, 0, 0, false
	}
	slot := m.song.Arrangement[idx]
	if slot.Section != m.sectionView {
		// Playhead is in another section — show absolute bar.
		return 0, 0, 0, false
	}
	barInSlot := m.position.Bar - m.slotStartBar(idx)
	if barInSlot < 0 {
		return 0, 0, 0, false
	}
	repeat = slot.Repeat
	if repeat < 1 {
		repeat = 1
	}
	localBar = (barInSlot % sec.Bars) + 1
	pass = (barInSlot / sec.Bars) + 1
	return localBar, pass, repeat, true
}

// renderLoopBadge returns the loop status suffix, or "" when no loop has
// been configured. "⟲ 3..6" when on, "loop 3..6 off" when set but disabled.
func (m Model) renderLoopBadge() string {
	if m.loopToBar <= m.loopFromBar {
		return ""
	}
	// loopToBar is exclusive — display as an inclusive range (to-1) which
	// is what users expect from "loop bars 3..6".
	last := m.loopToBar - 1
	body := fmt.Sprintf("%d..%d", m.loopFromBar, last)
	if m.loopEnabled {
		return "   " + styleLoopOn.Render("⟲ "+body)
	}
	return "   " + styleInactive.Render("loop "+body+" off")
}

// --- Helpers ---------------------------------------------------------------

func uniqueTrackIDs(s *song.Song) []string {
	seen := map[string]bool{}
	var out []string
	for _, slot := range s.Arrangement {
		sec, ok := s.Sections[slot.Section]
		if !ok {
			continue
		}
		for id := range sec.Parts {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// sectionHasTrackActivity reports whether the given section plays any
// non-rest cell on trackID. Returns false if the track has no part in the
// section or the referenced pattern can't be found.
func sectionHasTrackActivity(s *song.Song, sec song.Section, trackID string) bool {
	patName, ok := sec.Parts[trackID]
	if !ok {
		return false
	}
	pat, ok := s.Patterns[patName]
	if !ok {
		return false
	}
	for _, c := range pat.Cells {
		if c.Kind == song.CellNote || c.Kind == song.CellSample {
			return true
		}
	}
	return false
}

// sectionTrackActivity is the Model-aware variant that consults the
// in-progress record buffer before falling back to the loaded song's
// patterns — so a note recorded in the current take lights up the
// activity row immediately.
func (m Model) sectionTrackActivity(sec song.Section, trackID string) bool {
	patName, ok := sec.Parts[trackID]
	if !ok {
		return false
	}
	if pat, ok := m.recordBuffer[patName]; ok && pat != nil {
		for _, c := range pat.Cells {
			if c.Kind == song.CellNote || c.Kind == song.CellSample {
				return true
			}
		}
		return false
	}
	return sectionHasTrackActivity(m.song, sec, trackID)
}

func centerTrunc(s string, width int) string {
	if len(s) >= width {
		if width <= 0 {
			return ""
		}
		return s[:width]
	}
	pad := width - len(s)
	l := pad / 2
	r := pad - l
	return strings.Repeat(" ", l) + s + strings.Repeat(" ", r)
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return strings.Repeat(" ", width-len(s)) + s
}
