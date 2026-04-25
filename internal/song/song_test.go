package song

import (
	"path/filepath"
	"testing"
)

func TestLoadDemo(t *testing.T) {
	path, err := filepath.Abs("../../examples/demo/song.toml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Project.BPM != 120 {
		t.Errorf("bpm = %d, want 120", s.Project.BPM)
	}
	wantSections := []string{"intro", "verse", "chorus", "bridge", "outro"}
	for _, n := range wantSections {
		if _, ok := s.Sections[n]; !ok {
			t.Errorf("missing section %q", n)
		}
	}
	if got, want := len(s.Arrangement), 9; got != want {
		t.Errorf("arrangement len = %d, want %d", got, want)
	}
	verse, ok := s.Sections["verse"]
	if !ok {
		t.Fatal("no verse section")
	}
	if verse.Bars != 4 {
		t.Errorf("verse.Bars = %d, want 4", verse.Bars)
	}
	// Every pattern referenced by the verse section must have been loaded
	// and must match its section's bars.
	for trackID, patName := range verse.Parts {
		p, ok := s.Patterns[patName]
		if !ok {
			t.Errorf("verse track %q refers to missing pattern %q", trackID, patName)
			continue
		}
		if p.Bars != verse.Bars {
			t.Errorf("pattern %q bars = %d, want %d (section)", patName, p.Bars, verse.Bars)
		}
	}
	// Demo uses bare names — all slots should be Repeat=1.
	for _, slot := range s.Arrangement {
		if slot.Repeat != 1 {
			t.Errorf("slot %q repeat = %d, want 1", slot.Section, slot.Repeat)
		}
	}
}

func TestParseArrangement_BareAndStarShorthand(t *testing.T) {
	cases := []struct {
		in   []string
		want []ArrangementSlot
	}{
		{
			in:   []string{"intro", "verse*4", "chorus"},
			want: []ArrangementSlot{{"intro", 1}, {"verse", 4}, {"chorus", 1}},
		},
		{
			in:   []string{"a*2", " b * 3 "},
			want: []ArrangementSlot{{"a", 2}, {"b", 3}},
		},
	}
	for _, tc := range cases {
		got, err := parseArrangement(tc.in)
		if err != nil {
			t.Errorf("parseArrangement(%v): %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("parseArrangement(%v): got %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseArrangement(%v)[%d] = %v, want %v", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseArrangement_RejectsInvalid(t *testing.T) {
	for _, bad := range []string{"", "*4", "verse*0", "verse*-1", "verse*abc"} {
		if _, err := parseArrangement([]string{bad}); err == nil {
			t.Errorf("parseArrangement(%q): want error, got nil", bad)
		}
	}
}
