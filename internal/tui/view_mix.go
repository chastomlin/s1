package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// styleEffectOn / styleEffectOff colour the dot prefix on each
// effect-section header — green ●  when the effect is in the chain,
// dim ○  when bypassed. Matches the meter palette so the "active"
// colour is consistent across the TUI.
var (
	styleEffectOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("82")).Bold(true)
	styleEffectOff = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// mixState owns the working copy of the track being edited plus
// cursor + transient message. The track value is a snapshot that
// flushes to the engine + TOML on every change; reloads driven by
// the file watcher refresh it via syncFromSong so external edits
// propagate without stomping in-progress dragging.
type mixState struct {
	trackID  string
	track    song.Track
	paramIdx int
	message  string
}

// enterMix opens the mix view for the currently-selected track in
// the section view. Sample tracks only — the EQ/compressor chain
// only routes sample triggers; pitched MIDI tracks are out of scope.
func (m Model) enterMix() Model {
	if m.song == nil {
		m.lastError = "no song loaded"
		return m
	}
	id := m.selectedTrackID()
	if id == "" {
		m.lastError = "no track selected"
		return m
	}
	t, ok := m.song.Tracks[id]
	if !ok {
		m.lastError = "track not in song: " + id
		return m
	}
	if t.Sample == "" {
		m.lastError = "mix view is for sample tracks (selected: " + id + ")"
		return m
	}
	m.mode = modeMix
	m.mix = mixState{
		trackID: id,
		track:   t,
	}
	return m
}

// updateMix dispatches keys while the mix view is active.
func (m Model) updateMix(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	mx := &m.mix
	switch msg.String() {
	case "esc", "q":
		// q here doesn't open the quit prompt — esc-feel is more
		// natural for "back out of the mix view".
		m.mode = modeSection
		m.mix = mixState{}
		return m, nil
	case "up", "k":
		if mx.paramIdx > 0 {
			mx.paramIdx--
		}
		return m, nil
	case "down", "j":
		if mx.paramIdx < len(mixParams)-1 {
			mx.paramIdx++
		}
		return m, nil
	case "left", "h":
		return m.adjustMix(-1, false)
	case "right", "l":
		return m.adjustMix(+1, false)
	case "shift+left", "H":
		return m.adjustMix(-1, true)
	case "shift+right", "L":
		return m.adjustMix(+1, true)
	}
	return m, nil
}

// adjustMix nudges the currently-selected parameter by one step (or
// big-step when shift was held), pushes the change to the engine,
// and persists it to song.toml. Errors land in the mix state's
// message so the user sees what failed.
func (m Model) adjustMix(direction int, big bool) (tea.Model, tea.Cmd) {
	mx := &m.mix
	if mx.paramIdx < 0 || mx.paramIdx >= len(mixParams) {
		return m, nil
	}
	p := mixParams[mx.paramIdx]
	step := p.step
	if big {
		step = p.bigStep
	}
	cur := p.get(&mx.track)
	next := cur + float32(direction)*step
	if next < p.min {
		next = p.min
	} else if next > p.max {
		next = p.max
	}
	if next == cur {
		return m, nil
	}
	cmd, err := p.apply(&m, &mx.track, next)
	if err != nil {
		mx.message = err.Error()
		return m, nil
	}
	mx.message = ""
	return m, cmd
}

// renderMixBody draws the parameter list. Rows live in named sections
// (EQ / FILTER / LOFI / COMPRESSOR) plus an unsectioned gain+pan
// preamble. Each section header is itself a toggleable row: a green
// dot and bright label when the effect is enabled, dim when bypassed.
// Parameter rows under a disabled section render in the muted palette
// so the eye can tell at a glance what is and isn't in the signal.
func (m Model) renderMixBody() string {
	mx := &m.mix
	if mx.trackID == "" {
		return styleInactive.Render("  no track selected\n")
	}
	var b strings.Builder
	disp := mx.trackID
	if t, ok := m.song.Tracks[mx.trackID]; ok && t.Name != "" {
		disp = t.Name
	}
	b.WriteString(styleColHead.Render(fmt.Sprintf("  mix: %s  (sample track)", disp)))
	b.WriteString("\n\n")

	for i, p := range mixParams {
		isCursor := i == mx.paramIdx
		on := effectEnabled(&mx.track, p.effect)
		if p.separator != "" {
			b.WriteString(renderEffectHeader(p.separator, on, isCursor))
			b.WriteByte('\n')
			continue
		}
		b.WriteString(renderParamRow(p, mx, on, isCursor))
		b.WriteByte('\n')
	}

	if mx.message != "" {
		b.WriteString("\n")
		b.WriteString(styleErr.Render("  ! " + mx.message))
		b.WriteString("\n")
	}
	return b.String()
}

