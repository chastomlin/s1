package song

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// defaultSectionBlock is injected by writeSong when a test TOML doesn't
// already declare any sections. The demo arrangement ["a"] needs section
// "a" to exist — tests focused on the tracks/samples/instruments blocks
// shouldn't need to boilerplate a sections block themselves.
const defaultSectionBlock = `
[[sections]]
name = "a"
bars = 1
parts = {}
`

// writeSong writes a song.toml plus a minimal "a.pat" (1 bar, 4 cells of
// rests) to a temp dir and returns the song.toml path. Lets each test
// focus on the tracks / sections block. The section "a" is used by every
// test's arrangement and binds no tracks — tests that need specific parts
// should write extra patterns and declare a different section themselves.
func writeSong(t *testing.T, toml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	patBody := "bars 1\nresolution 1\n\n. . . .\n"
	if err := os.WriteFile(filepath.Join(dir, "patterns/a.pat"), []byte(patBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toml, "[[sections]]") {
		// Insert just before [song] so the section is declared before the
		// arrangement that references it.
		if idx := strings.Index(toml, "[song]"); idx >= 0 {
			toml = toml[:idx] + defaultSectionBlock + "\n" + toml[idx:]
		} else {
			toml += defaultSectionBlock
		}
	}
	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_TracksSchemaRoundTrip(t *testing.T) {
	toml := `[project]
title = "t"
bpm = 120

[[samples]]
id = "k"
path = "samples/k.wav"

[[instruments]]
id = "bass"
kind = "midi"
channel = 3
program = 33

[[tracks]]
id = "k"
name = "Kick"
sample = "k"
channel = 10
note = 36

[[tracks]]
id = "bass"
name = "Bass"
instrument = "bass"
channel = 3

[song]
arrangement = ["a"]
`
	s, err := Load(writeSong(t, toml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(s.Tracks))
	}
	k := s.Tracks["k"]
	if k.Sample != "k" || k.Channel != 10 || k.Note != 36 || k.Name != "Kick" {
		t.Errorf("kick track = %+v", k)
	}
	b := s.Tracks["bass"]
	if b.Instrument != "bass" || b.Channel != 3 || b.Name != "Bass" {
		t.Errorf("bass track = %+v", b)
	}
	// TrackOrder preserves declaration order.
	if want := []string{"k", "bass"}; !equalStrings(s.TrackOrder, want) {
		t.Errorf("TrackOrder = %v, want %v", s.TrackOrder, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLoad_TracksOptional(t *testing.T) {
	// A song without any [[tracks]] must still load — backwards compat.
	toml := `[project]
title = "t"
bpm = 120

[[samples]]
id = "k"
path = "samples/k.wav"

[song]
arrangement = ["a"]
`
	s, err := Load(writeSong(t, toml))
	if err != nil {
		t.Fatalf("Load without tracks: %v", err)
	}
	if len(s.Tracks) != 0 {
		t.Errorf("expected empty tracks map, got %+v", s.Tracks)
	}
}

func TestLoad_TrackChannelDefault(t *testing.T) {
	toml := `[project]
bpm = 120

[[instruments]]
id = "bass"
kind = "midi"
channel = 1
program = 33

[[tracks]]
id = "bass"
instrument = "bass"

[song]
arrangement = ["a"]
`
	s, err := Load(writeSong(t, toml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Tracks["bass"].Channel; got != 1 {
		t.Errorf("channel = %d, want default 1", got)
	}
}

func TestLoad_TrackValidationErrors(t *testing.T) {
	base := func(trackBlock string) string {
		return `[project]
bpm = 120

[[samples]]
id = "k"
path = "s/k.wav"

[[instruments]]
id = "bass"
kind = "midi"
channel = 1
program = 33

` + trackBlock + `

[song]
arrangement = ["a"]
`
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "both_instrument_and_sample",
			body: `[[tracks]]
id = "x"
instrument = "bass"
sample = "k"
note = 36`,
			want: "exactly one of instrument or sample",
		},
		{
			name: "neither_instrument_nor_sample",
			body: `[[tracks]]
id = "x"
name = "nothing"`,
			want: "exactly one of instrument or sample",
		},
		{
			name: "unknown_instrument",
			body: `[[tracks]]
id = "x"
instrument = "ghost"`,
			want: "unknown instrument",
		},
		{
			name: "unknown_sample",
			body: `[[tracks]]
id = "x"
sample = "ghost"
note = 36`,
			want: "unknown sample",
		},
		{
			name: "channel_out_of_range",
			body: `[[tracks]]
id = "x"
instrument = "bass"
channel = 17`,
			want: "channel 17 out of range",
		},
		{
			name: "sample_missing_note",
			body: `[[tracks]]
id = "x"
sample = "k"`,
			want: "must specify note",
		},
		{
			name: "note_out_of_range",
			body: `[[tracks]]
id = "x"
sample = "k"
note = 200`,
			want: "note 200 out of range",
		},
		{
			name: "duplicate_id",
			body: `[[tracks]]
id = "x"
instrument = "bass"

[[tracks]]
id = "x"
sample = "k"
note = 36`,
			want: "duplicate track id",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(writeSong(t, base(c.body)))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}
