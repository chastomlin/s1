package rtpmidi

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"
)

// fakePeer acts as an AppleMIDI receiver: listens on a known (control, data)
// port pair, replies OK to every IN, forwards received data packets to a
// channel, and logs CK exchanges. Stops when ctx is cancelled.
type fakePeer struct {
	ctrl, data net.PacketConn
	ctrlPort   int
	ssrc       uint32
	received   chan DataPacket
	wg         sync.WaitGroup
}

func startFakePeer(t *testing.T) *fakePeer {
	t.Helper()
	// Listen on two adjacent ports. Search loop for a free pair.
	var (
		ctrl, data net.PacketConn
		port       int
	)
	for attempt := 0; attempt < 100; attempt++ {
		candidate := 40000 + attempt*2
		c, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", candidate))
		if err != nil {
			continue
		}
		d, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", candidate+1))
		if err != nil {
			c.Close()
			continue
		}
		ctrl, data, port = c, d, candidate
		break
	}
	if ctrl == nil {
		t.Fatal("could not bind fake-peer ports")
	}
	p := &fakePeer{
		ctrl:     ctrl,
		data:     data,
		ctrlPort: port,
		ssrc:     0xFEEDFACE,
		received: make(chan DataPacket, 64),
	}
	p.wg.Add(2)
	go p.serve(ctrl, "ctrl")
	go p.serve(data, "data")
	return p
}

func (p *fakePeer) serve(conn net.PacketConn, which string) {
	defer p.wg.Done()
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		pkt := buf[:n]
		if IsSessionControl(pkt) {
			if len(pkt) >= 4 && pkt[2] == 'C' && pkt[3] == 'K' {
				// Respond to initiator's count=0 with count=1.
				ck, err := ParseClockSync(pkt)
				if err == nil && ck.Count == 0 {
					reply := ClockSync{
						SSRC:       p.ssrc,
						Count:      1,
						Timestamps: [3]uint64{ck.Timestamps[0], 12345, 0},
					}.Marshal()
					_, _ = conn.WriteTo(reply, from)
				}
				continue
			}
			s, err := ParseSession(pkt)
			if err != nil {
				continue
			}
			switch s.Cmd {
			case CmdInvitation:
				ok := SessionPacket{
					Cmd:     CmdAccept,
					Version: s.Version,
					Token:   s.Token,
					SSRC:    p.ssrc,
					Name:    "fake",
				}.Marshal()
				_, _ = conn.WriteTo(ok, from)
			case CmdBye:
				// peer tearing down; no reply.
			}
			continue
		}
		// Not session control: try parsing as RTP-MIDI data.
		if which == "data" {
			dp, err := ParseData(pkt)
			if err == nil {
				select {
				case p.received <- dp:
				default:
				}
			}
		}
	}
}

func (p *fakePeer) stop() {
	p.ctrl.Close()
	p.data.Close()
	p.wg.Wait()
}

func TestSession_HandshakeAndSend(t *testing.T) {
	peer := startFakePeer(t)
	defer peer.stop()

	logger := log.New(io.Discard, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sess, err := Dial(ctx, "127.0.0.1", peer.ctrlPort, "tester", 2*time.Second, logger)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// Send a note-on and verify the fake peer received the right MIDI bytes.
	if err := sess.Send([]byte{0x90, 60, 100}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-peer.received:
		if len(got.MIDI) != 3 || got.MIDI[0] != 0x90 || got.MIDI[1] != 60 || got.MIDI[2] != 100 {
			t.Errorf("wrong MIDI bytes: %x", got.MIDI)
		}
		if got.SSRC == 0 {
			t.Error("expected non-zero SSRC")
		}
		if got.SequenceNumber == 0 {
			t.Error("expected non-zero sequence number")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for MIDI packet at peer")
	}

	// Send a second note and confirm the sequence number advances.
	if err := sess.Send([]byte{0x80, 60, 0}); err != nil {
		t.Fatalf("Send #2: %v", err)
	}
	select {
	case got := <-peer.received:
		if got.MIDI[0] != 0x80 {
			t.Errorf("expected note-off, got status %x", got.MIDI[0])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for second MIDI packet")
	}

	if err := sess.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestSession_DialTimeoutWhenPeerSilent(t *testing.T) {
	// Bind a socket but never reply; Dial should give up inside the timeout.
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	// Remote port pair: we only have one socket bound, the +1 port likely
	// isn't listening, but that's fine — the initiator retries on the
	// control port first and should time out there.
	port := dead.LocalAddr().(*net.UDPAddr).Port

	logger := log.New(io.Discard, "", 0)
	start := time.Now()
	_, err = Dial(context.Background(), "127.0.0.1", port, "tester", 400*time.Millisecond, logger)
	if err == nil {
		t.Fatal("expected timeout, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Dial took too long to give up: %v", elapsed)
	}
}
