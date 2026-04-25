// Package song loads and represents a song: a TOML project file plus a
// directory of plain-text pattern files.
package song

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Song is a fully-loaded project: metadata + samples + instruments + tracks
// + patterns (single-track clips) + sections (which track plays which
// pattern for N bars) + the arrangement that sequences those sections.
type Song struct {
	Project      Project
	Samples      map[string]Sample
	Instruments  map[string]Instrument
	Tracks       map[string]Track
	TrackOrder   []string // track IDs in declaration order for deterministic UI listing
	Patterns     map[string]Pattern
	Sections     map[string]Section
	SectionOrder []string // section names in declaration order
	Arrangement  []ArrangementSlot

	// SourcePath is the song.toml that was loaded, useful for reloads.
	SourcePath string
}

// ArrangementSlot is one entry in the arrangement: a section name and the
// number of times it repeats consecutively before the next slot begins.
// TOML accepts either a bare name ("verse") or the "name*N" shorthand
// ("verse*4" → Repeat=4).
type ArrangementSlot struct {
	Section string
	Repeat  int
}

// TotalBars is this slot's contribution to song length: Section.Bars × Repeat.
func (a ArrangementSlot) TotalBars(s Section) int { return s.Bars * a.Repeat }

// Section is a named, N-bar arrangement unit. Parts binds a track ID to the
// pattern that track plays for this section; omitted tracks are silent.
// BeatsPerBar and Resolution are adopted from the referenced patterns, which
// must all agree (and must match Bars).
type Section struct {
	Name        string
	Bars        int
	BeatsPerBar int
	Resolution  int
	Parts       map[string]string // trackID -> patternName
}

type Project struct {
	Title         string
	BPM           int
	TimeSignature string
}

type Sample struct {
	ID   string
	Path string // resolved absolute path
}

type Instrument struct {
	ID      string
	Kind    string // "midi" for now
	Port    string
	Channel int
	Program int
}

// Track binds a pattern-track ID (e.g. "k", "bass") to one output: either a
// MIDI instrument (for pitched note cells) or a sample (for X cells). For
// MIDI output, Channel is 1..16.
//
// For sample tracks two note fields split concerns that would otherwise
// conflict:
//
//   - Note is the MIDI note number the MIDI bridge emits on trigger —
//     typically a General-MIDI drum note on channel 10 (36 kick, 38 snare).
//   - BaseNote is the sample's native pitch; the audio mixer transposes a
//     trigger by 2^((cellNote - BaseNote)/12) semitones. Defaults to Note
//     when the TOML omits base_note so existing songs keep their tuning.
type Track struct {
	ID         string
	Name       string
	Instrument string  // set iff this is a pitched MIDI track; refs Instruments[].ID
	Sample     string  // set iff this is a sample/drum track; refs Samples[].ID
	Channel    int     // 1..16
	Note       int     // 0..127; meaningful only for sample tracks (MIDI drum routing)
	BaseNote   int     // 0..127; sample's native pitch for varispeed transposition
	Pan        float32 // -1.0 (full left) .. 1.0 (full right). 0 = centre.
	Gain       float32 // per-track gain multiplier on top of velocity. 1.0 = unity.

	// Effect chain — both default to "off" (flat EQ, dormant comp) when
	// the song.toml omits the inline table, so adding tracks without
	// effects is unchanged.
	EQ   EQConfig
	Comp CompConfig
}

// EQConfig describes a 3-band EQ: low shelf, mid peak (parametric),
// high shelf. Frequencies are in Hz; gains in dB; Q is the bandwidth
// of the mid peak. Zero gain on a band is a unity pass-through, so
// configuring just one band leaves the others flat.
type EQConfig struct {
	LowFreq  float32
	LowGain  float32
	MidFreq  float32
	MidQ     float32
	MidGain  float32
	HighFreq float32
	HighGain float32
}

// IsActive reports whether the EQ would actually colour the signal.
// All-zero gains mean the chain can be skipped at the audio thread —
// "flat" is a no-op rather than three biquads doing nothing.
func (e EQConfig) IsActive() bool {
	return e.LowGain != 0 || e.MidGain != 0 || e.HighGain != 0
}

