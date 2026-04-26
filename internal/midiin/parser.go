package midiin

// MIDI wire-format parser. Pure-Go, allocation-free in the steady
// state, designed for the byte stream you read out of /dev/snd/midiCxDy
// — running status, channel messages, and a best-effort skip for
// SysEx and the rare real-time bytes.

// Kind names a MIDI channel-voice message we care about. We only
// surface what the rest of the system uses; less common things
// (aftertouch, channel pressure) are parsed and ignored for now.
type Kind int

const (
	KindNoteOff Kind = iota
	KindNoteOn
	KindControlChange
	KindProgramChange
	KindPitchBend
)

// Message is the result of one fully-parsed MIDI event.
//
// Channel is 1..16 to match how the song schema talks about channels
// elsewhere — wire bytes encode 0..15 in the low nibble and we add 1
// at parse time so callers don't have to remember.
//
// Data1 / Data2 carry the message-specific values; meaning depends on
// Kind. For ControlChange: Data1 = controller (0..127), Data2 = value
// (0..127). For PitchBend, Data1 holds the LSB and Data2 the MSB.
type Message struct {
	Kind    Kind
	Channel int
	Data1   uint8
	Data2   uint8
}

// Parser is a stream parser. Feed bytes via Feed and it appends fully-
// formed Messages to the slice you pass. State across calls is just
// the running-status byte and any partial-message accumulator.
type Parser struct {
	runningStatus uint8
	dataNeeded    int
	dataBuf       [2]uint8
	dataHave      int
	inSysEx       bool
}

// Feed processes the bytes in `in` and appends any completed messages
// to `out`, returning the new slice (so the caller can pass nil and
// receive a fresh slice). Allocation-free on the hot path provided
// `out` has enough cap.
func (p *Parser) Feed(in []byte, out []Message) []Message {
	for _, b := range in {
		switch {
		case b >= 0xF8:
			// System real-time (clock, start, stop, etc.). Single-byte
			// messages that interleave with anything; we drop them.
			continue
		case b == 0xF0:
			p.inSysEx = true
			continue
		case b == 0xF7:
			p.inSysEx = false
			continue
		case p.inSysEx:
			continue
		case b >= 0xF1 && b <= 0xF6:
			// System common (MTC quarter, song position, etc.). Reset
			// running status per spec; we don't surface them.
			p.runningStatus = 0
			p.dataHave = 0
			p.dataNeeded = 0
			continue
		case b >= 0x80:
			// Channel status byte starts a new message.
			p.runningStatus = b
			p.dataHave = 0
			p.dataNeeded = bytesForStatus(b)
		default:
			// Data byte. Use running status if we don't have a current one.
			if p.runningStatus == 0 {
				continue
			}
			if p.dataHave < 2 {
				p.dataBuf[p.dataHave] = b
			}
			p.dataHave++
			if p.dataHave >= p.dataNeeded {
				if msg, ok := p.assemble(); ok {
					out = append(out, msg)
				}
				// Reset for next message; running status persists so
				// the next plain data byte starts a new message of the
				// same kind without needing a status repeat.
				p.dataHave = 0
			}
		}
	}
	return out
}

func bytesForStatus(status uint8) int {
	switch status & 0xF0 {
	case 0xC0, 0xD0:
		return 1
	default:
		return 2
	}
}

func (p *Parser) assemble() (Message, bool) {
	status := p.runningStatus
	ch := int(status&0x0F) + 1
	d1 := p.dataBuf[0]
	d2 := p.dataBuf[1]
	switch status & 0xF0 {
	case 0x80:
		return Message{Kind: KindNoteOff, Channel: ch, Data1: d1, Data2: d2}, true
	case 0x90:
		// Note On with velocity 0 is conventionally a Note Off.
		if d2 == 0 {
			return Message{Kind: KindNoteOff, Channel: ch, Data1: d1, Data2: 0}, true
		}
		return Message{Kind: KindNoteOn, Channel: ch, Data1: d1, Data2: d2}, true
	case 0xB0:
		return Message{Kind: KindControlChange, Channel: ch, Data1: d1, Data2: d2}, true
	case 0xC0:
		return Message{Kind: KindProgramChange, Channel: ch, Data1: d1}, true
	case 0xE0:
		return Message{Kind: KindPitchBend, Channel: ch, Data1: d1, Data2: d2}, true
	}
	return Message{}, false
}
