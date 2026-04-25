package mixer

import (
	"log"
	"math"

	"seqone/internal/protocol"
)

// TrackRoute describes how a pattern-track ID maps to mixer inputs:
// which sample to play, what the sample's native pitch is, and the
// per-track pan + gain. Returned by the resolver the bridge queries
// on every trigger so song reloads are picked up without reconnecting.
type TrackRoute struct {
	SampleID string  // "" when the track has no sample binding
	BaseNote int     // 0..127 — sample's native pitch (for varispeed transposition)
	Pan      float32 // -1.0 (full left) .. 1.0 (full right). 0 = centre.
	Gain     float32 // per-track gain multiplier. 0 is treated as unity for back-compat.
}

// TrackRouteResolver looks up the TrackRoute for a pattern-track ID.
// Typically backed by the currently-loaded Song's Tracks map.
type TrackRouteResolver func(trackID string) TrackRoute

// Bridge subscribes to the engine event bus and translates sample-
// trigger events into mixer.Trigger calls. Pitched NoteOn/Off events are
// ignored here — those belong to the RTP-MIDI bridge. NoteAllOff silences
// every active voice.
type Bridge struct {
	mix     *Mixer
	resolve TrackRouteResolver
	log     *log.Logger
}

func NewBridge(mix *Mixer, resolve TrackRouteResolver, logger *log.Logger) *Bridge {
	if logger == nil {
		logger = log.Default()
	}
	if resolve == nil {
		resolve = func(string) TrackRoute { return TrackRoute{} }
	}
	return &Bridge{mix: mix, resolve: resolve, log: logger}
}

// Run consumes events from a subscribed channel until it closes.
func (b *Bridge) Run(events <-chan protocol.Event) {
	for ev := range events {
		if ev.Event != protocol.EvNote {
			continue
		}
		switch ev.NoteKind {
		case protocol.SampleTrigger:
			route := b.resolve(ev.Track)
			if route.SampleID == "" {
				continue
			}
			// Pitch only when the event carries an explicit pitch.
			// ev.Note == 0 is "bare trigger" (native rate) — matches an
			// `X` pattern cell and the uppercase-X editor binding.
			ratio := 1.0
			if ev.Note != 0 {
				ratio = math.Pow(2.0, float64(ev.Note-route.BaseNote)/12.0)
			}
			// Pattern-track ID is the voice choke key — a new hit on the
			// same track silences any still-decaying voice from the
			// previous trigger, matching Amiga channel monophony and
			// killing the loop-wrap phasing you'd otherwise get when a
			// tail overlaps the next cycle's onset.
			b.mix.Play(VoiceTrigger{
				SampleID:   route.SampleID,
				Vel:        ev.Vel,
				PitchRatio: ratio,
				ChokeKey:   ev.Track,
				Pan:        route.Pan,
				TrackGain:  route.Gain,
			})
		case protocol.NoteAllOff:
			b.mix.AllOff()
		}
	}
}