// CompConfig describes a feedforward compressor. Threshold in dB
// (negative); Ratio 1.0 = no compression, 4.0 = 4:1; Attack/Release in
// milliseconds; Makeup gain in dB applied after compression. Zero
// Ratio (or 1.0) keeps the compressor inactive even if the inline
// table is present, so it's easy to bypass without removing config.
type CompConfig struct {
	ThresholdDB float32
	Ratio       float32
	AttackMs    float32
	ReleaseMs   float32
	MakeupDB    float32
}

// IsActive reports whether the compressor would actually reduce gain.
// Ratio ≤ 1 means "no compression" — the chain skips it.
func (c CompConfig) IsActive() bool {
	return c.Ratio > 1.0
}

// --- TOML binding (internal) ------------------------------------------------

type fileProject struct {
	Title         string `toml:"title"`
	BPM           int    `toml:"bpm"`
	TimeSignature string `toml:"time_signature"`
}

type fileSample struct {
	ID   string `toml:"id"`
	Path string `toml:"path"`
}

type fileInstrument struct {
	ID      string `toml:"id"`
	Kind    string `toml:"kind"`
	Port    string `toml:"port"`
	Channel int    `toml:"channel"`
	Program int    `toml:"program"`
}

type fileTrack struct {
	ID         string   `toml:"id"`
	Name       string   `toml:"name"`
	Instrument string   `toml:"instrument"`
	Sample     string   `toml:"sample"`
	Channel    *int     `toml:"channel"`   // optional, default 1
	Note       *int     `toml:"note"`      // required for sample tracks
	BaseNote   *int     `toml:"base_note"` // optional; defaults to Note for sample tracks
	Pan        *float64 `toml:"pan"`       // optional, default 0.0 (centre)
	Gain       *float64 `toml:"gain"`      // optional, default 1.0 (unity)
	EQ         *fileEQ  `toml:"eq"`
	Comp       *fileComp `toml:"comp"`
}

type fileEQ struct {
	LowFreq  *float64 `toml:"low_freq"`
	LowGain  *float64 `toml:"low_gain"`
	MidFreq  *float64 `toml:"mid_freq"`
	MidQ     *float64 `toml:"mid_q"`
	MidGain  *float64 `toml:"mid_gain"`
	HighFreq *float64 `toml:"high_freq"`
	HighGain *float64 `toml:"high_gain"`
}

type fileComp struct {
	ThresholdDB *float64 `toml:"threshold_db"`
	Ratio       *float64 `toml:"ratio"`
	AttackMs    *float64 `toml:"attack_ms"`
	ReleaseMs   *float64 `toml:"release_ms"`
	MakeupDB    *float64 `toml:"makeup_db"`
}

type fileSection struct {
	Name  string            `toml:"name"`
	Bars  int               `toml:"bars"`
	Parts map[string]string `toml:"parts"`
}

type fileSongDoc struct {
	Project     fileProject      `toml:"project"`
	Samples     []fileSample     `toml:"samples"`
	Instruments []fileInstrument `toml:"instruments"`
	Tracks      []fileTrack      `toml:"tracks"`
	Sections    []fileSection    `toml:"sections"`
	Song        struct {
		Arrangement []string `toml:"arrangement"`
		PatternDir  string   `toml:"pattern_dir"` // optional, default "patterns"
	} `toml:"song"`
}

