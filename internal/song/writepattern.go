package song

import (
	"fmt"
	"strings"
)

// WritePattern serialises a Pattern to a .pat file at path, in a form that
// round-trips through ParsePattern. Bar boundaries get a pipe separator for
// readability. The write is atomic (temp file + rename, mode preserved).
func WritePattern(path string, p Pattern) error {
	body, err := renderPattern(p)
	if err != nil {
		return err
	}
	return editAtomically(path, func(_ []byte) ([]byte, error) {
		return []byte(body), nil
	})
}

// renderPattern is the pure serialisation step, split out so tests can
// assert the exact formatting without touching the filesystem.
func renderPattern(p Pattern) (string, error) {
	if p.Bars <= 0 {
		return "", fmt.Errorf("pattern %q has bars=%d", p.Name, p.Bars)
	}
	if p.Resolution <= 0 {
		return "", fmt.Errorf("pattern %q has resolution=%d", p.Name, p.Resolution)
	}
	bpb := p.BeatsPerBar
	if bpb <= 0 {
		bpb = 4
	}
	want := p.Bars * bpb * p.Resolution
	if len(p.Cells) != want {
		return "", fmt.Errorf("pattern %q has %d cells, expected %d", p.Name, len(p.Cells), want)
	}

	rendered := make([]string, len(p.Cells))
	cellWidth := 1
	for i, c := range p.Cells {
		s := formatCell(c)
		if len(s) > cellWidth {
			cellWidth = len(s)
		}
		rendered[i] = s
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("bars %d\n", p.Bars))
	if bpb != 4 {
		b.WriteString(fmt.Sprintf("beats_per_bar %d\n", bpb))
	}
	if p.Resolution != 4 {
		b.WriteString(fmt.Sprintf("resolution %d\n", p.Resolution))
	}
	b.WriteByte('\n')

	stepsPerBar := bpb * p.Resolution
	for i, s := range rendered {
		if i > 0 && i%stepsPerBar == 0 {
			b.WriteString("| ")
		}
		b.WriteString(s)
		b.WriteString(strings.Repeat(" ", cellWidth-len(s)))
		if i != len(rendered)-1 && (i+1)%stepsPerBar != 0 {
			b.WriteByte(' ')
		}
	}
	b.WriteByte('\n')
	return b.String(), nil
}

// formatCell is the inverse of parseCell: emits the token form for one cell.
// Velocity is emitted as the raw MIDI value (0..127) when it differs from
// the default 100; the parser's 1..9 shorthand is lossy so we avoid it for
// machine-written output even though hand-edited files may still use it.
func formatCell(c Cell) string {
	switch c.Kind {
	case CellRest:
		return "."
	case CellTie:
		return "-"
	case CellSample:
		if c.Vel == 100 || c.Vel == 0 {
			return "X"
		}
		// Zero-pad so "1" becomes "01" — the parser's single-digit branch
		// (1..9 → 1/9..9/9 fractional velocity) would otherwise turn
		// velocity 1 into 14 on reparse.
		return fmt.Sprintf("X.%02d", c.Vel)
	case CellNote:
		s := formatPitch(c.Note)
		if c.Vel == 100 || c.Vel == 0 {
			return s
		}
		return fmt.Sprintf("%s.%02d", s, c.Vel)
	}
	return "."
}

// FormatPitch converts a MIDI note number 0..127 to "C4"-style text.
// parsePitch's rule: (oct+1)*12 + semi == MIDI. So middle C (60) is C4.
// Sharps are emitted with '#'; flats are never produced (the parser
// accepts both). Exported for TUI editors and any other consumer.
func FormatPitch(n int) string { return formatPitch(n) }

func formatPitch(n int) string {
	if n < 0 || n > 127 {
		return "?"
	}
	names := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	oct := n/12 - 1
	semi := n % 12
	return fmt.Sprintf("%s%d", names[semi], oct)
}
