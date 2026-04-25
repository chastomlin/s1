package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"seqone/internal/protocol"
	"seqone/internal/song"
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

// renderMixBody draws the parameter list. Rows live in three
// loosely-grouped sections (gain/pan, EQ, comp) separated by blank
// lines so the eye can scan the chain top-to-bottom.
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
		if p.separator != "" {
			b.WriteString("  ")
			b.WriteString(styleColHead.Render(p.separator))
			b.WriteString("\n")
			continue
		}
		marker := "  "
		labelStyle := styleArrIdle
		if i == mx.paramIdx {
			marker = "▶ "
			labelStyle = styleArrPlaying
		}
		b.WriteString(marker)
		b.WriteString(labelStyle.Render(padRight(p.label, 14)))
		b.WriteString("  ")
		b.WriteString(p.format(p.get(&mx.track)))
		b.WriteByte('\n')
	}

	if mx.message != "" {
		b.WriteString("\n")
		b.WriteString(styleErr.Render("  ! " + mx.message))
		b.WriteString("\n")
	}
	return b.String()
}

// mixParam describes one editable parameter row. label is what
// renders in the left column; format produces the right-side string;
// get/apply carry the working-copy mutation + engine push. step /
// bigStep are added/subtracted on ← / shift-←. min/max are the
// clamp range — kept aligned with the loader's range checks.
type mixParam struct {
	label     string
	separator string // when non-empty this row is a header, no value
	get       func(*song.Track) float32
	apply     func(m *Model, t *song.Track, v float32) (tea.Cmd, error)
	step      float32
	bigStep   float32
	min, max  float32
	format    func(float32) string
}

// mixParams is the canonical row order for the mix view. Header
// separators slot in as label-only rows so the cursor skips them
// (paramIdx is constrained to non-separator rows by adjustMix's
// guard plus the up/down clamp; for clarity we just include them
// in the slice and let the user "land" on them — the apply lambda
// is nil, so adjustMix is a no-op when paramIdx points at one).
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
	{separator: "EQ"},
	{
		label: "low_freq",
		get:   func(t *song.Track) float32 { return t.EQ.LowFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.LowFreq }),
		step:  10, bigStep: 100, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "low_gain",
		get:   func(t *song.Track) float32 { return t.EQ.LowGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.LowGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{
		label: "mid_freq",
		get:   func(t *song.Track) float32 { return t.EQ.MidFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidFreq }),
		step:  20, bigStep: 200, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "mid_q",
		get:   func(t *song.Track) float32 { return t.EQ.MidQ },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidQ }),
		step:  0.05, bigStep: 0.5, min: 0.1, max: 10,
		format: func(v float32) string { return fmt.Sprintf("Q %.2f", v) },
	},
	{
		label: "mid_gain",
		get:   func(t *song.Track) float32 { return t.EQ.MidGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.MidGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{
		label: "high_freq",
		get:   func(t *song.Track) float32 { return t.EQ.HighFreq },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.HighFreq }),
		step:  50, bigStep: 500, min: 20, max: 20000,
		format: func(v float32) string { return fmt.Sprintf("%4.0f Hz", v) },
	},
	{
		label: "high_gain",
		get:   func(t *song.Track) float32 { return t.EQ.HighGain },
		apply: applyEQ(func(t *song.Track) *float32 { return &t.EQ.HighGain }),
		step:  0.5, bigStep: 3, min: -36, max: 36,
		format: dbFormat,
	},
	{separator: "COMPRESSOR"},
	{
		label: "threshold",
		get:   func(t *song.Track) float32 { return t.Comp.ThresholdDB },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.ThresholdDB }),
		step:  0.5, bigStep: 3, min: -60, max: 0,
		format: dbFormat,
	},
	{
		label: "ratio",
		get:   func(t *song.Track) float32 { return t.Comp.Ratio },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.Ratio }),
		step:  0.1, bigStep: 1, min: 1, max: 20,
		format: func(v float32) string { return fmt.Sprintf("%.1f:1", v) },
	},
	{
		label: "attack",
		get:   func(t *song.Track) float32 { return t.Comp.AttackMs },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.AttackMs }),
		step:  1, bigStep: 10, min: 0.1, max: 500,
		format: func(v float32) string { return fmt.Sprintf("%.1f ms", v) },
	},
	{
		label: "release",
		get:   func(t *song.Track) float32 { return t.Comp.ReleaseMs },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.ReleaseMs }),
		step:  5, bigStep: 50, min: 1, max: 5000,
		format: func(v float32) string { return fmt.Sprintf("%.0f ms", v) },
	},
	{
		label: "makeup",
		get:   func(t *song.Track) float32 { return t.Comp.MakeupDB },
		apply: applyComp(func(t *song.Track) *float32 { return &t.Comp.MakeupDB }),
		step:  0.5, bigStep: 3, min: -12, max: 24,
		format: dbFormat,
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
// every sub-parameter change resends all seven values.
func applyEQ(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		if m.songPath != "" {
			if err := song.SetTrackEQ(m.songPath, m.mix.trackID, t.EQ); err != nil {
				return nil, err
			}
		}
		return sendCommand(m.conn, protocol.Command{
			Cmd:   protocol.CmdSetTrackEQ,
			Track: m.mix.trackID,
			EQ: &protocol.EQConfigCmd{
				LowFreq: t.EQ.LowFreq, LowGain: t.EQ.LowGain,
				MidFreq: t.EQ.MidFreq, MidQ: t.EQ.MidQ, MidGain: t.EQ.MidGain,
				HighFreq: t.EQ.HighFreq, HighGain: t.EQ.HighGain,
			},
		}), nil
	}
}

// applyComp is the comp counterpart to applyEQ — sends the full comp
// block on every sub-param tweak.
func applyComp(field func(*song.Track) *float32) func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
	return func(m *Model, t *song.Track, v float32) (tea.Cmd, error) {
		*field(t) = v
		if m.songPath != "" {
			if err := song.SetTrackComp(m.songPath, m.mix.trackID, t.Comp); err != nil {
				return nil, err
			}
		}
		return sendCommand(m.conn, protocol.Command{
			Cmd:   protocol.CmdSetTrackComp,
			Track: m.mix.trackID,
			Comp: &protocol.CompConfigCmd{
				ThresholdDB: t.Comp.ThresholdDB,
				Ratio:       t.Comp.Ratio,
				AttackMs:    t.Comp.AttackMs,
				ReleaseMs:   t.Comp.ReleaseMs,
				MakeupDB:    t.Comp.MakeupDB,
			},
		}), nil
	}
}

// keep imports used even when the file is trimmed during refactors
var _ = lipgloss.NewStyle
var _ = filepath.Base