// Load reads a song.toml and the associated pattern files.
func Load(path string) (*Song, error) {
	var doc fileSongDoc
	md, err := toml.DecodeFile(path, &doc)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown keys in %s: %s", path, strings.Join(keys, ", "))
	}

	if doc.Project.BPM <= 0 {
		return nil, fmt.Errorf("%s: project.bpm must be > 0", path)
	}
	if doc.Project.TimeSignature == "" {
		doc.Project.TimeSignature = "4/4"
	}

	arrangement, err := parseArrangement(doc.Song.Arrangement)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	s := &Song{
		Project: Project{
			Title:         doc.Project.Title,
			BPM:           doc.Project.BPM,
			TimeSignature: doc.Project.TimeSignature,
		},
		Samples:     map[string]Sample{},
		Instruments: map[string]Instrument{},
		Tracks:      map[string]Track{},
		Patterns:    map[string]Pattern{},
		Sections:    map[string]Section{},
		Arrangement: arrangement,
		SourcePath:  path,
	}

	baseDir := filepath.Dir(path)

	for _, fs := range doc.Samples {
		if fs.ID == "" {
			return nil, fmt.Errorf("%s: sample with empty id", path)
		}
		if _, dup := s.Samples[fs.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate sample id %q", path, fs.ID)
		}
		sp := fs.Path
		if !filepath.IsAbs(sp) {
			sp = filepath.Join(baseDir, sp)
		}
		s.Samples[fs.ID] = Sample{ID: fs.ID, Path: sp}
	}

	for _, fi := range doc.Instruments {
		if fi.ID == "" {
			return nil, fmt.Errorf("%s: instrument with empty id", path)
		}
		if _, dup := s.Instruments[fi.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate instrument id %q", path, fi.ID)
		}
		s.Instruments[fi.ID] = Instrument{
			ID:      fi.ID,
			Kind:    fi.Kind,
			Port:    fi.Port,
			Channel: fi.Channel,
			Program: fi.Program,
		}
	}

	for _, ft := range doc.Tracks {
		if ft.ID == "" {
			return nil, fmt.Errorf("%s: track with empty id", path)
		}
		if _, dup := s.Tracks[ft.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate track id %q", path, ft.ID)
		}
		hasInst := ft.Instrument != ""
		hasSample := ft.Sample != ""
		if hasInst == hasSample {
			return nil, fmt.Errorf("%s: track %q must reference exactly one of instrument or sample", path, ft.ID)
		}
		if hasInst {
			if _, ok := s.Instruments[ft.Instrument]; !ok {
				return nil, fmt.Errorf("%s: track %q references unknown instrument %q", path, ft.ID, ft.Instrument)
			}
		} else {
			if _, ok := s.Samples[ft.Sample]; !ok {
				return nil, fmt.Errorf("%s: track %q references unknown sample %q", path, ft.ID, ft.Sample)
			}
		}
		channel := 1
		if ft.Channel != nil {
			channel = *ft.Channel
			if channel < 1 || channel > 16 {
				return nil, fmt.Errorf("%s: track %q channel %d out of range 1..16", path, ft.ID, channel)
			}
		}
		note := 0
		if ft.Note != nil {
			note = *ft.Note
			if note < 0 || note > 127 {
				return nil, fmt.Errorf("%s: track %q note %d out of range 0..127", path, ft.ID, note)
			}
		} else if hasSample {
			return nil, fmt.Errorf("%s: sample track %q must specify note", path, ft.ID)
		}
		baseNote := note
		if ft.BaseNote != nil {
			baseNote = *ft.BaseNote
			if baseNote < 0 || baseNote > 127 {
				return nil, fmt.Errorf("%s: track %q base_note %d out of range 0..127", path, ft.ID, baseNote)
			}
		}
		pan := 0.0
		if ft.Pan != nil {
			pan = *ft.Pan
			if pan < -1.0 || pan > 1.0 {
				return nil, fmt.Errorf("%s: track %q pan %.3f out of range -1.0..1.0", path, ft.ID, pan)
			}
		}
		gain := 1.0
		if ft.Gain != nil {
			gain = *ft.Gain
			if gain < 0 || gain > 4.0 {
				return nil, fmt.Errorf("%s: track %q gain %.3f out of range 0..4.0", path, ft.ID, gain)
			}
		}
		eq, err := parseEQ(ft.EQ, ft.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		comp, err := parseComp(ft.Comp, ft.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.Tracks[ft.ID] = Track{
			ID:         ft.ID,
			Name:       ft.Name,
			Instrument: ft.Instrument,
			Sample:     ft.Sample,
			Channel:    channel,
			Note:       note,
			BaseNote:   baseNote,
			Pan:        float32(pan),
			Gain:       float32(gain),
			EQ:         eq,
			Comp:       comp,
		}
		s.TrackOrder = append(s.TrackOrder, ft.ID)
	}

	patternDir := doc.Song.PatternDir
	if patternDir == "" {
		patternDir = "patterns"
	}
	if !filepath.IsAbs(patternDir) {
		patternDir = filepath.Join(baseDir, patternDir)
	}

	entries, err := os.ReadDir(patternDir)
	if err != nil {
		return nil, fmt.Errorf("read pattern dir %s: %w", patternDir, err)
	}
	// Read in sorted filename order so repeated loads produce identical
	// Patterns maps — the filesystem may return entries in any order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pat") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".pat")
		f, err := os.Open(filepath.Join(patternDir, e.Name()))
		if err != nil {
			return nil, err
		}
		pat, err := ParsePattern(name, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("pattern %s: %w", e.Name(), err)
		}
		s.Patterns[name] = pat
	}

	projectBPB := parseBPBFromTimeSig(doc.Project.TimeSignature)
	if projectBPB <= 0 {
		projectBPB = 4
	}

	for _, fs := range doc.Sections {
		if fs.Name == "" {
			return nil, fmt.Errorf("%s: section with empty name", path)
		}
		if _, dup := s.Sections[fs.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate section name %q", path, fs.Name)
		}
		if fs.Bars <= 0 {
			return nil, fmt.Errorf("%s: section %q bars must be > 0", path, fs.Name)
		}

		sec := Section{
			Name:        fs.Name,
			Bars:        fs.Bars,
			BeatsPerBar: projectBPB,
			Resolution:  4,
			Parts:       map[string]string{},
		}

		// Walk parts in sorted trackID order for deterministic validation
		// errors; the map itself is order-independent for consumers.
		ids := make([]string, 0, len(fs.Parts))
		for id := range fs.Parts {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		var adopted bool
		for _, id := range ids {
			patName := fs.Parts[id]
			if _, ok := s.Tracks[id]; !ok {
				return nil, fmt.Errorf("%s: section %q part references unknown track %q", path, fs.Name, id)
			}
			pat, ok := s.Patterns[patName]
			if !ok {
				return nil, fmt.Errorf("%s: section %q track %q references unknown pattern %q", path, fs.Name, id, patName)
			}
			if pat.Bars != fs.Bars {
				return nil, fmt.Errorf("%s: section %q track %q: pattern %q has %d bars, section has %d", path, fs.Name, id, patName, pat.Bars, fs.Bars)
			}
			if !adopted {
				sec.BeatsPerBar = pat.BeatsPerBar
				sec.Resolution = pat.Resolution
				adopted = true
			} else if pat.BeatsPerBar != sec.BeatsPerBar || pat.Resolution != sec.Resolution {
				return nil, fmt.Errorf("%s: section %q track %q: pattern %q has beats_per_bar=%d resolution=%d, section expects %d/%d", path, fs.Name, id, patName, pat.BeatsPerBar, pat.Resolution, sec.BeatsPerBar, sec.Resolution)
			}
			sec.Parts[id] = patName
		}

		s.Sections[fs.Name] = sec
		s.SectionOrder = append(s.SectionOrder, fs.Name)
	}

	for _, slot := range s.Arrangement {
		if _, ok := s.Sections[slot.Section]; !ok {
			return nil, fmt.Errorf("arrangement references unknown section %q", slot.Section)
		}
	}

	return s, nil
}

// parseArrangement expands each string entry into an ArrangementSlot. Accepts
// bare names ("verse") and the "name*N" shorthand ("verse*4", Repeat=4). N
// must be a positive integer.
func parseArrangement(raw []string) ([]ArrangementSlot, error) {
	out := make([]ArrangementSlot, 0, len(raw))
	for _, entry := range raw {
		name, rep, err := splitSlot(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, ArrangementSlot{Section: name, Repeat: rep})
	}
	return out, nil
}

func splitSlot(entry string) (string, int, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", 0, fmt.Errorf("empty arrangement entry")
	}
	star := strings.IndexByte(entry, '*')
	if star < 0 {
		return entry, 1, nil
	}
	name := strings.TrimSpace(entry[:star])
	countStr := strings.TrimSpace(entry[star+1:])
	if name == "" {
		return "", 0, fmt.Errorf("arrangement entry %q: empty section name before '*'", entry)
	}
	n, err := strconv.Atoi(countStr)
	if err != nil || n < 1 {
		return "", 0, fmt.Errorf("arrangement entry %q: repeat must be a positive integer, got %q", entry, countStr)
	}
	return name, n, nil
}

