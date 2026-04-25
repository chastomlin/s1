package song

import (
	"strings"
	"testing"
)

func TestParsePitch(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"C4", 60},
		{"C-1", 0},
		{"G9", 127},
		{"D#3", 51},
		{"Eb5", 75},
		{"A4", 69},
		{"B3", 59},
	}
	for _, c := range cases {
		got, err := parsePitch(c.in)
		if err != nil {
			t.Errorf("parsePitch(%q) err: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePitch(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParsePitchErrors(t *testing.T) {
	for _, s := range []string{"", "H4", "C", "C10", "C#"} {
		if _, err := parsePitch(s); err == nil {
			t.Errorf("parsePitch(%q) expected error", s)
		}
	}
}

func TestParseCell(t *testing.T) {
	must := func(in string) Cell {
		t.Helper()
		c, err := parseCell(in)
		if err != nil {
			t.Fatalf("parseCell(%q): %v", in, err)
		}
		return c
	}
	if got := must("."); got.Kind != CellRest {
		t.Errorf("'.' -> %+v", got)
	}
	if got := must("-"); got.Kind != CellTie {
		t.Errorf("'-' -> %+v", got)
	}
	if got := must("X"); got.Kind != CellSample || got.Vel != 100 {
		t.Errorf("'X' -> %+v", got)
	}
	if got := must("X.9"); got.Kind != CellSample || got.Vel != 127 {
		t.Errorf("'X.9' -> %+v", got)
	}
	if got := must("C4"); got.Kind != CellNote || got.Note != 60 || got.Vel != 100 {
		t.Errorf("'C4' -> %+v", got)
	}
	if got := must("D#3.5"); got.Kind != CellNote || got.Note != 51 || got.Vel != 70 {
		t.Errorf("'D#3.5' -> %+v", got)
	}
	if got := must("C4.96"); got.Kind != CellNote || got.Note != 60 || got.Vel != 96 {
		t.Errorf("'C4.96' -> %+v", got)
	}
}

func TestParsePattern(t *testing.T) {
	src := `
# a simple one-bar pattern at default 4/4, 4 cells/beat = 16 cells
bars 1

X . . . X . . . X . . . X . . .
`
	p, err := ParsePattern("verse-k", strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParsePattern: %v", err)
	}
	if p.Bars != 1 || p.BeatsPerBar != 4 || p.Resolution != 4 {
		t.Errorf("defaults wrong: %+v", p)
	}
	if p.TotalCells() != 16 {
		t.Errorf("TotalCells = %d, want 16", p.TotalCells())
	}
	if len(p.Cells) != 16 {
		t.Fatalf("cells = %d, want 16", len(p.Cells))
	}
	if p.Cells[0].Kind != CellSample {
		t.Errorf("cell[0] = %+v, want CellSample", p.Cells[0])
	}

	// Pitched pattern with ties.
	bassBody := `bars 1

C2 - - - E2 - - - G2 - - - C3 - - -
`
	bass, err := ParsePattern("verse-bass", strings.NewReader(bassBody))
	if err != nil {
		t.Fatalf("ParsePattern bass: %v", err)
	}
	if bass.Cells[0].Kind != CellNote || bass.Cells[0].Note != 36 /* C2 */ {
		t.Errorf("bass[0] = %+v, want C2 note", bass.Cells[0])
	}
	if bass.Cells[1].Kind != CellTie {
		t.Errorf("bass[1] = %+v, want tie", bass.Cells[1])
	}
}

func TestParsePatternBarSeparators(t *testing.T) {
	src := `bars 2

| X . . . | X . . . | X . . . | X . . . | | X . . . | X . . . | X . . . | X . . . |
`
	p, err := ParsePattern("p", strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParsePattern: %v", err)
	}
	if p.TotalCells() != 32 || len(p.Cells) != 32 {
		t.Errorf("bar-separator stripping failed: cells=%d", len(p.Cells))
	}
}

// Cell tokens may be split across multiple lines below the headers — the
// parser concatenates until it has the expected TotalCells().
func TestParsePatternMultilineCells(t *testing.T) {
	src := `bars 2

X . . . | X . . . | X . . . | X . . .
X . . . | X . . . | X . . . | X . . .
`
	p, err := ParsePattern("p", strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParsePattern: %v", err)
	}
	if p.TotalCells() != 32 || len(p.Cells) != 32 {
		t.Errorf("multi-line parse: cells=%d want 32", len(p.Cells))
	}
}

func TestParsePatternErrors(t *testing.T) {
	cases := map[string]string{
		"wrong cell count": `bars 1
X . .
`,
		"bad header": `bars -1
. . . . . . . . . . . . . . . .
`,
		"missing bars": `. . . . . . . . . . . . . . . .
`,
	}
	for name, src := range cases {
		if _, err := ParsePattern("x", strings.NewReader(src)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLeadingTiePromoted(t *testing.T) {
	src := `bars 1

- . . . . . . . . . . . . . . .
`
	p, err := ParsePattern("x", strings.NewReader(src))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if p.Cells[0].Kind != CellRest {
		t.Errorf("leading tie not promoted to rest: %+v", p.Cells[0])
	}
}
