package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"seqone/internal/protocol"
)

var (
	// Arrangement row styles. The cursor row gets a backgrounded label
	// so it stays visible when the row also carries the "playing" glyph.
	// styleArrCursor is applied per-cell like the section-view selection.
	styleArrCursorBg = lipgloss.NewStyle().Background(colorTrackSelBg)
	styleArrPlaying  = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	styleArrIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleArrBars     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleArrRepeat   = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
)

// renderArrangementBody draws the song-level arrangement view as a
// vertical list: one row per arrangement slot, with a cursor for
// selection and a ▶ glyph for the slot currently under the playhead.
// Scales to long arrangements better than a horizontal ribbon and
// shows each slot's full detail (bars, repeat, selected summary).
func (m Model) renderArrangementBody() string {
	if m.song == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(styleColHead.Render("  Arrangement"))
	b.WriteString("\n\n")

	if len(m.song.Arrangement) == 0 {
		b.WriteString(styleInactive.Render("  (empty) — press n to create a section\n"))
		return b.String()
	}

	// Column widths.
	nameWidth := 8
	for _, slot := range m.song.Arrangement {
		if n := len(slot.Section); n > nameWidth {
			nameWidth = n
		}
	}
	const (
		leftPad  = 2
		markerW  = 1 // "▶" or " "
		gap      = 2
		indexW   = 3 // right-aligned 1..999
		barsW    = 4 // "4b" / "16b"
		repeatW  = 4 // "×4" / "×99"
	)

	// Header row.
	b.WriteString(strings.Repeat(" ", leftPad+markerW+gap))
	b.WriteString(styleColHead.Render(padLeft("#", indexW)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padRight("SECTION", nameWidth)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padLeft("BARS", barsW)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padLeft("×N", repeatW)))
	b.WriteString("\n")

	// Divider.
	dashes := markerW + gap + indexW + gap + nameWidth + gap + barsW + gap + repeatW
	b.WriteString(strings.Repeat(" ", leftPad))
	b.WriteString(styleInactive.Render(strings.Repeat("─", dashes)))
	b.WriteString("\n")

	// Rows.
	playingIdx := -1
	if m.state == protocol.StatePlaying {
		playingIdx = m.currentSlotIndex()
	}
	for i, slot := range m.song.Arrangement {
		sec, known := m.song.Sections[slot.Section]
		bars := 0
		if known {
			bars = sec.Bars
		}
		selected := i == m.arrangementIdx
		playing := i == playingIdx
		marker := " "
		if playing {
			marker = "▶"
		}

		withBg := func(s lipgloss.Style) lipgloss.Style {
			if selected {
				return s.Background(colorTrackSelBg)
			}
			return s
		}
		padBg := func(n int) string {
			s := strings.Repeat(" ", n)
			if selected {
				return styleSelPad.Render(s)
			}
			return s
		}

		// Left margin (untouched by selection so the cursor band starts
		// at the marker column).
		b.WriteString(strings.Repeat(" ", leftPad))

		// Marker column.
		if playing {
			b.WriteString(withBg(styleArrPlaying).Render(marker))
		} else {
			b.WriteString(padBg(markerW))
		}
		b.WriteString(padBg(gap))

		// Index.
		b.WriteString(withBg(styleArrBars).Render(padLeft(fmt.Sprintf("%d", i+1), indexW)))
		b.WriteString(padBg(gap))

		// Section name.
		nameStyle := styleArrIdle
		if playing {
			nameStyle = styleArrPlaying
		}
		b.WriteString(withBg(nameStyle).Render(padRight(slot.Section, nameWidth)))
		b.WriteString(padBg(gap))

		// Bars.
		barsTxt := "—"
		if bars > 0 {
			barsTxt = fmt.Sprintf("%db", bars)
		}
		b.WriteString(withBg(styleArrBars).Render(padLeft(barsTxt, barsW)))
		b.WriteString(padBg(gap))

		// Repeat (blank when =1 so the ×N column is visually quiet).
		repeatTxt := ""
		if slot.Repeat > 1 {
			repeatTxt = fmt.Sprintf("×%d", slot.Repeat)
		}
		b.WriteString(withBg(styleArrRepeat).Render(padLeft(repeatTxt, repeatW)))

		b.WriteByte('\n')
	}

	// Selected-slot summary below the list.
	if m.arrangementIdx >= 0 && m.arrangementIdx < len(m.song.Arrangement) {
		slot := m.song.Arrangement[m.arrangementIdx]
		if sec, ok := m.song.Sections[slot.Section]; ok {
			nTracks := len(sec.Parts)
			summary := fmt.Sprintf("  %s — %d bars, %d tracks", slot.Section, sec.Bars, nTracks)
			if slot.Repeat > 1 {
				summary += fmt.Sprintf(", ×%d", slot.Repeat)
			}
			b.WriteString("\n")
			b.WriteString(styleInactive.Render(summary))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderEmptyState draws the splash shown when no song is loaded. Two
// primary actions: create a new song (n) or load an existing one (l).
func (m Model) renderEmptyState() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(styleInactive.Render("  no song loaded"))
	b.WriteString("\n\n")
	b.WriteString(styleHelp.Render("    n: new song      l: load song\n"))
	return b.String()
}
