package rtpmidi

import (
	"bytes"
	"reflect"
	"testing"
)

func TestSessionPacket_RoundTrip(t *testing.T) {
	cases := []SessionPacket{
		{Cmd: CmdInvitation, Version: 2, Token: 0xDEADBEEF, SSRC: 0x11223344, Name: "seqone"},
		{Cmd: CmdAccept, Version: 2, Token: 0xDEADBEEF, SSRC: 0xAABBCCDD, Name: "receiver"},
		{Cmd: CmdReject, Version: 2, Token: 0x01020304, SSRC: 0x55667788},
		{Cmd: CmdBye, Version: 2, Token: 0x01020304, SSRC: 0x55667788},
		{Cmd: CmdInvitation, Version: 2, Name: ""}, // bare invitation
	}
	for _, p := range cases {
		got, err := ParseSession(p.Marshal())
		if err != nil {
			t.Fatalf("ParseSession(%v): %v", p.Cmd, err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Errorf("roundtrip mismatch for %q:\n got %+v\nwant %+v", p.Cmd, got, p)
		}
	}
}

func TestSessionPacket_WireBytes(t *testing.T) {
	// Reference encoding of an IN packet — checked against the AppleMIDI
	// byte-level spec.
	p := SessionPacket{
		Cmd:     CmdInvitation,
		Version: 2,
		Token:   0xDEADBEEF,
		SSRC:    0x11223344,
		Name:    "x",
	}
	got := p.Marshal()
	want := []byte{
		0xFF, 0xFF, // magic
		'I', 'N', // cmd
		0x00, 0x00, 0x00, 0x02, // version
		0xDE, 0xAD, 0xBE, 0xEF, // token
		0x11, 0x22, 0x33, 0x44, // ssrc
		'x', 0x00, // name
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire bytes mismatch:\n got %x\nwant %x", got, want)
	}
}

func TestClockSync_RoundTrip(t *testing.T) {
	c := ClockSync{
		SSRC:       0xAABBCCDD,
		Count:      1,
		Timestamps: [3]uint64{0x1111111122222222, 0x3333333344444444, 0},
	}
	b := c.Marshal()
	if len(b) != 36 {
		t.Fatalf("CK packet size: got %d want 36", len(b))
	}
	got, err := ParseClockSync(b)
	if err != nil {
		t.Fatalf("ParseClockSync: %v", err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Errorf("CK roundtrip mismatch:\n got %+v\nwant %+v", got, c)
	}
}

func TestIsSessionControl(t *testing.T) {
	cs := ClockSync{SSRC: 1, Count: 0}.Marshal()
	if !IsSessionControl(cs) {
		t.Error("CK should be recognised as session control")
	}
	sp := SessionPacket{Cmd: CmdInvitation, Version: 2}.Marshal()
	if !IsSessionControl(sp) {
		t.Error("IN should be recognised as session control")
	}
	dp, err := DataPacket{SequenceNumber: 1, Timestamp: 2, SSRC: 3, MIDI: []byte{0x90, 60, 100}}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if IsSessionControl(dp) {
		t.Error("RTP data packet should NOT be recognised as session control")
	}
}

func TestDataPacket_ShortHeader(t *testing.T) {
	p := DataPacket{
		SequenceNumber: 0x0010,
		Timestamp:      0x01020304,
		SSRC:           0xAABBCCDD,
		MIDI:           []byte{0x90, 60, 100}, // note-on C4 ch1 vel100
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := []byte{
		0x80, 0x61, // V=2, PT=97
		0x00, 0x10, // seq
		0x01, 0x02, 0x03, 0x04, // ts
		0xAA, 0xBB, 0xCC, 0xDD, // ssrc
		0x03,             // MIDI header: no flags, len=3
		0x90, 0x3C, 0x64, // note-on C4 vel100
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("bytes mismatch:\n got %x\nwant %x", b, want)
	}
	got, err := ParseData(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got, p)
	}
}

func TestDataPacket_LongHeader(t *testing.T) {
	// 20 MIDI bytes forces the 12-bit (B=1) header.
	midi := make([]byte, 20)
	for i := range midi {
		midi[i] = byte(i)
	}
	p := DataPacket{SequenceNumber: 1, Timestamp: 2, SSRC: 3, MIDI: midi}
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// byte 12 should be 0x80 (B=1) with upper nibble of length = 0, byte 13 = 20.
	if b[12] != 0x80 || b[13] != 20 {
		t.Fatalf("long header bytes: got %02x %02x, want 80 14", b[12], b[13])
	}
	got, err := ParseData(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got, p)
	}
}

func TestParseData_RejectsUnsupportedFlags(t *testing.T) {
	// Hand-craft a packet with J=1 flag set.
	b := []byte{0x80, 0x61, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0x40}
	if _, err := ParseData(b); err == nil {
		t.Error("expected error for J=1 (journal) flag")
	}
	b[12] = 0x20
	if _, err := ParseData(b); err == nil {
		t.Error("expected error for Z=1 (delta-time) flag")
	}
	b[12] = 0x10
	if _, err := ParseData(b); err == nil {
		t.Error("expected error for P=1 (running-status) flag")
	}
}