// parseEQ fills an EQConfig from the optional inline TOML table,
// applying defaults that make missing fields no-ops (centre frequency
// reasonable for the band, gain 0). Returns an error only on
// out-of-range values.
func parseEQ(in *fileEQ, trackID string) (EQConfig, error) {
	out := EQConfig{
		LowFreq:  200,
		LowGain:  0,
		MidFreq:  1000,
		MidQ:     1.0,
		MidGain:  0,
		HighFreq: 5000,
		HighGain: 0,
	}
	if in == nil {
		return out, nil
	}
	check := func(p *float64, lo, hi float64, name string) (float32, error) {
		if p == nil {
			return 0, nil
		}
		v := *p
		if v < lo || v > hi {
			return 0, fmt.Errorf("track %q eq.%s = %.3f out of range %.1f..%.1f", trackID, name, v, lo, hi)
		}
		return float32(v), nil
	}
	if in.LowFreq != nil {
		v, err := check(in.LowFreq, 20, 20000, "low_freq")
		if err != nil {
			return out, err
		}
		out.LowFreq = v
	}
	if in.LowGain != nil {
		v, err := check(in.LowGain, -36, 36, "low_gain")
		if err != nil {
			return out, err
		}
		out.LowGain = v
	}
	if in.MidFreq != nil {
		v, err := check(in.MidFreq, 20, 20000, "mid_freq")
		if err != nil {
			return out, err
		}
		out.MidFreq = v
	}
	if in.MidQ != nil {
		v, err := check(in.MidQ, 0.1, 10, "mid_q")
		if err != nil {
			return out, err
		}
		out.MidQ = v
	}
	if in.MidGain != nil {
		v, err := check(in.MidGain, -36, 36, "mid_gain")
		if err != nil {
			return out, err
		}
		out.MidGain = v
	}
	if in.HighFreq != nil {
		v, err := check(in.HighFreq, 20, 20000, "high_freq")
		if err != nil {
			return out, err
		}
		out.HighFreq = v
	}
	if in.HighGain != nil {
		v, err := check(in.HighGain, -36, 36, "high_gain")
		if err != nil {
			return out, err
		}
		out.HighGain = v
	}
	return out, nil
}