// renderEffectHeader formats one section header line. The dot is
// green when on, dim circle when bypassed; the cursor marker (▶) is
// bright when the section's enabled, dimmed otherwise so the colour
// tells you the bypass state at a glance even with the cursor on it.
func renderEffectHeader(name string, on, isCursor bool) string {
	var dot, label string
	if on {
		dot = styleEffectOn.Render("●")
		label = styleColHead.Render(name)
	} else {
		dot = styleEffectOff.Render("○")
		label = styleInactive.Render(name)
	}
	marker := "  "
	if isCursor {
		if on {
			marker = styleArrPlaying.Render("▶ ")
		} else {
			marker = styleInactive.Render("▶ ")
		}
	}
	return marker + dot + " " + label
}

// renderParamRow formats one parameter line. enabled=false greys both
// the label and value so a glance tells you "this isn't doing anything
// right now"; the cursor still highlights the row, but in the dim
// palette to match.
func renderParamRow(p mixParam, mx *mixState, enabled, isCursor bool) string {
	var marker, label, value string
	formatted := p.format(p.get(&mx.track))
	switch {
	case !enabled && isCursor:
		marker = styleInactive.Render("▶ ")
		label = styleInactive.Render(padRight(p.label, 14))
		value = styleInactive.Render(formatted)
	case !enabled:
		marker = "  "
		label = styleInactive.Render(padRight(p.label, 14))
		value = styleInactive.Render(formatted)
	case isCursor:
		marker = "▶ "
		label = styleArrPlaying.Render(padRight(p.label, 14))
		value = formatted
	default:
		marker = "  "
		label = styleArrIdle.Render(padRight(p.label, 14))
		value = formatted
	}
	return marker + label + "  " + value
}

// effectEnabled reports whether the parameter group `eff` is currently
// active for this track. Empty `eff` is reserved for unsectioned rows
// (gain, pan) which are always considered on.
func effectEnabled(t *song.Track, eff string) bool {
	switch eff {
	case "eq":
		return t.EQ.Enabled
	case "drive":
		return t.Drive.Enabled
	case "filter":
		return t.Filter.Enabled
	case "lofi":
		return t.Lofi.Enabled
	case "comp":
		return t.Comp.Enabled
	case "reverb":
		return t.Reverb.Enabled
	}
	return true
}

// mixParam describes one editable parameter row. label is what
// renders in the left column; format produces the right-side string;
// get/apply carry the working-copy mutation + engine push. step /
// bigStep are added/subtracted on ← / shift-←. min/max are the
// clamp range — kept aligned with the loader's range checks.
//
// Section header rows reuse the same struct: separator names the
// section, effect tags the group it belongs to, and get/apply read /
// write the section's Enabled flag (modelled as 0/1 to slot into the
// adjustMix slider mechanic). Param rows tag effect so the renderer
// knows whether to grey them out when the section is bypassed.
type mixParam struct {
	label     string
	separator string // when non-empty this row is a section header
	effect    string // "eq" / "filter" / "lofi" / "comp"; "" = always-on
	get       func(*song.Track) float32
	apply     func(m *Model, t *song.Track, v float32) (tea.Cmd, error)
	step      float32
	bigStep   float32
	min, max  float32
	format    func(float32) string
}

