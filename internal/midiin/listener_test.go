package midiin

import "testing"

// TestParseCardLine covers the /proc/asound/cards line shape — the
// "[name           ]" bracketed identifier with a numeric prefix.
// Real samples from a Linux box vary in the inner padding, so we
// tolerate that and just want the trimmed name.
func TestParseCardLine(t *testing.T) {
	cases := []struct {
		in   string
		idx  int
		name string
		ok   bool
	}{
		{" 0 [sofhdadsp      ]: sof-hda-dsp - sof-hda-dsp", 0, "sofhdadsp", true},
		{" 2 [LCXL           ]: USB-Audio - Launch Control XL", 2, "LCXL", true},
		{"                      LENOVO-20WNS28C2E", 0, "", false}, // continuation line
		{"", 0, "", false},
	}
	for _, c := range cases {
		idx, name, ok := parseCardLine(c.in)
		if ok != c.ok || idx != c.idx || name != c.name {
			t.Errorf("parseCardLine(%q) = (%d, %q, %v); want (%d, %q, %v)",
				c.in, idx, name, ok, c.idx, c.name, c.ok)
		}
	}
}

// TestParseMidiName covers the kernel's "midiCxDy" device-file naming.
// The card and device numbers are what we use to correlate against
// /proc/asound/cards entries.
func TestParseMidiName(t *testing.T) {
	cases := []struct {
		in       string
		card     int
		dev      int
		ok       bool
	}{
		{"midiC0D0", 0, 0, true},
		{"midiC2D1", 2, 1, true},
		{"midiC10D5", 10, 5, true},
		{"midiC0", 0, 0, false},
		{"hwC0D0", 0, 0, false},
		{"midi", 0, 0, false},
	}
	for _, c := range cases {
		card, dev, ok := parseMidiName(c.in)
		if ok != c.ok || card != c.card || dev != c.dev {
			t.Errorf("parseMidiName(%q) = (%d, %d, %v); want (%d, %d, %v)",
				c.in, card, dev, ok, c.card, c.dev, c.ok)
		}
	}
}