// parseComp fills a CompConfig from the optional inline TOML table.
// Defaults leave the comp dormant (ratio 1.0 = no compression) so a
// missing or empty `comp = {}` is a clean no-op.
func parseComp(in *fileComp, trackID string) (CompConfig, error) {
	out := CompConfig{
		ThresholdDB: -12,
		Ratio:       1.0,
		AttackMs:    5,
		ReleaseMs:   50,
		MakeupDB:    0,
	}
	if in == nil {
		return out, nil
	}
	check := func(p *float64, lo, hi float64, name string) (float32, error) {
		if p == nil {
			return 0, nil
		}
		v := *p
		if v < lo || v > hi {
			return 0, fmt.Errorf("track %q comp.%s = %.3f out of range %.1f..%.1f", trackID, name, v, lo, hi)
		}
		return float32(v), nil
	}
	if in.ThresholdDB != nil {
		v, err := check(in.ThresholdDB, -60, 0, "threshold_db")
		if err != nil {
			return out, err
		}
		out.ThresholdDB = v
	}
	if in.Ratio != nil {
		v, err := check(in.Ratio, 1, 20, "ratio")
		if err != nil {
			return out, err
		}
		out.Ratio = v
	}
	if in.AttackMs != nil {
		v, err := check(in.AttackMs, 0.1, 500, "attack_ms")
		if err != nil {
			return out, err
		}
		out.AttackMs = v
	}
	if in.ReleaseMs != nil {
		v, err := check(in.ReleaseMs, 1, 5000, "release_ms")
		if err != nil {
			return out, err
		}
		out.ReleaseMs = v
	}
	if in.MakeupDB != nil {
		v, err := check(in.MakeupDB, -12, 24, "makeup_db")
		if err != nil {
			return out, err
		}
		out.MakeupDB = v
	}
	return out, nil
}

// parseBPBFromTimeSig extracts the numerator of "4/4", "3/4", "6/8" etc.
// Returns 0 if the string isn't a recognisable "N/D" form — callers then
// fall back to a default.
func parseBPBFromTimeSig(ts string) int {
	for i := 0; i < len(ts); i++ {
		if ts[i] == '/' {
			n := 0
			for j := 0; j < i; j++ {
				if ts[j] < '0' || ts[j] > '9' {
					return 0
				}
				n = n*10 + int(ts[j]-'0')
			}
			return n
		}
	}
	return 0
}
