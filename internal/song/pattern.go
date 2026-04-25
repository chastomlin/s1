package song

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Pattern is a single-track, fixed-length clip of cells. Patterns live in a
// flat namespace (one .pat file each) and are bound to tracks by [[sections]]
// entries in song.toml. Cells always has Bars * BeatsPerBar * Resolution
// entries.
type Pattern struct {
	Name        string
	Bars        int
	BeatsPerBar int // from time signature numerator, default 4
	Resolution  int // cells per beat; default 4 (i.e. 16ths in 4/4)
	Cells       []Cell
}

// TotalCells is how many cells Cells must contain.
func (p Pattern) TotalCells() int {
	return p.Bars * p.BeatsPerBar * p.Resolution
}

type CellKind int

const (
	CellRest CellKind = iota
	CellTie
	CellSample
	CellNote
)

// Cell is one step in a Pattern.
// Note and Vel are only meaningful for CellNote; Vel also carries for CellSample.
type Cell struct {
	Kind CellKind
	Note int // MIDI note 0..127
	Vel  int // velocity 0..127
}

// ParsePattern reads a .pat file body.
//
// Format:
//
//	bars 4
//	beats_per_bar 4        # optional, default 4
//	resolution 4           # optional, default 4 (cells per beat)
//
//	X . . . | X . . . | X . . . | X . . .
//
// Cell tokens may be spread across any number of lines after the headers;
// the reader concatenates them into a single row and validates the total
// count against Bars × BeatsPerBar × Resolution.
//
// Tokens:
//
//	.         rest
//	-         tie (continue previous note)
//	X         sample trigger (velocity default 100; "X.8" for 80%)
//	<pitch>   note-on, e.g. C4, D#3, Eb5
//	<pitch>.<v>  note-on with velocity 0..1 mapped to 1..127
//
// Pipe characters '|' in cell rows are cosmetic bar separators and ignored.
// Lines starting with '#' are comments.
func ParsePattern(name string, r io.Reader) (Pattern, error) {
	p := Pattern{
		Name:        name,
		BeatsPerBar: 4,
		Resolution:  4,
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	seenCells := false
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !seenCells {
			fields := strings.Fields(line)
			if len(fields) == 2 && isHeaderKey(fields[0]) {
				n, err := strconv.Atoi(fields[1])
				if err != nil || n <= 0 {
					return p, fmt.Errorf("line %d: %s must be a positive integer", lineNo, fields[0])
				}
				switch fields[0] {
				case "bars":
					p.Bars = n
				case "beats_per_bar":
					p.BeatsPerBar = n
				case "resolution":
					p.Resolution = n
				}
				continue
			}
		}

		seenCells = true
		cells, err := parseCells(line)
		if err != nil {
			return p, fmt.Errorf("line %d: %w", lineNo, err)
		}
		p.Cells = append(p.Cells, cells...)
	}
	if err := sc.Err(); err != nil {
		return p, err
	}

	if p.Bars == 0 {
		return p, fmt.Errorf("pattern %q: missing 'bars N' header", name)
	}

	want := p.TotalCells()
	if len(p.Cells) != want {
		return p, fmt.Errorf("pattern %q: has %d cells, expected %d (bars=%d * beats=%d * res=%d)",
			name, len(p.Cells), want, p.Bars, p.BeatsPerBar, p.Resolution)
	}
	// Leading tie is meaningless; promote it to a rest so downstream code
	// doesn't have to defend against it.
	if len(p.Cells) > 0 && p.Cells[0].Kind == CellTie {
		p.Cells[0] = Cell{Kind: CellRest}
	}

	return p, nil
}

func isHeaderKey(s string) bool {
	switch s {
	case "bars", "beats_per_bar", "resolution":
		return true
	}
	return false
}

func parseCells(s string) ([]Cell, error) {
	// Strip cosmetic bar separators.
	s = strings.ReplaceAll(s, "|", " ")
	fields := strings.Fields(s)
	cells := make([]Cell, 0, len(fields))
	for _, f := range fields {
		c, err := parseCell(f)
		if err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, nil
}

func parseCell(tok string) (Cell, error) {
	switch tok {
	case ".":
		return Cell{Kind: CellRest}, nil
	case "-":
		return Cell{Kind: CellTie}, nil
	}

	// Sample trigger: "X" or "X.<vel>"
	if tok[0] == 'X' {
		vel := 100
		if len(tok) > 1 {
			if tok[1] != '.' {
				return Cell{}, fmt.Errorf("bad sample token %q", tok)
			}
			v, err := parseVelocity(tok[2:])
			if err != nil {
				return Cell{}, fmt.Errorf("bad sample velocity in %q: %w", tok, err)
			}
			vel = v
		}
		return Cell{Kind: CellSample, Vel: vel}, nil
	}

	// Pitched note, e.g. "C4", "D#3", "Eb5", "C4.8"
	pitch, velStr, hasVel := strings.Cut(tok, ".")
	n, err := parsePitch(pitch)
	if err != nil {
		return Cell{}, fmt.Errorf("bad note token %q: %w", tok, err)
	}
	vel := 100
	if hasVel {
		v, err := parseVelocity(velStr)
		if err != nil {
			return Cell{}, fmt.Errorf("bad velocity in %q: %w", tok, err)
		}
		vel = v
	}
	return Cell{Kind: CellNote, Note: n, Vel: vel}, nil
}

// parsePitch: "C4" -> 60 (middle C). Supports # and b accidentals and negative octaves.
func parsePitch(s string) (int, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("too short")
	}
	base := map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}
	semi, ok := base[s[0]]
	if !ok {
		return 0, fmt.Errorf("unknown note letter %q", s[:1])
	}
	i := 1
	switch s[i] {
	case '#':
		semi++
		i++
	case 'b':
		semi--
		i++
	}
	if i >= len(s) {
		return 0, fmt.Errorf("missing octave")
	}
	oct, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0, fmt.Errorf("bad octave %q", s[i:])
	}
	n := (oct+1)*12 + semi
	if n < 0 || n > 127 {
		return 0, fmt.Errorf("note out of MIDI range: %d", n)
	}
	return n, nil
}

// parseVelocity: "8" -> 0.8 -> 102; "100" -> interpreted as raw MIDI 0..127.
// Heuristic: if the value starts with '0' or is a single digit, treat as 0..9 scaled
// to 0..127; otherwise if <= 127 it's raw MIDI; else error.
func parseVelocity(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty velocity")
	}
	// Single digit 1..9 -> fractional (1=~14, 5=~71, 9=~127).
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		d := int(s[0] - '0')
		return d * 127 / 9, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 || n > 127 {
		return 0, fmt.Errorf("velocity %d out of 0..127", n)
	}
	return n, nil
}
