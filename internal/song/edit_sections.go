package song

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// CreateNewSong writes a minimal project at dir: a song.toml with [project]
// (title, bpm, time_signature) and an empty [song].arrangement, plus an
// empty patterns/ subdirectory. dir must be empty or not exist. Returns
// the path to song.toml on success.
func CreateNewSong(dir, title string, bpm int, timeSig string) (string, error) {
	if bpm < 20 || bpm > 400 {
		return "", fmt.Errorf("bpm %d out of range 20..400", bpm)
	}
	if timeSig == "" {
		timeSig = "4/4"
	}
	if err := ensureEmptyOrNew(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "patterns"), 0o755); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[project]\n")
	if title != "" {
		b.WriteString("title          = " + quote(title) + "\n")
	}
	fmt.Fprintf(&b, "bpm            = %d\n", bpm)
	b.WriteString("time_signature = " + quote(timeSig) + "\n\n")
	b.WriteString("[song]\n")
	b.WriteString("arrangement = []\n")

	path := filepath.Join(dir, "song.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func ensureEmptyOrNew(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty — refusing to overwrite", dir)
	}
	return nil
}

// AppendSection appends a new [[sections]] block and adds its name as the
// next entry in [song].arrangement. Parts starts empty. Caller is
// responsible for checking name uniqueness against the loaded song.
func AppendSection(path, name string, bars int) error {
	if err := validateIdent(name, "section name"); err != nil {
		return err
	}
	if bars <= 0 {
		return fmt.Errorf("bars must be > 0")
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		content = cleanTrailingBlankLines(content)
		var b strings.Builder
		b.WriteString(content)
		b.WriteString("\n\n")
		b.WriteString(formatBlock("[[sections]]", []tomlField{
			{"name", quote(name)},
			{"bars", fmt.Sprintf("%d", bars)},
			{"parts", "{}"},
		}))
		out := b.String() + "\n"

		out, err := mutateArrangement(out, func(entries []string) ([]string, error) {
			return append(entries, quote(name)), nil
		})
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	})
}

// RemoveArrangementSlot removes the idx-th entry from [song].arrangement.
// The [[sections]] library entry is left intact so sections can be
// re-added to the arrangement later.
func RemoveArrangementSlot(path string, idx int) error {
	return editAtomically(path, func(src []byte) ([]byte, error) {
		out, err := mutateArrangement(string(src), func(entries []string) ([]string, error) {
			if idx < 0 || idx >= len(entries) {
				return nil, fmt.Errorf("arrangement index %d out of range 0..%d", idx, len(entries)-1)
			}
			return slices.Delete(entries, idx, idx+1), nil
		})
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	})
}

// MoveArrangementSlot moves the entry at from to toward to, shifting
// other entries accordingly. Negative or out-of-range indices error.
func MoveArrangementSlot(path string, from, to int) error {
	return editAtomically(path, func(src []byte) ([]byte, error) {
		out, err := mutateArrangement(string(src), func(entries []string) ([]string, error) {
			if from < 0 || from >= len(entries) {
				return nil, fmt.Errorf("from index %d out of range", from)
			}
			if to < 0 || to >= len(entries) {
				return nil, fmt.Errorf("to index %d out of range", to)
			}
			if from == to {
				return entries, nil
			}
			entry := entries[from]
			entries = slices.Delete(entries, from, from+1)
			entries = slices.Insert(entries, to, entry)
			return entries, nil
		})
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	})
}

// SetArrangementRepeat rewrites the idx-th arrangement entry to include
// "*N" (repeat) shorthand. N must be >= 1; N == 1 emits the bare name.
func SetArrangementRepeat(path string, idx, n int) error {
	if n < 1 {
		return fmt.Errorf("repeat must be >= 1")
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		out, err := mutateArrangement(string(src), func(entries []string) ([]string, error) {
			if idx < 0 || idx >= len(entries) {
				return nil, fmt.Errorf("arrangement index %d out of range", idx)
			}
			name, _, err := splitSlotQuoted(entries[idx])
			if err != nil {
				return nil, err
			}
			if n == 1 {
				entries[idx] = quote(name)
			} else {
				entries[idx] = quote(name + "*" + strconv.Itoa(n))
			}
			return entries, nil
		})
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	})
}

// AppendSectionPart sets section.parts[trackID] = patternName. If the
// parts inline-table already has an entry for trackID it's overwritten.
// Returns an error if the named section can't be found in the file.
func AppendSectionPart(path, sectionName, trackID, patternName string) error {
	if err := validateIdent(trackID, "track id"); err != nil {
		return err
	}
	if patternName == "" {
		return fmt.Errorf("pattern name must not be empty")
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		secStart, secEnd, err := findSectionBlock(lines, sectionName)
		if err != nil {
			return nil, err
		}

		partsLine := -1
		for i := secStart; i < secEnd; i++ {
			if partsLineRe.MatchString(lines[i]) {
				partsLine = i
				break
			}
		}
		if partsLine < 0 {
			// Insert a parts line at the end of the section block.
			lines = slices.Insert(lines, secEnd, fmt.Sprintf("parts = { %s = %s }", trackID, quote(patternName)))
		} else {
			updated, err := mergePartsLine(lines[partsLine], trackID, patternName)
			if err != nil {
				return nil, err
			}
			lines[partsLine] = updated
		}

		out := strings.Join(lines, "\n")
		if trailingNL {
			out += "\n"
		}
		return []byte(out), nil
	})
}

