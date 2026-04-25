package song

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var bpmLineRe = regexp.MustCompile(`^(\s*bpm\s*=\s*)(\d+)(.*)$`)

// SetBPM updates [project].bpm in song.toml at path to the given value.
// If the file has a [project] section with no bpm line, one is inserted
// immediately after the header. If the file has no [project] section at
// all, an error is returned (that song is malformed). Comments and other
// blocks are preserved verbatim. Write is atomic (temp file + rename).
func SetBPM(path string, bpm int) error {
	if bpm < 20 || bpm > 400 {
		return fmt.Errorf("bpm %d out of range 20..400", bpm)
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		var (
			currentSection string
			projectStart   = -1
			bpmLine        = -1
		)
		for i, line := range lines {
			trim := strings.TrimSpace(line)
			if isTableHeader(trim) {
				if trim == "[project]" {
					currentSection = "[project]"
					projectStart = i
				} else {
					currentSection = trim
				}
				continue
			}
			if currentSection == "[project]" && bpmLine < 0 {
				if bpmLineRe.MatchString(line) {
					bpmLine = i
				}
			}
		}

		if projectStart < 0 {
			return nil, fmt.Errorf("no [project] section in %s", path)
		}
		if bpmLine >= 0 {
			m := bpmLineRe.FindStringSubmatch(lines[bpmLine])
			lines[bpmLine] = m[1] + fmt.Sprintf("%d", bpm) + m[3]
		} else {
			lines = slices.Insert(lines, projectStart+1, fmt.Sprintf("bpm = %d", bpm))
		}

		out := strings.Join(lines, "\n")
		if trailingNL {
			out += "\n"
		}
		return []byte(out), nil
	})
}

// AppendSampleTrack appends a new [[samples]] and [[tracks]] pair to the
// song.toml at path. The track binds to the new sample, is routed on
// channel 10 (GM drum channel), and triggers the given MIDI note. Caller
// is responsible for checking that id is unique against the loaded song.
func AppendSampleTrack(path, id, samplePath string, note int) error {
	if err := validateIdent(id, "track id"); err != nil {
		return err
	}
	if note < 0 || note > 127 {
		return fmt.Errorf("note %d out of range 0..127", note)
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		var b strings.Builder
		b.WriteString(cleanTrailingBlankLines(string(src)))
		b.WriteString("\n\n")
		b.WriteString(formatBlock("[[samples]]", []tomlField{
			{"id", quote(id)},
			{"path", quote(samplePath)},
		}))
		b.WriteString("\n")
		b.WriteString(formatBlock("[[tracks]]", []tomlField{
			{"id", quote(id)},
			{"name", quote(capitalize(id))},
			{"sample", quote(id)},
			{"channel", "10"},
			{"note", fmt.Sprintf("%d", note)},
		}))
		return []byte(b.String()), nil
	})
}

// AppendInstrumentTrack appends a new [[instruments]] and [[tracks]] pair.
// The instrument is MIDI with the given channel and GM program; the track
// routes to that instrument on the same channel.
func AppendInstrumentTrack(path, id string, channel, program int) error {
	if err := validateIdent(id, "track id"); err != nil {
		return err
	}
	if channel < 1 || channel > 16 {
		return fmt.Errorf("channel %d out of range 1..16", channel)
	}
	if program < 0 || program > 127 {
		return fmt.Errorf("program %d out of range 0..127", program)
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		var b strings.Builder
		b.WriteString(cleanTrailingBlankLines(string(src)))
		b.WriteString("\n\n")
		b.WriteString(formatBlock("[[instruments]]", []tomlField{
			{"id", quote(id)},
			{"kind", quote("midi")},
			{"channel", fmt.Sprintf("%d", channel)},
			{"program", fmt.Sprintf("%d", program)},
		}))
		b.WriteString("\n")
		b.WriteString(formatBlock("[[tracks]]", []tomlField{
			{"id", quote(id)},
			{"name", quote(capitalize(id))},
			{"instrument", quote(id)},
			{"channel", fmt.Sprintf("%d", channel)},
		}))
		return []byte(b.String()), nil
	})
}

// --- Helpers ---------------------------------------------------------------

type tomlField struct {
	key   string
	value string
}

// formatBlock renders a TOML block with column-aligned keys, matching the
// style the scaffolder emits.
func formatBlock(header string, fields []tomlField) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	max := 0
	for _, f := range fields {
		if len(f.key) > max {
			max = len(f.key)
		}
	}
	for _, f := range fields {
		b.WriteString(f.key)
		b.WriteString(strings.Repeat(" ", max-len(f.key)))
		b.WriteString(" = ")
		b.WriteString(f.value)
		b.WriteByte('\n')
	}
	return b.String()
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func isTableHeader(trim string) bool {
	return strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") && trim != "[]"
}

// cleanTrailingBlankLines trims trailing blank lines (including any run of
// "\n") but guarantees a single trailing newline before more content is
// appended. Makes the output of repeated appends stable.
func cleanTrailingBlankLines(s string) string {
	return strings.TrimRight(s, " \t\r\n")
}

// validateIdent checks an id field is non-empty and contains only
// alphanumerics, underscore, or hyphen. Keeps ids safe as keys and as
// component parts of generated file paths.
func validateIdent(id, field string) error {
	if id == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	for _, r := range id {
		if !(r == '_' || r == '-' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z')) {
			return fmt.Errorf("%s %q contains invalid character %q (use letters, digits, _, -)", field, id, r)
		}
	}
	return nil
}

// editAtomically reads path, runs fn on its bytes, and writes the result
// back via a temp file + rename in the same directory. Preserves the
// existing file's mode. A missing file is treated as empty input so
// callers can use this helper to write new files atomically too.
func editAtomically(path string, fn func([]byte) ([]byte, error)) error {
	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	dst, err := fn(src)
	if err != nil {
		return err
	}
	if bytes.Equal(src, dst) {
		return nil // nothing to do
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(dst)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}
	return os.Rename(tmpName, path)
}
