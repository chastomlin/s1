package song

import (
	"fmt"
	"regexp"
	"strings"
)

// SetTrackPan rewrites or inserts the `pan = N` line inside the named
// track's [[tracks]] block. Out-of-range values are clamped to
// [-1, 1] before writing — matches the loader's accepted range.
func SetTrackPan(path, trackID string, pan float32) error {
	if pan < -1 {
		pan = -1
	} else if pan > 1 {
		pan = 1
	}
	return setTrackScalar(path, trackID, "pan", fmt.Sprintf("%g", pan))
}

// SetTrackGain rewrites or inserts the `gain = N` line inside the
// named track's [[tracks]] block. Clamped to [0, 4].
func SetTrackGain(path, trackID string, gain float32) error {
	if gain < 0 {
		gain = 0
	} else if gain > 4 {
		gain = 4
	}
	return setTrackScalar(path, trackID, "gain", fmt.Sprintf("%g", gain))
}

// SetTrackEQ rewrites or inserts the `eq = { ... }` inline-table line
// inside the named track's [[tracks]] block, expressing the full
// 3-band config in a stable order. The leading `enabled` flag is the
// bypass toggle — written explicitly so a flipped flag round-trips.
func SetTrackEQ(path, trackID string, e EQConfig) error {
	body := fmt.Sprintf(
		"enabled = %t, low_freq = %g, low_gain = %g, mid_freq = %g, mid_q = %g, mid_gain = %g, high_freq = %g, high_gain = %g",
		e.Enabled, e.LowFreq, e.LowGain, e.MidFreq, e.MidQ, e.MidGain, e.HighFreq, e.HighGain,
	)
	return setTrackInlineTable(path, trackID, "eq", body)
}

// SetTrackComp rewrites or inserts the `comp = { ... }` inline-table
// line inside the named track's [[tracks]] block.
func SetTrackComp(path, trackID string, c CompConfig) error {
	body := fmt.Sprintf(
		"enabled = %t, threshold_db = %g, ratio = %g, attack_ms = %g, release_ms = %g, makeup_db = %g",
		c.Enabled, c.ThresholdDB, c.Ratio, c.AttackMs, c.ReleaseMs, c.MakeupDB,
	)
	return setTrackInlineTable(path, trackID, "comp", body)
}

// SetTrackFilter rewrites or inserts the `filter = { ... }` inline-table.
// Type defaults to lowpass when the caller hands a zero-value config.
func SetTrackFilter(path, trackID string, f FilterConfig) error {
	if f.Type == "" {
		f.Type = FilterLowpass
	}
	body := fmt.Sprintf(
		"enabled = %t, type = %q, cutoff = %g, resonance = %g",
		f.Enabled, string(f.Type), f.Cutoff, f.Resonance,
	)
	return setTrackInlineTable(path, trackID, "filter", body)
}

// SetTrackLofi rewrites or inserts the `lofi = { ... }` inline-table.
func SetTrackLofi(path, trackID string, l LofiConfig) error {
	body := fmt.Sprintf(
		"enabled = %t, bits = %d, rate = %g",
		l.Enabled, l.Bits, l.Rate,
	)
	return setTrackInlineTable(path, trackID, "lofi", body)
}

// SetTrackDrive rewrites or inserts the `drive = { ... }` inline-table.
// Type defaults to soft when the caller hands a zero-value config.
func SetTrackDrive(path, trackID string, d DriveConfig) error {
	if d.Type == "" {
		d.Type = DriveSoft
	}
	body := fmt.Sprintf(
		"enabled = %t, type = %q, drive = %g, tone = %g, level = %g",
		d.Enabled, string(d.Type), d.Drive, d.Tone, d.Level,
	)
	return setTrackInlineTable(path, trackID, "drive", body)
}

// SetTrackReverb rewrites or inserts the `reverb = { ... }` inline-table.
func SetTrackReverb(path, trackID string, r ReverbConfig) error {
	body := fmt.Sprintf(
		"enabled = %t, size = %g, damping = %g, mix = %g",
		r.Enabled, r.Size, r.Damping, r.Mix,
	)
	return setTrackInlineTable(path, trackID, "reverb", body)
}

// --- Internals ---

// trackBlockBounds locates the [[tracks]] block whose `id = "<id>"`
// matches. Returns [start, end) line indices. The end is exclusive
// and points at the next blank line / table header / EOF.
func trackBlockBounds(lines []string, trackID string) (int, int, error) {
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[tracks]]" {
			continue
		}
		end := len(lines)
		gotID := ""
		for j := i + 1; j < len(lines); j++ {
			trim := strings.TrimSpace(lines[j])
			if trim == "" || isTableHeader(trim) {
				end = j
				break
			}
			if m := idLineRe.FindStringSubmatch(lines[j]); m != nil {
				gotID = m[1]
			}
		}
		if gotID == trackID {
			return i, end, nil
		}
	}
	return 0, 0, fmt.Errorf("track %q not found", trackID)
}

// scalarKeyRe matches `<key> = <value>` lines. The value group is
// everything up to a trailing comment or end-of-line; we don't try
// to be clever about TOML data types because we always overwrite the
// whole right-hand side.
var scalarKeyRe = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`^(\s*` + regexp.QuoteMeta(key) + `\s*=\s*)(.*?)(\s*(?:#.*)?)$`)
}

// setTrackScalar replaces the scalar `key = value` line in the named
// track's block, or inserts it just before the block's end if
// missing. Trailing comments after the value are preserved.
func setTrackScalar(path, trackID, key, value string) error {
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		start, end, err := trackBlockBounds(lines, trackID)
		if err != nil {
			return nil, err
		}
		re := scalarKeyRe(key)
		for i := start + 1; i < end; i++ {
			if m := re.FindStringSubmatch(lines[i]); m != nil {
				lines[i] = m[1] + value + m[3]
				return []byte(joinLines(lines, trailingNL)), nil
			}
		}
		// Not present — insert at the end of the block. The trackBlockBounds
		// `end` is the line just past the block; insert there so the new
		// line lands inside the block.
		insertion := key + " = " + value
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:end]...)
		newLines = append(newLines, insertion)
		newLines = append(newLines, lines[end:]...)
		return []byte(joinLines(newLines, trailingNL)), nil
	})
}

// inlineTableLineRe matches `<key> = { ... }` on a single line and
// captures (prefix, body, suffix) so callers can rewrite the body
// while preserving the original indentation and any trailing comment.
var inlineTableLineRe = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`^(\s*` + regexp.QuoteMeta(key) + `\s*=\s*\{)(.*)(\}.*)$`)
}

// setTrackInlineTable replaces the `key = { ... }` line in the named
// track's block. Inserts the line if absent. The body is the comma-
// separated key/value pairs the caller wants between the braces.
func setTrackInlineTable(path, trackID, key, body string) error {
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		start, end, err := trackBlockBounds(lines, trackID)
		if err != nil {
			return nil, err
		}
		re := inlineTableLineRe(key)
		for i := start + 1; i < end; i++ {
			if m := re.FindStringSubmatch(lines[i]); m != nil {
				lines[i] = m[1] + " " + body + " " + m[3]
				return []byte(joinLines(lines, trailingNL)), nil
			}
		}
		// Not present — insert at the end of the block.
		insertion := key + " = { " + body + " }"
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:end]...)
		newLines = append(newLines, insertion)
		newLines = append(newLines, lines[end:]...)
		return []byte(joinLines(newLines, trailingNL)), nil
	})
}

func joinLines(lines []string, trailingNL bool) string {
	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}
