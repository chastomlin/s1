// Package rtpmidi implements a minimum-viable RTP-MIDI (RFC 6295) sender with
// the AppleMIDI session-control protocol layered on top. It is sender-only in
// v1 — no journal/recovery and no full CK clock-sync exchange, though the
// receive loop replies to peer-initiated CK syncs to be a good citizen.
package rtpmidi

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// SessionCmd is one of the AppleMIDI session control commands. Each command
// is encoded as two ASCII bytes on the wire.
type SessionCmd [2]byte

var (
	CmdInvitation SessionCmd = [2]byte{'I', 'N'}
	CmdAccept     SessionCmd = [2]byte{'O', 'K'}
	CmdReject     SessionCmd = [2]byte{'N', 'O'}
	CmdBye        SessionCmd = [2]byte{'B', 'Y'}
	CmdClockSync  SessionCmd = [2]byte{'C', 'K'}
)

// sessionMagic is the two-byte signature at the start of every AppleMIDI
// control packet (invitation / clock-sync / bye).
const sessionMagic uint16 = 0xFFFF

// AppleMIDI protocol version we advertise. Version 2 is what everything in
// the wild speaks.
const ProtocolVersion uint32 = 2

// SessionPacket is an IN / OK / NO / BY packet. Name is meaningful only for
// IN and OK; leave it empty for NO and BY.
type SessionPacket struct {
	Cmd     SessionCmd
	Version uint32
	Token   uint32
	SSRC    uint32
	Name    string
}

// Marshal serialises p to the AppleMIDI wire format. The name, when present,
// is written as UTF-8 with a single trailing NUL.
func (p SessionPacket) Marshal() []byte {
	size := 2 + 2 + 4 + 4 + 4
	if p.Name != "" {
		size += len(p.Name) + 1
	}
	b := make([]byte, size)
	binary.BigEndian.PutUint16(b[0:2], sessionMagic)
	b[2] = p.Cmd[0]
	b[3] = p.Cmd[1]
	binary.BigEndian.PutUint32(b[4:8], p.Version)
	binary.BigEndian.PutUint32(b[8:12], p.Token)
	binary.BigEndian.PutUint32(b[12:16], p.SSRC)
	if p.Name != "" {
		copy(b[16:], p.Name)
		b[16+len(p.Name)] = 0
	}
	return b
}

// ParseSession decodes a session control packet. It is tolerant of optional
// trailing name for commands that normally omit it (NO, BY).
func ParseSession(data []byte) (SessionPacket, error) {
	if len(data) < 16 {
		return SessionPacket{}, fmt.Errorf("rtpmidi: session packet too short: %d", len(data))
	}
	if binary.BigEndian.Uint16(data[0:2]) != sessionMagic {
		return SessionPacket{}, errors.New("rtpmidi: bad session magic")
	}
	var p SessionPacket
	p.Cmd = SessionCmd{data[2], data[3]}
	p.Version = binary.BigEndian.Uint32(data[4:8])
	p.Token = binary.BigEndian.Uint32(data[8:12])
	p.SSRC = binary.BigEndian.Uint32(data[12:16])
	if len(data) > 16 {
		end := len(data)
		for i := 16; i < end; i++ {
			if data[i] == 0 {
				end = i
				break
			}
		}
		p.Name = string(data[16:end])
	}
	return p, nil
}

// ClockSync is a CK packet used for session time synchronisation. The three
// timestamps are filled in by initiator (count=0), responder (count=1), and
// initiator again (count=2) during a three-way round-trip probe. Units are
// 100 µs ticks (a 10 kHz clock), matching AppleMIDI convention.
type ClockSync struct {
	SSRC       uint32
	Count      uint8
	Timestamps [3]uint64
}

const clockSyncSize = 2 + 2 + 4 + 1 + 3 + 8 + 8 + 8 // 36 bytes

func (c ClockSync) Marshal() []byte {
	b := make([]byte, clockSyncSize)
	binary.BigEndian.PutUint16(b[0:2], sessionMagic)
	b[2] = 'C'
	b[3] = 'K'
	binary.BigEndian.PutUint32(b[4:8], c.SSRC)
	b[8] = c.Count
	// b[9..11] are three bytes of zero padding (already zero).
	binary.BigEndian.PutUint64(b[12:20], c.Timestamps[0])
	binary.BigEndian.PutUint64(b[20:28], c.Timestamps[1])
	binary.BigEndian.PutUint64(b[28:36], c.Timestamps[2])
	return b
}