// mixParams is the canonical row order for the mix view. The first
// two are always-on (gain, pan); the remaining four sections each
// start with a header row whose left/right toggles the section's
// Enabled flag — that's why every header carries get/apply and an
// effect tag, not just the separator string.
var mixParams = []mixParam{
	{
		label: "gain",
		get:   func(t *song.Track) float32 { return t.Gain },
		apply: applyGain,
		step:  0.05, bigStep: 0.2, min: 0, max: 4,
		format: func(v float32) string { return fmt.Sprintf("%.2f×", v) },
	},
	{
		label: "pan",
		get:   func(t *song.Track) float32 { return t.Pan },
		apply: applyPan,
		step:  0.05, bigStep: 0.2, min: -1, max: 1,
		format: func(v float32) string {
			switch {
			case v < -0.001:
				return fmt.Sprintf("%.2f L", -v)
			case v > 0.001:
				return fmt.Sprintf("%.2f R", v)
			default:
				return "centre"
			}
		},
	},
	{
		separator: "EQ", effect: "eq",
		get: func(t *song.Track) float32 {
			if t.EQ.Enabled {
				return 1
			}
			return 0
		},
		apply: applyEQEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "low_freq", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.LowFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.LowFreq }),
		step:  10, bigStep: 100, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "low_gain", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.LowGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.LowGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{
		label: "mid_freq", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.MidFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidFreq }),
		step:  20, bigStep: 200, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "mid_q", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.MidQ },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidQ }),
		step:  0.05, bigStep: 0.5, min: 0.1, max: 10,
		format: func(v float32) string { return fmt.Sprintf("Q %.2f", v) },
	},
	{
		label: "mid_gain", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.MidGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{
		label: "high_freq", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.HighFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.HighFreq }),
		step:  50, bigStep: 500, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "high_gain", effect: "eq",
		get:   func(t *song.Track) float32 { return t.EQ.HighGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.HighGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{
		separator: "DRIVE", effect: "drive",
		get: func(t *song.Track) float32 {
			if t.Drive.Enabled {
				return 1
			}
			return 0
		},
		apply: applyDriveEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "type", effect: "drive",
		get:   func(t *song.Track) float32 { return float32(driveTypeIndex(t.Drive.Type)) },
		apply: applyDriveType,
		step:  1, bigStep: 1, min: 0, max: 2,
		format: driveTypeName,
	},
	{
		label: "drive", effect: "drive",
		get:   func(t *song.Track) float32 { return t.Drive.Drive },
		apply: applyDrive(func(t *song.Track) *float32 { return &t.Drive.Drive }),
		step:  0.02, bigStep: 0.1, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
	{
		label: "tone", effect: "drive",
		get:   func(t *song.Track) float32 { return t.Drive.Tone },
		apply: applyDrive(func(t *song.Track) *float32 { return &t.Drive.Tone }),
		step:  0.02, bigStep: 0.1, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
	{
		label: "level", effect: "drive",
		get:   func(t *song.Track) float32 { return t.Drive.Level },
		apply: applyDrive(func(t *song.Track) *float32 { return &t.Drive.Level }),
		step:  0.05, bigStep: 0.2, min: 0, max: 2,
		format: func(v float32) string { return fmt.Sprintf("%.2f×", v) },
	},
	{
		separator: "FILTER", effect: "filter",
		get: func(t *song.Track) float32 {
			if t.Filter.Enabled {
				return 1
			}
			return 0
		},
		apply: applyFilterEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "type", effect: "filter",
		get:   func(t *song.Track) float32 { return float32(filterTypeIndex(t.Filter.Type)) },
		apply: applyFilterType,
		step:  1, bigStep: 1, min: 0, max: 2,
		format: filterTypeName,
	},
	{
		label: "cutoff", effect: "filter",
		get:   func(t *song.Track) float32 { return t.Filter.Cutoff },
		apply: applyFilter(func(t *song.Track) *float32 { return &t.Filter.Cutoff }),
		step:  20, bigStep: 200, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "resonance", effect: "filter",
		get:   func(t *song.Track) float32 { return t.Filter.Resonance },
		apply: applyFilter(func(t *song.Track) *float32 { return &t.Filter.Resonance }),
		step:  0.05, bigStep: 0.2, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
	{
		separator: "LOFI", effect: "lofi",
		get: func(t *song.Track) float32 {
			if t.Lofi.Enabled {
				return 1
			}
			return 0
		},
		apply: applyLofiEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "bits", effect: "lofi",
		get:   func(t *song.Track) float32 { return float32(t.Lofi.Bits) },
		apply: applyLofiBits,
		step:  1, bigStep: 4, min: 1, max: 16,
		format: func(v float32) string { return fmt.Sprintf("%d-bit", int(v)) },
	},
	{
		label: "rate", effect: "lofi",
		get:   func(t *song.Track) float32 { return t.Lofi.Rate },
		apply: applyLofi(func(t *song.Track) *float32 { return &t.Lofi.Rate }),
		step:  500, bigStep: 2000, min: 1000, max: 48000,
		format: func(v float32) string { return fmt.Sprintf("%5.0f Hz", v) },
	},
	{
		separator: "COMPRESSOR", effect: "comp",
		get: func(t *song.Track) float32 {
			if t.Comp.Enabled {
				return 1
			}
			return 0
		},
		apply: applyCompEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "threshold", effect: "comp",
		get:   func(t *song.Track) float32 { return t.Comp.ThresholdDB },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.ThresholdDB }),
		step:  0.5, bigStep: 3, min: -60, max: 0,
		format: dbFormat,
	},
	{
		label: "ratio", effect: "comp",
		get:   func(t *song.Track) float32 { return t.Comp.Ratio },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.Ratio }),
		step:  0.1, bigStep: 1, min: 1, max: 20,
		format: func(v float32) string { return fmt.Sprintf("%.1f:1", v) },
	},
	{
		label: "attack", effect: "comp",
		get:   func(t *song.Track) float32 { return t.Comp.AttackMs },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.AttackMs }),
		step:  1, bigStep: 10, min: 0.1, max: 500,
		format: func(v float32) string { return fmt.Sprintf("%.1f ms", v) },
	},
	{
		label: "release", effect: "comp",
		get:   func(t *song.Track) float32 { return t.Comp.ReleaseMs },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.ReleaseMs }),
		step:  5, bigStep: 50, min: 1, max: 5000,
		format: func(v float32) string { return fmt.Sprintf("%.0f ms", v) },
	},
	{
		label: "makeup", effect: "comp",
		get:   func(t *song.Track) float32 { return t.Comp.MakeupDB },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.MakeupDB }),
		step:  0.5, bigStep: 3, min: -12, max: 24,
		format: dbFormat,
	},
	{
		separator: "REVERB", effect: "reverb",
		get: func(t *song.Track) float32 {
			if t.Reverb.Enabled {
				return 1
			}
			return 0
		},
		apply: applyReverbEnabled,
		step:  1, bigStep: 1, min: 0, max: 1,
	},
	{
		label: "size", effect: "reverb",
		get:   func(t *song.Track) float32 { return t.Reverb.Size },
		apply: applyReverb(func(t *song.Track) *float32 { return &t.Reverb.Size }),
		step:  0.02, bigStep: 0.1, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
	{
		label: "damping", effect: "reverb",
		get:   func(t *song.Track) float32 { return t.Reverb.Damping },
		apply: applyReverb(func(t *song.Track) *float32 { return &t.Reverb.Damping }),
		step:  0.02, bigStep: 0.1, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
	{
		label: "mix", effect: "reverb",
		get:   func(t *song.Track) float32 { return t.Reverb.Mix },
		apply: applyReverb(func(t *song.Track) *float32 { return &t.Reverb.Mix }),
		step:  0.02, bigStep: 0.1, min: 0, max: 1,
		format: func(v float32) string { return fmt.Sprintf("%.2f", v) },
	},
}

func dbFormat(v float32) string {
	if v == 0 {
		return "0 dB"
	}
	return fmt.Sprintf("%+.1f dB", v)
}

// applyGain mutates the working copy and pushes a CmdSetTrackGain
// to the engine + persists via song.SetTrackGain.
func applyGain(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Gain = v
	if m.songPath != "" {
		if err := song.SetTrackGain(m.songPath, m.mix.trackID, v); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackGain,
		Track: m.mix.trackID,
		Gain:  v,
	}), nil
}

