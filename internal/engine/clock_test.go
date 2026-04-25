package engine

import (
	"testing"
	"time"
)

func TestElapsedToPosition(t *testing.T) {
	c := Clock{BPM: 120, BeatsPerBar: 4}
	// 120 BPM = 2 beats/sec; 1 second = 2 beats = beat 3 of bar 1 (0-indexed 2).
	p := c.ElapsedToPosition(time.Second)
	if p.Bar != 1 || p.Beat != 3 || p.Tick != 0 {
		t.Errorf("at 1s @120bpm: got %+v, want bar=1 beat=3 tick=0", p)
	}
	// 2 seconds = 4 beats = bar 2 beat 1
	p = c.ElapsedToPosition(2 * time.Second)
	if p.Bar != 2 || p.Beat != 1 {
		t.Errorf("at 2s @120bpm: got %+v, want bar=2 beat=1", p)
	}
	// 0 = bar 1 beat 1
	p = c.ElapsedToPosition(0)
	if p.Bar != 1 || p.Beat != 1 || p.Tick != 0 {
		t.Errorf("at 0s: got %+v, want bar=1 beat=1 tick=0", p)
	}
}

func TestParseBeatsPerBar(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"4/4", 4},
		{"3/4", 3},
		{"6/8", 6},
		{"7/8", 7},
		{"", 0},
		{"bad", 0},
	} {
		if got := parseBeatsPerBar(c.in); got != c.want {
			t.Errorf("parseBeatsPerBar(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