func ParseClockSync(data []byte) (ClockSync, error) {
	if len(data) < clockSyncSize {
		return ClockSync{}, fmt.Errorf("rtpmidi: CK packet too short: %d", len(data))
	}
	if binary.BigEndian.Uint16(data[0:2]) != sessionMagic {
		return ClockSync{}, errors.New("rtpmidi: bad CK magic")
	}
	if data[2] != 'C' || data[3] != 'K' {
		return ClockSync{}, errors.New("rtpmidi: not a CK packet")
	}
	var c ClockSync
	c.SSRC = binary.BigEndian.Uint32(data[4:8])
	c.Count = data[8]
	c.Timestamps[0] = binary.BigEndian.Uint64(data[12:20])
	c.Timestamps[1] = binary.BigEndian.Uint64(data[20:28])
	c.Timestamps[2] = binary.BigEndian.Uint64(data[28:36])
	return c, nil
}

// IsSessionControl returns true if the packet looks like an AppleMIDI
// control (session or CK) message rather than an RTP data packet. The 0xFFFF
// signature is illegal as RTP's first two bytes (V=2 P=0 X=0 CC=0 yields
// 0x80 in byte 0), so a simple magic check is sufficient to discriminate.
func IsSessionControl(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFF
}

// --- RTP-MIDI data packet (RFC 6295) ---------------------------------------

// DataPacket is one RTP-MIDI data packet. V1 omits the journal section
// (J=0), delta time (Z=0), and running-status carry-over (P=0). MIDI is a
// flat slice of raw MIDI command bytes — caller is responsible for forming
// valid MIDI.
type DataPacket struct {
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	MIDI           []byte
}

// Payload-type for RTP-MIDI, RFC 6295 §2.1. Any dynamic PT in the 96-127
// range is legal; 97 is the value Apple and most implementations use.
const payloadType = 0x61 // 97

// Marshal serialises p to wire bytes. Uses the short (4-bit length) MIDI
// header when MIDI fits in 15 bytes, else the long (12-bit length) header.
// Returns an error if MIDI is too large to fit in a single packet (>4095 B).
func (p DataPacket) Marshal() ([]byte, error) {
	n := len(p.MIDI)
	if n > 0x0FFF {
		return nil, fmt.Errorf("rtpmidi: midi payload too large: %d bytes", n)
	}
	var header []byte
	if n < 16 {
		header = []byte{byte(n)} // B=0, J=0, Z=0, P=0, len=n
	} else {
		header = []byte{
			0x80 | byte(n>>8&0x0F), // B=1
			byte(n & 0xFF),
		}
	}
	b := make([]byte, 12+len(header)+n)
	b[0] = 0x80 // V=2, P=0, X=0, CC=0
	b[1] = payloadType
	binary.BigEndian.PutUint16(b[2:4], p.SequenceNumber)
	binary.BigEndian.PutUint32(b[4:8], p.Timestamp)
	binary.BigEndian.PutUint32(b[8:12], p.SSRC)
	copy(b[12:], header)
	copy(b[12+len(header):], p.MIDI)
	return b, nil
}

// ParseData decodes an RTP-MIDI data packet. For v1 we only understand the
// no-flags case (J=0, Z=0, P=0). A packet with any of those flags set
// returns an error — we don't parse journals or running status.
func ParseData(data []byte) (DataPacket, error) {
	if len(data) < 13 {
		return DataPacket{}, fmt.Errorf("rtpmidi: data packet too short: %d", len(data))
	}
	if data[0]>>6 != 2 {
		return DataPacket{}, errors.New("rtpmidi: not RTP v2")
	}
	if data[1]&0x7F != payloadType {
		return DataPacket{}, fmt.Errorf("rtpmidi: unexpected payload type %d", data[1]&0x7F)
	}
	var p DataPacket
	p.SequenceNumber = binary.BigEndian.Uint16(data[2:4])
	p.Timestamp = binary.BigEndian.Uint32(data[4:8])
	p.SSRC = binary.BigEndian.Uint32(data[8:12])

	h := data[12]
	big := h&0x80 != 0
	if h&0x40 != 0 {
		return DataPacket{}, errors.New("rtpmidi: journal section not supported")
	}
	if h&0x20 != 0 {
		return DataPacket{}, errors.New("rtpmidi: leading delta-time not supported")
	}
	if h&0x10 != 0 {
		return DataPacket{}, errors.New("rtpmidi: running-status carry not supported")
	}
	var midiStart, midiLen int
	if big {
		if len(data) < 14 {
			return DataPacket{}, errors.New("rtpmidi: truncated long header")
		}
		midiLen = int(h&0x0F)<<8 | int(data[13])
		midiStart = 14
	} else {
		midiLen = int(h & 0x0F)
		midiStart = 13
	}
	if midiStart+midiLen > len(data) {
		return DataPacket{}, fmt.Errorf("rtpmidi: declared midi length %d overruns packet (have %d)", midiLen, len(data)-midiStart)
	}
	p.MIDI = append([]byte(nil), data[midiStart:midiStart+midiLen]...)
	return p, nil
}