func applyPan(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Pan = v
	if m.songPath != "" {
		if err := song.SetTrackPan(m.songPath, m.mix.trackID, v); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackPan,
		Track: m.mix.trackID,
		Pan:   v,
	}), nil
}

// applyEQ returns an apply function that mutates one EQ field via the
// supplied accessor, then pushes the whole EQ block to the engine.
// EQ params are tied (the engine command takes the full struct), so
// every sub-parameter change resends all values including Enabled.
func applyEQ(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushEQ(m, t)
	}
}

// applyEQEnabled flips the EQ bypass flag. v is 0 (off) or 1 (on)
// from the bypass-row slider; we treat anything > 0 as enable.
func applyEQEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.EQ.Enabled = v > 0
	return pushEQ(m, t)
}

// pushEQ persists the track's EQ to song.toml and pushes a CmdSetTrackEQ
// to the engine. Centralised so the band-tweak path and the bypass
// toggle both do exactly the same write.
func pushEQ(m *Model, t *song.Track) (tea.Cmd, error) {
	if m.songPath != "" {
		if err := song.SetTrackEQ(m.songPath, m.mix.trackID, t.EQ); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackEQ,
		Track: m.mix.trackID,
		EQ: &protocol.EQConfigCmd{
			Enabled: t.EQ.Enabled,
			LowFreq: t.EQ.LowFreq, LowGain: t.EQ.LowGain,
			MidFreq: t.EQ.MidFreq, MidQ: t.EQ.MidQ, MidGain: t.EQ.MidGain,
			HighFreq: t.EQ.HighFreq, HighGain: t.EQ.HighGain,
		},
	}), nil
}

