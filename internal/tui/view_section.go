package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"seqone/internal/protocol"
)

// renderSectionBody draws the section-scoped tracks view. Shows the
// section's name and bars in a header, the tracks bound by
// Section.Parts as rows (with per-track activity bars spanning the
// section's extent), and a playhead when the transport is inside this
// section.
func (m Model) renderSectionBody() string {
	if m.song == nil {
		return ""
	}
	if m.sectionView == "" {
		return styleInactive.Render("  no section selected\n")
	}
	sec, ok := m.song.Sections[m.sectionView]
	if !ok {
		return styleErr.Render("  section not found: " + m.sectionView + "\n")
	}

	var b strings.Builder

	// Header: "section: verse (4 bars)" + optional LIVE / REC chips.
	b.WriteString(styleColHead.Render(fmt.Sprintf("  section: %s  (%d bars)", sec.Name, sec.Bars)))
	if m.liveEnabled {
		target := m.selectedTrackID()
		if target == "" {
			target = "—"
		} else {
			target = m.trackDisplayName(target)
		}
		b.WriteString("     ")
		b.WriteString(styleLiveBadge.Render("● LIVE"))
		b.WriteString(styleInactive.Render(fmt.Sprintf("  on: %s   oct %d", target, m.liveOctave)))
		if m.recordArmed {
			b.WriteString("   ")
			b.WriteString(styleRecBadge.Render("● REC"))
		}
	}
	b.WriteString("\n\n")

	trackIDs := m.displayTracks()
	if len(trackIDs) == 0 {
		b.WriteString(styleInactive.Render("  no tracks in this section — press a to add one\n"))
		return b.String()
	}

	// Activity strip is one chip spanning section.Bars × 6 cells. 6 is
	// just wide enough to label each column "bar N" in the header.
	const barWidth = 6
	activityWidth := sec.Bars * barWidth
	if activityWidth < 12 {
		activityWidth = 12
	}

	// Left-hand column widths.
	nameWidth := 8
	for _, id := range trackIDs {
		n := m.trackDisplayName(id)
		if len(n) > nameWidth {
			nameWidth = len(n)
		}
	}
	const (
		flashCol = 1
		numCol   = 2
		chCol    = 2
		msCol    = 2
		meterCol = 8 // mono meter (max of L, R) — stereo-per-track would push the activity off-screen on common terminal widths
	)
	leftPad := 2
	gap := 2

	soloActive := len(m.solos) > 0

	// Column header row.
	b.WriteString(strings.Repeat(" ", leftPad+flashCol+gap))
	b.WriteString(styleColHead.Render(padRight("#", numCol)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padRight("CH", chCol)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padRight("NAME", nameWidth)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padRight("MS", msCol)))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(styleColHead.Render(padRight("METER", meterCol)))
	b.WriteString(strings.Repeat(" ", gap))
	// Activity header: "bar 1 bar 2 bar 3 bar 4" roughly centered.
	barsHeader := buildBarsHeader(sec.Bars, activityWidth)
	b.WriteString(styleColHead.Render(barsHeader))
	b.WriteString("\n")

	// Divider.
	b.WriteString(strings.Repeat(" ", leftPad))
	dashCount := flashCol + gap + numCol + gap + chCol + gap + nameWidth + gap + msCol + gap + meterCol + gap + activityWidth
	b.WriteString(styleInactive.Render(strings.Repeat("─", dashCount)))
	b.WriteString("\n")

	// Track rows.
	now := time.Now()
	for idx, id := range trackIDs {
		selected := idx == m.selectedTrackIdx
		muted := m.mutes[id]
		soloed := m.solos[id]
		audible := (!soloActive && !muted) || soloed

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

		var row strings.Builder
		row.WriteString(padBg(leftPad))

		flashed := !m.flash[id].IsZero() && now.Sub(m.flash[id]) < flashDuration
		switch {
		case flashed:
			row.WriteString(withBg(styleFlashHot).Render("●"))
		case selected:
			row.WriteString(withBg(styleActive).Render("▶"))
		default:
			row.WriteString(withBg(styleFlashDim).Render("·"))
		}
		row.WriteString(padBg(gap))
		row.WriteString(withBg(styleTrackNo).Render(fmt.Sprintf("%02d", idx+1)))
		row.WriteString(padBg(gap))
		row.WriteString(withBg(styleTrackCh).Render(padLeft(m.channelLabel(id), chCol)))
		row.WriteString(padBg(gap))

		nameStyle := styleTrackNm
		if selected {
			nameStyle = styleActive
		}
		row.WriteString(withBg(nameStyle).Render(padRight(m.trackDisplayName(id), nameWidth)))
		row.WriteString(padBg(gap))

		if muted {
			row.WriteString(withBg(styleMute).Render("M"))
		} else {
			row.WriteString(withBg(styleOff).Render("·"))
		}
		if soloed {
			row.WriteString(withBg(styleSolo).Render("S"))
		} else {
			row.WriteString(withBg(styleOff).Render("·"))
		}
		row.WriteString(padBg(gap))

		// Meter cell — combined mono level (max of L/R) so the column
		// fits next to the activity bar. Stereo lives on the master.
		mt := m.meterTracks[id]
		level := mt.LL
		if mt.RL > level {
			level = mt.RL
		}
		peak := mt.LP
		if mt.RP > peak {
			peak = mt.RP
		}
		row.WriteString(renderMeter(level, peak, meterCol))
		row.WriteString(padBg(gap))

		// Activity cell: single contiguous bar if this track has any
		// non-rest cell in the referenced pattern, else blank space.
		// Overlays the in-progress record buffer so edits light up the
		// row immediately, without waiting for the next disk flush.
		if m.sectionTrackActivity(sec, id) {
			cell := strings.Repeat("█", activityWidth)
			switch {
			case !audible:
				row.WriteString(withBg(styleOff).Render(cell))
			case m.position.Section == sec.Name && m.state == protocol.StatePlaying:
				row.WriteString(withBg(styleBlock).Render(cell))
			default:
				row.WriteString(withBg(styleBlockLo).Render(cell))
			}
		} else {
			row.WriteString(padBg(activityWidth))
		}

		b.WriteString(row.String())
		b.WriteString("\n")
	}

	// Playhead row — only aligned when the transport is inside this section.
	if m.position.Section == sec.Name && m.position.Bar > 0 {
		prefix := leftPad + flashCol + gap + numCol + gap + chCol + gap + nameWidth + gap + msCol + gap + meterCol + gap
		absStart := m.sectionAbsStartBar()
		if absStart > 0 {
			barInSection := m.position.Bar - absStart
			totalBarsThisSlot := sec.Bars * m.currentSlotRepeat()
			if totalBarsThisSlot > 0 && barInSection >= 0 {
				// Loop the bar within the section if the playback slot has
				// Repeat > 1 (playhead walks the section repeatedly).
				barInSection = barInSection % sec.Bars
				cols := barInSection * (activityWidth / sec.Bars)
				b.WriteString(strings.Repeat(" ", prefix+cols))
				b.WriteString(stylePlayhead.Render("▲"))
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

// buildBarsHeader produces a "bar 1   bar 2   ..." row that fits inside
// width. At narrow widths it falls back to bare numeric labels "1 2 ..."
// centered per column, and finally to a compact "bars 1..N" blurb.
func buildBarsHeader(bars, width int) string {
	if bars <= 0 {
		return ""
	}
	perBar := width / bars
	var b strings.Builder
	switch {
	case perBar >= 5:
		for i := 1; i <= bars; i++ {
			b.WriteString(centerTrunc(fmt.Sprintf("bar %d", i), perBar))
		}
	case perBar >= 2:
		for i := 1; i <= bars; i++ {
			b.WriteString(centerTrunc(fmt.Sprintf("%d", i), perBar))
		}
	default:
		return padRight(fmt.Sprintf("bars 1..%d", bars), width)
	}
	s := b.String()
	if len(s) < width {
		s += strings.Repeat(" ", width-len(s))
	}
	return s
}

// currentSlotRepeat returns the Repeat value for the arrangement slot
// currently under the playhead, or 1 if no match. Used by the section
// view's playhead calc to wrap within the section when the slot repeats.
func (m Model) currentSlotRepeat() int {
	if m.song == nil {
		return 1
	}
	for _, slot := range m.song.Arrangement {
		if slot.Section == m.sectionView {
			return slot.Repeat
		}
	}
	return 1
}