// RemoveSectionPart drops trackID from a section's parts inline table.
// Missing keys are a no-op. The section must exist or ErrSectionNotFound-
// style error is returned.
func RemoveSectionPart(path, sectionName, trackID string) error {
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		secStart, secEnd, err := findSectionBlock(lines, sectionName)
		if err != nil {
			return nil, err
		}
		partsLine := -1
		for i := secStart; i < secEnd; i++ {
			if partsLineRe.MatchString(lines[i]) {
				partsLine = i
				break
			}
		}
		if partsLine < 0 {
			// No parts line at all — nothing to remove.
			return []byte(content), nil
		}
		updated, err := removePartKey(lines[partsLine], trackID)
		if err != nil {
			return nil, err
		}
		lines[partsLine] = updated

		out := strings.Join(lines, "\n")
		if trailingNL {
			out += "\n"
		}
		return []byte(out), nil
	})
}

// DeleteTrack removes the [[tracks]] block whose id matches trackID and
// strips trackID from every section's parts inline table. Referenced
// [[samples]] / [[instruments]] entries are left alone — they may be
// shared with other tracks. Pattern files on disk are not touched so
// users can recover recordings after an accidental delete.
func DeleteTrack(path, trackID string) error {
	if err := validateIdent(trackID, "track id"); err != nil {
		return err
	}
	return editAtomically(path, func(src []byte) ([]byte, error) {
		content := string(src)
		trailingNL := strings.HasSuffix(content, "\n")
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

		lines = removeTrackBlock(lines, trackID)
		lines = stripTrackFromParts(lines, trackID)

		out := strings.Join(lines, "\n")
		if trailingNL {
			out += "\n"
		}
		return []byte(out), nil
	})
}

// --- Internal arrangement/section helpers ----------------------------------

var (
	// arrangementLineRe matches `arrangement = [<body>]` on a single line
	// (with optional leading whitespace and trailing comment).
	arrangementLineRe = regexp.MustCompile(`^(\s*arrangement\s*=\s*\[)(.*)(\].*)$`)

	// partsLineRe matches `parts = {<body>}` inline tables on a single line.
	partsLineRe = regexp.MustCompile(`^(\s*parts\s*=\s*\{)(.*)(\}.*)$`)

	// nameLineRe matches `name = "<value>"`.
	nameLineRe = regexp.MustCompile(`^\s*name\s*=\s*"([^"]*)"`)

	// idLineRe matches `id = "<value>"` inside a block.
	idLineRe = regexp.MustCompile(`^\s*id\s*=\s*"([^"]*)"`)
)

// mutateArrangement applies fn to the arrangement entries of the TOML
// source. Entries are returned in their on-file form (including quotes).
// Handles an `arrangement = []` (empty) or `arrangement = ["a", "b"]`
// on a single line. Multi-line arrangements are rejected with a clear
// error so the caller can hand-edit. If the source has no [song].arrangement
// at all, an empty one is synthesised before fn runs.
func mutateArrangement(src string, fn func([]string) ([]string, error)) (string, error) {
	trailingNL := strings.HasSuffix(src, "\n")
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")

	arrLine := -1
	for i, line := range lines {
		if arrangementLineRe.MatchString(line) {
			arrLine = i
			break
		}
	}

	if arrLine < 0 {
		// No arrangement line yet — ensure [song] exists, then append one.
		songHeader := -1
		for i, line := range lines {
			if strings.TrimSpace(line) == "[song]" {
				songHeader = i
				break
			}
		}
		if songHeader < 0 {
			lines = append(lines, "", "[song]", "arrangement = []")
		} else {
			lines = slices.Insert(lines, songHeader+1, "arrangement = []")
		}
		// Re-locate.
		for i, line := range lines {
			if arrangementLineRe.MatchString(line) {
				arrLine = i
				break
			}
		}
	}

	m := arrangementLineRe.FindStringSubmatch(lines[arrLine])
	prefix, body, suffix := m[1], m[2], m[3]
	entries := splitArrangementEntries(body)
	entries, err := fn(entries)
	if err != nil {
		return "", err
	}
	lines[arrLine] = prefix + joinArrangementEntries(entries) + suffix

	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out, nil
}