// applyComp is the comp counterpart to applyEQ — sends the full comp
// block on every sub-param tweak.
func applyComp(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushComp(m, t)
	}
}

// applyCompEnabled flips the compressor bypass flag.
func applyCompEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Comp.Enabled = v > 0
	return pushComp(m, t)
}

func pushComp(m *Model, t *song.Track) (tea.Cmd, error) {
	if m.songPath != "" {
		if err := song.SetTrackComp(m.songPath, m.mix.trackID, t.Comp); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackComp,
		Track: m.mix.trackID,
		Comp: &protocol.CompConfigCmd{
			Enabled:     t.Comp.Enabled,
			ThresholdDB: t.Comp.ThresholdDB,
			Ratio:       t.Comp.Ratio,
			AttackMs:    t.Comp.AttackMs,
			ReleaseMs:   t.Comp.ReleaseMs,
			MakeupDB:    t.Comp.MakeupDB,
		},
	}), nil
}

// filterTypeIndex maps the song-side enum to the slider index used by
// the TUI's "type" row. Unknown values fall back to lowpass (0).
func filterTypeIndex(t song.FilterType) int {
	switch t {
	case song.FilterHighpass:
		return 1
	case song.FilterBandpass:
		return 2
	default:
		return 0
	}
}

// filterTypeName is the inverse — used as the row's format function so
// the slider position renders as a human-readable label.
func filterTypeName(v float32) string {
	switch int(v) {
	case 1:
		return "highpass"
	case 2:
		return "bandpass"
	default:
		return "lowpass"
	}
}

// filterTypeFromIndex turns a slider value back into the song-side
// enum so apply functions can write FilterConfig.Type cleanly.
func filterTypeFromIndex(v float32) song.FilterType {
	switch int(v) {
	case 1:
		return song.FilterHighpass
	case 2:
		return song.FilterBandpass
	default:
		return song.FilterLowpass
	}
}

// applyFilter — band-tweak path for cutoff/resonance.
func applyFilter(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushFilter(m, t)
	}
}

func applyFilterEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Filter.Enabled = v > 0
	return pushFilter(m, t)
}

func applyFilterType(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Filter.Type = filterTypeFromIndex(v)
	return pushFilter(m, t)
}

