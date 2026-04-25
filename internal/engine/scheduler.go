package engine

import (
	"sort"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// NoteEvent is a single scheduled musical action at an absolute song tick:
// a note-on, note-off, or sample trigger on one track. The scheduler
// precomputes these once from the loaded song and then flushes them in
// order as the transport advances.
type NoteEvent struct {
	AbsTick int
	Track   string
	Section string
	Kind    protocol.NoteKind
	Note    int
	Vel     int
}

// Scheduler walks a precomputed timeline of NoteEvents. It is not safe for
// concurrent use — callers (the engine) serialise access under their own
// mutex.
type Scheduler struct {
	timeline []NoteEvent
	endTick  int
	cursor   int // index of the next event to emit
}

// NewScheduler builds a scheduler from a loaded song. A nil song yields a
// no-op scheduler that emits nothing.
func NewScheduler(s *song.Song) *Scheduler {
	if s == nil {
		return &Scheduler{}
	}
	tl, end := buildTimeline(s)
	return &Scheduler{timeline: tl, endTick: end}
}

// Advance returns all events with AbsTick < nowTick that haven't yet been
// emitted, and moves the cursor forward past them.
func (s *Scheduler) Advance(nowTick int) []NoteEvent {
	if s == nil {
		return nil
	}
	var out []NoteEvent
	for s.cursor < len(s.timeline) && s.timeline[s.cursor].AbsTick < nowTick {
		out = append(out, s.timeline[s.cursor])
		s.cursor++
	}
	return out
}

// SeekTo positions the cursor so the next Advance will emit events whose
// AbsTick is >= tickAbs. The caller is responsible for silencing any
// currently-sounding notes (via an all-off event) before calling this.
func (s *Scheduler) SeekTo(tickAbs int) {
	if s == nil {
		return
	}
	s.cursor = 0
	for s.cursor < len(s.timeline) && s.timeline[s.cursor].AbsTick < tickAbs {
		s.cursor++
	}
}

// Reset rewinds the cursor to the start of the timeline.
func (s *Scheduler) Reset() {
	if s == nil {
		return
	}
	s.cursor = 0
}

// EndTick is the total song length in ticks — the tick at which the last
// arranged section ends. Useful for "song finished" detection.
func (s *Scheduler) EndTick() int {
	if s == nil {
		return 0
	}
	return s.endTick
}

// buildTimeline flattens the arrangement into a tick-sorted list of
// NoteEvents. Ties extend the previously-attacked note on the same track;
// rests terminate a held note; sample cells trigger one-shot, non-holding
// events. Held notes are explicitly terminated at the end of each section
// repeat — a section is a self-contained block.
func buildTimeline(s *song.Song) ([]NoteEvent, int) {
	var out []NoteEvent
	absTick := 0

	for _, slot := range s.Arrangement {
		sec, ok := s.Sections[slot.Section]
		if !ok {
			continue
		}
		if sec.Resolution <= 0 || sec.BeatsPerBar <= 0 || sec.Bars <= 0 {
			continue
		}
		ticksPerCell := PPQN / sec.Resolution
		sectionLenTicks := sec.Bars * sec.BeatsPerBar * PPQN

		repeats := slot.Repeat
		if repeats < 1 {
			repeats = 1
		}
		for r := 0; r < repeats; r++ {
			base := absTick
			// Iterate parts in deterministic order (by track ID) so repeated
			// builds produce identical timelines.
			trackIDs := make([]string, 0, len(sec.Parts))
			for id := range sec.Parts {
				trackIDs = append(trackIDs, id)
			}
			sort.Strings(trackIDs)

			for _, id := range trackIDs {
				pat, ok := s.Patterns[sec.Parts[id]]
				if !ok {
					continue
				}
				// Sample tracks treat CellNote as a pitched sample trigger
				// rather than a MIDI note-on — the mixer transposes via
				// varispeed, the MIDI bridge still routes to the drum note
				// regardless of pitch.
				isSampleTrack := false
				if t, ok := s.Tracks[id]; ok && t.Sample != "" {
					isSampleTrack = true
				}
				holding := false
				heldNote := 0
				for i, c := range pat.Cells {
					cellTick := base + i*ticksPerCell
					switch c.Kind {
					case song.CellRest:
						if holding {
							out = append(out, NoteEvent{
								AbsTick: cellTick,
								Track:   id,
								Section: slot.Section,
								Kind:    protocol.NoteOff,
								Note:    heldNote,
							})
							holding = false
						}
					case song.CellTie:
						// Extend — emit nothing.
					case song.CellNote:
						if isSampleTrack {
							out = append(out, NoteEvent{
								AbsTick: cellTick,
								Track:   id,
								Section: slot.Section,
								Kind:    protocol.SampleTrigger,
								Note:    c.Note,
								Vel:     c.Vel,
							})
							continue
						}
						if holding {
							out = append(out, NoteEvent{
								AbsTick: cellTick,
								Track:   id,
								Section: slot.Section,
								Kind:    protocol.NoteOff,
								Note:    heldNote,
							})
						}
						out = append(out, NoteEvent{
							AbsTick: cellTick,
							Track:   id,
							Section: slot.Section,
							Kind:    protocol.NoteOn,
							Note:    c.Note,
							Vel:     c.Vel,
						})
						holding = true
						heldNote = c.Note
					case song.CellSample:
						out = append(out, NoteEvent{
							AbsTick: cellTick,
							Track:   id,
							Section: slot.Section,
							Kind:    protocol.SampleTrigger,
							Vel:     c.Vel,
						})
					}
				}
				if holding {
					out = append(out, NoteEvent{
						AbsTick: base + sectionLenTicks,
						Track:   id,
						Section: slot.Section,
						Kind:    protocol.NoteOff,
						Note:    heldNote,
					})
				}
			}
			absTick += sectionLenTicks
		}
	}

	// Sort by tick, preserving input order for ties. NoteOff should sort
	// before NoteOn at the same tick on the same track so a retrigger
	// releases the old note first; since buildTimeline emits them in that
	// order already, a stable sort preserves it.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].AbsTick < out[j].AbsTick
	})
	return out, absTick
}