// splitArrangementEntries splits the body of an `arrangement = [...]` list
// into its individual quoted entries, preserving the quotes. Accepts an
// empty body and any whitespace around commas. Unquoted or malformed
// entries are returned verbatim (so callers can validate or rewrite them).
func splitArrangementEntries(body string) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(body, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinArrangementEntries(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	return strings.Join(entries, ", ")
}

// splitSlotQuoted mirrors splitSlot but works on the on-file form of an
// arrangement entry, e.g. `"verse*4"` -> ("verse", 4). Returns an error
// on unquoted or malformed input.
func splitSlotQuoted(entry string) (string, int, error) {
	entry = strings.TrimSpace(entry)
	if len(entry) < 2 || entry[0] != '"' || entry[len(entry)-1] != '"' {
		return "", 0, fmt.Errorf("arrangement entry %q is not a quoted string", entry)
	}
	return splitSlot(entry[1 : len(entry)-1])
}

// findSectionBlock returns the line range [start, end) covering the
// `[[sections]]` block whose `name = "<name>"` matches. start is the
// `[[sections]]` header line, end is the line past the last member line
// (i.e. the next blank line, next table header, or EOF).
func findSectionBlock(lines []string, name string) (int, int, error) {
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[sections]]" {
			continue
		}
		end := len(lines)
		gotName := ""
		for j := i + 1; j < len(lines); j++ {
			trim := strings.TrimSpace(lines[j])
			if trim == "" || isTableHeader(trim) {
				end = j
				break
			}
			if m := nameLineRe.FindStringSubmatch(lines[j]); m != nil {
				gotName = m[1]
			}
		}
		if gotName == name {
			return i, end, nil
		}
	}
	return 0, 0, fmt.Errorf("section %q not found", name)
}

// mergePartsLine rewrites a `parts = { ... }` inline-table line to set
// trackID = patternName. If trackID is already present its value is
// overwritten; otherwise the new pair is appended. Preserves the prefix
// and suffix of the original line (indentation, trailing comment, etc.).
func mergePartsLine(line, trackID, patternName string) (string, error) {
	m := partsLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", fmt.Errorf("expected parts = {...} on a single line, got %q", line)
	}
	prefix, body, suffix := m[1], m[2], m[3]

	pairs := splitInlineTablePairs(body)
	replaced := false
	for i, p := range pairs {
		if p.key == trackID {
			pairs[i].value = quote(patternName)
			replaced = true
			break
		}
	}
	if !replaced {
		pairs = append(pairs, inlinePair{key: trackID, value: quote(patternName)})
	}

	var b strings.Builder
	if len(pairs) > 0 {
		b.WriteByte(' ')
	}
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.key)
		b.WriteString(" = ")
		b.WriteString(p.value)
	}
	if len(pairs) > 0 {
		b.WriteByte(' ')
	}
	return prefix + b.String() + suffix, nil
}

type inlinePair struct {
	key, value string
}

// removePartKey rewrites a `parts = {...}` line by dropping the given
// trackID key. If the key isn't present the line is returned unchanged.
func removePartKey(line, trackID string) (string, error) {
	m := partsLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", fmt.Errorf("expected parts = {...} on a single line, got %q", line)
	}
	prefix, body, suffix := m[1], m[2], m[3]
	pairs := splitInlineTablePairs(body)
	kept := pairs[:0]
	for _, p := range pairs {
		if p.key != trackID {
			kept = append(kept, p)
		}
	}

	var b strings.Builder
	if len(kept) > 0 {
		b.WriteByte(' ')
	}
	for i, p := range kept {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.key)
		b.WriteString(" = ")
		b.WriteString(p.value)
	}
	if len(kept) > 0 {
		b.WriteByte(' ')
	}
	return prefix + b.String() + suffix, nil
}

// removeTrackBlock walks lines looking for `[[tracks]]` blocks; when it
// finds one whose `id = "<trackID>"` matches, it drops the block plus
// one trailing blank line (for cosmetic neatness). Only the first match
// is removed — duplicate ids are a loader error so there can't be two.
func removeTrackBlock(lines []string, trackID string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != "[[tracks]]" {
			out = append(out, lines[i])
			i++
			continue
		}
		// Scan forward to the end of this block (next blank line or
		// table header) and capture the id.
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
		if gotID != trackID {
			// Not the block we want — keep it and move past.
			out = append(out, lines[i:end]...)
			i = end
			continue
		}
		// Drop the block and optionally a trailing blank line.
		skipTo := end
		if skipTo < len(lines) && strings.TrimSpace(lines[skipTo]) == "" {
			skipTo++
		}
		i = skipTo
	}
	return out
}

// stripTrackFromParts rewrites every `parts = {...}` line in the file
// to drop the trackID key. Errors on individual lines are ignored so
// one malformed line doesn't prevent the rest of the delete.
func stripTrackFromParts(lines []string, trackID string) []string {
	for i, line := range lines {
		if !partsLineRe.MatchString(line) {
			continue
		}
		if updated, err := removePartKey(line, trackID); err == nil {
			lines[i] = updated
		}
	}
	return lines
}

// splitInlineTablePairs breaks a `{ k = "v", m = "n" }` body (what's
// between the braces) into its key/value pairs, preserving the on-file
// form of each value (quotes included).
func splitInlineTablePairs(body string) []inlinePair {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var out []inlinePair
	for _, part := range strings.Split(body, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		out = append(out, inlinePair{
			key:   strings.TrimSpace(p[:eq]),
			value: strings.TrimSpace(p[eq+1:]),
		})
	}
	return out
}