func pushFilter(m *Model, t *song.Track) (tea.Cmd, error) {
	if t.Filter.Type == "" {
		t.Filter.Type = song.FilterLowpass
	}
	if m.songPath != "" {
		if err := song.SetTrackFilter(m.songPath, m.mix.trackID, t.Filter); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackFilter,
		Track: m.mix.trackID,
		Filter: &protocol.FilterConfigCmd{
			Enabled:   t.Filter.Enabled,
			Type:      string(t.Filter.Type),
			Cutoff:    t.Filter.Cutoff,
			Resonance: t.Filter.Resonance,
		},
	}), nil
}

// driveTypeIndex maps the song-side enum to the slider index used by
// the TUI's "type" row.
func driveTypeIndex(t song.DriveType) int {
	switch t {
	case song.DriveHard:
		return 1
	case song.DriveFold:
		return 2
	default:
		return 0
	}
}

func driveTypeName(v float32) string {
	switch int(v) {
	case 1:
		return "hard"
	case 2:
		return "fold"
	default:
		return "soft"
	}
}

func driveTypeFromIndex(v float32) song.DriveType {
	switch int(v) {
	case 1:
		return song.DriveHard
	case 2:
		return song.DriveFold
	default:
		return song.DriveSoft
	}
}

// applyDrive — band-tweak path for the drive/tone/level float fields.
func applyDrive(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushDrive(m, t)
	}
}

func applyDriveEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Drive.Enabled = v > 0
	return pushDrive(m, t)
}

func applyDriveType(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Drive.Type = driveTypeFromIndex(v)
	return pushDrive(m, t)
}

func pushDrive(m *Model, t *song.Track) (tea.Cmd, error) {
	if t.Drive.Type == "" {
		t.Drive.Type = song.DriveSoft
	}
	if m.songPath != "" {
		if err := song.SetTrackDrive(m.songPath, m.mix.trackID, t.Drive); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackDrive,
		Track: m.mix.trackID,
		Drive: &protocol.DriveConfigCmd{
			Enabled: t.Drive.Enabled,
			Type:    string(t.Drive.Type),
			Drive:   t.Drive.Drive,
			Tone:    t.Drive.Tone,
			Level:   t.Drive.Level,
		},
	}), nil
}

// applyLofi — band-tweak path for the rate field (bits has its own
// helper because it's an int, not a float).
func applyLofi(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushLofi(m, t)
	}
}

func applyLofiEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Lofi.Enabled = v > 0
	return pushLofi(m, t)
}

func applyLofiBits(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Lofi.Bits = int(v)
	return pushLofi(m, t)
}

func pushLofi(m *Model, t *song.Track) (tea.Cmd, error) {
	if m.songPath != "" {
		if err := song.SetTrackLofi(m.songPath, m.mix.trackID, t.Lofi); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackLofi,
		Track: m.mix.trackID,
		Lofi: &protocol.LofiConfigCmd{
			Enabled: t.Lofi.Enabled,
			Bits:    t.Lofi.Bits,
			Rate:    t.Lofi.Rate,
		},
	}), nil
}

// applyReverb — band-tweak path for size/damping/mix.
func applyReverb(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		return pushReverb(m, t)
	}
}

func applyReverbEnabled(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	t.Reverb.Enabled = v > 0
	return pushReverb(m, t)
}

func pushReverb(m *Model, t *song.Track) (tea.Cmd, error) {
	if m.songPath != "" {
		if err := song.SetTrackReverb(m.songPath, m.mix.trackID, t.Reverb); err != nil {
			return nil, err
		}
		m.lastSelfEdit = time.Now()
	}
	return sendCommand(m.conn, protocol.Command{
		Cmd:   protocol.CmdSetTrackReverb,
		Track: m.mix.trackID,
		Reverb: &protocol.ReverbConfigCmd{
			Enabled: t.Reverb.Enabled,
			Size:    t.Reverb.Size,
			Damping: t.Reverb.Damping,
			Mix:     t.Reverb.Mix,
		},
	}), nil
}

// keep filepath imported — currently only referenced via styles above,
// but the load-song prompt is wired through here in passing.
var _ = filepath.Base
