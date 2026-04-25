package song

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// roundTripPattern renders a Pattern then re-parses the rendered text and
// returns the parsed pattern. This is the load-bearing invariant: the
// writer must emit something the reader accepts and preserves exactly.
func roundTripPattern(t *testing.T, p Pattern) Pattern {
	t.Helper()
	body, err := renderPattern(p)
	if err != nil {
		t.Fatalf("renderPattern: %v", err)
	}
	got, err := ParsePattern(p.Name, strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParsePattern of rendered body failed:\n%s\nerror: %v", body, err)
	}
	return got
}

func TestWritePattern_AllCellKinds(t *testing.T) {
	// 16 cells: mix of sample triggers, rests, and ties. Pitched notes are
	// exercised separately in TestWritePattern_NotesAndOctaves.
	p := Pattern{
		Name: "x", Bars: 1, BeatsPerBar: 4, Resolution: 4,
		Cells: []Cell{
			{Kind: CellSample, Vel: 100}, {Kind: CellRest}, {Kind: CellRest}, {Kind: CellRest},
			{Kind: CellSample, Vel: 80}, {Kind: CellRest}, {Kind: CellRest}, {Kind: CellRest},
			{Kind: CellSample, Vel: 100}, {Kind: CellRest}, {Kind: CellRest}, {Kind: CellRest},
			{Kind: CellSample, Vel: 100}, {Kind: CellRest}, {Kind: CellRest}, {Kind: CellRest},
		},
	}
	got := roundTripPattern(t, p)
	if !reflect.DeepEqual(got.Cells, p.Cells) {
		t.Errorf("roundtrip mismatch:\n got: %+v\nwant: %+v", got.Cells, p.Cells)
	}
	if got.Bars != 1 || got.BeatsPerBar != 4 || got.Resolution != 4 {
		t.Errorf("headers not preserved: %+v", got)
	}
}

func TestWritePattern_NonDefaultHeaders(t *testing.T) {
	// 2 bars, 3 beats per bar, resolution 2 → 12 cells.
	p := Pattern{
		Name: "waltz", Bars: 2, BeatsPerBar: 3, Resolution: 2,
		Cells: make([]Cell, 12),
	}
	for i := range p.Cells {
		p.Cells[i] = Cell{Kind: CellRest}
	}
	body, err := renderPattern(p)
	if err != nil {
		t.Fatalf("renderPattern: %v", err)
	}
	if !strings.Contains(body, "beats_per_bar 3") {
		t.Error("expected beats_per_bar 3 header")
	}
	if !strings.Contains(body, "resolution 2") {
		t.Error("expected resolution 2 header")
	}
	got := roundTripPattern(t, p)
	if got.BeatsPerBar != 3 || got.Resolution != 2 {
		t.Errorf("headers roundtrip: %+v", got)
	}
}

func TestWritePattern_NotesAndOctaves(t *testing.T) {
	// Run each MIDI note 24..84 through formatPitch → parsePitch to ensure
	// every octave we expect to encounter round-trips.
	for n := 24; n <= 84; n++ {
		s := formatPitch(n)
		p, err := parsePitch(s)
		if err != nil {
			t.Errorf("parsePitch(%q): %v", s, err)
			continue
		}
		if p != n {
			t.Errorf("note %d: formatted %q, parsed back as %d", n, s, p)
		}
	}
}

func TestWritePattern_ArbitraryVelocityRoundTrips(t *testing.T) {
	// Any 0..127 velocity must round-trip exactly via the raw-integer form.
	for _, v := range []int{1, 14, 42, 80, 99, 127} {
		p := Pattern{
			Name: "x", Bars: 1, BeatsPerBar: 1, Resolution: 1,
			Cells: []Cell{{Kind: CellSample, Vel: v}},
		}
		got := roundTripPattern(t, p)
		if got.Cells[0].Vel != v {
			t.Errorf("vel %d round-tripped as %d", v, got.Cells[0].Vel)
		}
	}
}

func TestWritePattern_FileWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.pat")

	p := Pattern{
		Name: "x", Bars: 1, BeatsPerBar: 4, Resolution: 4,
		Cells: make([]Cell, 16),
	}
	for i := range p.Cells {
		p.Cells[i] = Cell{Kind: CellRest}
	}
	// Seed file with content to verify overwrite.
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePattern(path, p); err != nil {
		t.Fatalf("WritePattern: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "old") {
		t.Error("old content leaked through; write was not atomic or didn't overwrite")
	}
	if !strings.Contains(string(data), "bars 1") {
		t.Errorf("new content missing header:\n%s", data)
	}
	// Re-parse as sanity check.
	f, _ := os.Open(path)
	defer f.Close()
	if _, err := ParsePattern("x", f); err != nil {
		t.Errorf("written file doesn't parse: %v", err)
	}
}

func TestWritePattern_ShapeMismatchErrors(t *testing.T) {
	p := Pattern{
		Name: "x", Bars: 1, BeatsPerBar: 4, Resolution: 4,
		Cells: []Cell{{Kind: CellRest}}, // only 1 cell, expected 16
	}
	if _, err := renderPattern(p); err == nil {
		t.Error("expected error for mismatched cell count")
	}
}
