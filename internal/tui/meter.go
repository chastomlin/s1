package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Meter colour bands — green up to ~70%, yellow into hot territory,
// red the last sliver where you're about to clip.
var (
	styleMeterFloor   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	styleMeterGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	styleMeterYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleMeterRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleMeterPeak    = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Bold(true)
	styleMeterPeakHot = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)

// renderMeter draws a fixed-width VU bar with a peak-program-meter
// overlay. The bar fills with █ proportional to level; a single │
// marker is drawn at the peak position (slowly decays). Both inputs
// are linear amplitudes in [0, 1]; non-finite values are clamped.
//
// At width=8: roughly 4 chars per "octave" of level if you're picturing
// dB scaling — but the math here is linear because the audio levels
// the mixer reports are already perceptually-friendly post-master-gain.
func renderMeter(level, peak float32, width int) string {
	if width < 1 {
		return ""
	}
	if level < 0 {
		level = 0
	}
	if level > 1 {
		level = 1
	}
	if peak < 0 {
		peak = 0
	}
	if peak > 1 {
		peak = 1
	}

	levelCells := int(level * float32(width))
	if levelCells > width {
		levelCells = width
	}
	peakIdx := int(peak * float32(width))
	if peakIdx >= width {
		peakIdx = width - 1
	}
	yellowFrom := int(0.7 * float32(width))
	redFrom := int(0.92 * float32(width))

	var b strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case i < levelCells:
			switch {
			case i >= redFrom:
				b.WriteString(styleMeterRed.Render("█"))
			case i >= yellowFrom:
				b.WriteString(styleMeterYellow.Render("█"))
			default:
				b.WriteString(styleMeterGreen.Render("█"))
			}
		case peak > 0 && i == peakIdx:
			if peakIdx >= redFrom {
				b.WriteString(styleMeterPeakHot.Render("│"))
			} else {
				b.WriteString(styleMeterPeak.Render("│"))
			}
		default:
			b.WriteString(styleMeterFloor.Render("·"))
		}
	}
	return b.String()
}

// renderStereoMeter pairs two renderMeter calls with a thin gap so the
// L and R bars read as a unit.
func renderStereoMeter(ll, lp, rl, rp float32, width int) string {
	return renderMeter(ll, lp, width) + " " + renderMeter(rl, rp, width)
}
