package rtpmidi

import (
	"log"

	"seqone/internal/protocol"
)

// Sender is the subset of *Session the bridge needs — abstracted so tests
// can substitute a capture-only fake.
type Sender interface {
	Send(midi []byte) error
}

// TrackRouting tells the bridge how to translate one engine track ID into
// MIDI. Channel is 1..16 (user-facing); it's mapped to 0..15 on the wire.
// For sample tracks IsSample is true and Note carries the MIDI note the
// bridge should trigger when the engine emits a SampleTrigger for that
// track. For pitched instrument tracks, the engine-supplied note is used.
type TrackRouting struct {
	Channel  int
	Note     int
	IsSample bool
}

// Resolver looks up the routing for a track ID. Second return is false when
// the track has no binding — the bridge falls back to a default channel for
// pitched notes and drops sample triggers in that case.
type Resolver func(trackID string) (TrackRouting, bool)

// Bridge subscribes to an engine event bus and forwards note-on, note-off,
// and all-off events as MIDI messages on a Sender. Per-track routing is
// resolved on each event so reloading the song picks up new bindings.
type Bridge struct {
	send     Sender
	resolve  Resolver
	log      *log.Logger
	fallback byte // wire channel for notes on unresolved tracks (default 0)
}

func NewBridge(send Sender, resolve Resolver, logger *log.Logger) *Bridge {
	if logger == nil {
		logger = log.Default()
	}
	if resolve == nil {
		resolve = func(string) (TrackRouting, bool) { return TrackRouting{}, false }
	}
	return &Bridge{send: send, resolve: resolve, log: logger, fallback: 0}
}

// Run consumes events from the given channel until it is closed. Errors
// from Send are logged and otherwise ignored — the engine bus keeps flowing.
func (b *Bridge) Run(events <-chan protocol.Event) {
	for ev := range events {
		if ev.Event != protocol.EvNote {
			continue
		}
		b.handle(ev)
	}
}

func (b *Bridge) handle(ev protocol.Event) {
	switch ev.NoteKind {
	case protocol.NoteOn:
		ch := b.channelFor(ev.Track)
		b.sendBytes([]byte{0x90 | ch, clampNote(ev.Note), clampVel(ev.Vel, 100)})
	case protocol.NoteOff:
		ch := b.channelFor(ev.Track)
		b.sendBytes([]byte{0x80 | ch, clampNote(ev.Note), 0})
	case protocol.SampleTrigger:
		r, ok := b.resolve(ev.Track)
		if !ok || !r.IsSample {
			return // no mapping — drop
		}
		ch := wireChannel(r.Channel, b.fallback)
		b.sendBytes([]byte{0x90 | ch, clampNote(r.Note), clampVel(ev.Vel, 100)})
	case protocol.NoteAllOff, protocol.NoteHoldsRelease:
		// CC 123 (All Notes Off) on every channel. Both event kinds get
		// the same wire treatment — held MIDI notes need releasing
		// either way. NoteHoldsRelease just signals to the audio mixer
		// that it should leave its sample voices alone (loop-wrap soft
		// cleanup); MIDI doesn't have a "soft" equivalent.
		for ch := byte(0); ch < 16; ch++ {
			b.sendBytes([]byte{0xB0 | ch, 123, 0})
		}
	}
}

// channelFor returns the wire channel (0..15) for a pitched note event. If
// the track has a routing and it is a pitched (instrument) mapping, use
// that channel. Otherwise fall back to b.fallback.
func (b *Bridge) channelFor(trackID string) byte {
	if r, ok := b.resolve(trackID); ok && !r.IsSample {
		return wireChannel(r.Channel, b.fallback)
	}
	return b.fallback
}

// wireChannel converts a 1..16 channel to its 0..15 wire value. Out-of-range
// inputs fall back to the provided default (already a wire value).
func wireChannel(userChannel int, fallback byte) byte {
	if userChannel < 1 || userChannel > 16 {
		return fallback
	}
	return byte(userChannel - 1)
}

func (b *Bridge) sendBytes(msg []byte) {
	if err := b.send.Send(msg); err != nil {
		b.log.Printf("rtpmidi bridge: send %x: %v", msg, err)
	}
}

func clampNote(n int) byte {
	if n < 0 {
		return 0
	}
	if n > 127 {
		return 127
	}
	return byte(n)
}

func clampVel(v, fallback int) byte {
	if v <= 0 {
		return byte(fallback)
	}
	if v > 127 {
		return 127
	}
	return byte(v)
}
