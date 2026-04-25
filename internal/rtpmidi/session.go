package rtpmidi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// Session is an outbound RTP-MIDI session to a single remote peer. It binds
// two adjacent UDP sockets locally (control + data = control+1), runs the
// AppleMIDI invitation handshake on both, then lets you Send MIDI bytes as
// RTP-MIDI data packets on the data socket. A background goroutine per
// socket reads incoming packets and responds to peer-initiated clock syncs.
type Session struct {
	name  string
	ssrc  uint32
	token uint32

	control       net.PacketConn
	data          net.PacketConn
	remoteControl net.Addr
	remoteData    net.Addr

	startedAt time.Time

	mu     sync.Mutex
	seq    uint16
	closed bool

	wg  sync.WaitGroup
	log *log.Logger
}

// Dial establishes a session with the remote AppleMIDI peer at
// host:controlPort. The data port is controlPort+1 on the peer.
// dialTimeout applies to each of the two handshake exchanges.
func Dial(ctx context.Context, host string, controlPort int, localName string, dialTimeout time.Duration, logger *log.Logger) (*Session, error) {
	if logger == nil {
		logger = log.Default()
	}
	if dialTimeout <= 0 {
		dialTimeout = 3 * time.Second
	}

	ctrl, data, err := bindPortPair()
	if err != nil {
		return nil, err
	}

	remoteCtrl, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, controlPort))
	if err != nil {
		ctrl.Close()
		data.Close()
		return nil, err
	}
	remoteData, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, controlPort+1))
	if err != nil {
		ctrl.Close()
		data.Close()
		return nil, err
	}

	s := &Session{
		name:          localName,
		ssrc:          randUint32(),
		token:         randUint32(),
		control:       ctrl,
		data:          data,
		remoteControl: remoteCtrl,
		remoteData:    remoteData,
		startedAt:     time.Now(),
		log:           logger,
	}

	if err := s.invite(ctx, ctrl, remoteCtrl, dialTimeout); err != nil {
		s.close()
		return nil, fmt.Errorf("control port handshake: %w", err)
	}
	if err := s.invite(ctx, data, remoteData, dialTimeout); err != nil {
		s.close()
		return nil, fmt.Errorf("data port handshake: %w", err)
	}

	s.wg.Add(2)
	go s.receiveLoop(ctrl, remoteCtrl, "control")
	go s.receiveLoop(data, remoteData, "data")

	return s, nil
}

// invite runs the IN/OK or IN/NO exchange on one socket. Retries the
// invitation a small number of times in case the first UDP packet is lost.
func (s *Session) invite(ctx context.Context, conn net.PacketConn, remote net.Addr, total time.Duration) error {
	inv := SessionPacket{
		Cmd:     CmdInvitation,
		Version: ProtocolVersion,
		Token:   s.token,
		SSRC:    s.ssrc,
		Name:    s.name,
	}.Marshal()

	deadline := time.Now().Add(total)
	buf := make([]byte, 1500)
	retry := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		if _, err := conn.WriteTo(inv, remote); err != nil {
			return fmt.Errorf("send invitation: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("handshake timed out")
		}
		waitUntil := time.Now().Add(retry)
		if waitUntil.After(deadline) {
			waitUntil = deadline
		}
		if err := conn.SetReadDeadline(waitUntil); err != nil {
			return fmt.Errorf("set deadline: %w", err)
		}
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if attempt >= 4 {
					return errors.New("no response to invitation after 5 tries")
				}
				continue
			}
			return fmt.Errorf("read response: %w", err)
		}
		_ = conn.SetReadDeadline(time.Time{})
		if !IsSessionControl(buf[:n]) {
			continue
		}
		pkt, err := ParseSession(buf[:n])
		if err != nil {
			continue
		}
		if pkt.Token != s.token {
			continue // not our handshake
		}
		switch pkt.Cmd {
		case CmdAccept:
			return nil
		case CmdReject:
			return errors.New("peer rejected invitation")
		}
	}
}

// receiveLoop reads packets on one socket until the session is closed. CK
// sync packets get a response so we appear synchronised; BY from the peer
// tears the session down; everything else is logged and dropped.
func (s *Session) receiveLoop(conn net.PacketConn, remote net.Addr, which string) {
	defer s.wg.Done()
	buf := make([]byte, 1500)
	for {
		s.mu.Lock()
		done := s.closed
		s.mu.Unlock()
		if done {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		data := buf[:n]
		if !IsSessionControl(data) {
			continue // probably an RTP packet — we don't parse inbound data in v1
		}
		// Discriminate CK from session packets by the command bytes at offset 2.
		if len(data) >= 4 && data[2] == 'C' && data[3] == 'K' {
			s.handleCK(conn, from, data)
			continue
		}
		pkt, err := ParseSession(data)
		if err != nil {
			continue
		}
		switch pkt.Cmd {
		case CmdBye:
			s.log.Printf("rtpmidi: peer sent BY on %s", which)
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			return
		}
	}
}

// handleCK responds to a peer's clock-sync probe. If they sent count=0, we
// fill in timestamp2 and reply with count=1; if count=1, we fill timestamp3
// and reply with count=2 (though as initiator we rarely receive count=1).
// count=2 terminates the exchange.
func (s *Session) handleCK(conn net.PacketConn, from net.Addr, data []byte) {
	ck, err := ParseClockSync(data)
	if err != nil {
		return
	}
	now := s.timestamp64()
	switch ck.Count {
	case 0:
		reply := ClockSync{
			SSRC:       s.ssrc,
			Count:      1,
			Timestamps: [3]uint64{ck.Timestamps[0], now, 0},
		}
		_, _ = conn.WriteTo(reply.Marshal(), from)
	case 1:
		reply := ClockSync{
			SSRC:       s.ssrc,
			Count:      2,
			Timestamps: [3]uint64{ck.Timestamps[0], ck.Timestamps[1], now},
		}
		_, _ = conn.WriteTo(reply.Marshal(), from)
	case 2:
		// Exchange complete — nothing to do.
	}
}

// Send transmits one RTP-MIDI data packet carrying the given MIDI bytes.
// Caller must pass valid MIDI (status byte + data bytes). Thread-safe.
func (s *Session) Send(midi []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("rtpmidi: session closed")
	}
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	pkt := DataPacket{
		SequenceNumber: seq,
		Timestamp:      uint32(s.timestamp64()),
		SSRC:           s.ssrc,
		MIDI:           midi,
	}
	b, err := pkt.Marshal()
	if err != nil {
		return err
	}
	_, err = s.data.WriteTo(b, s.remoteData)
	return err
}

// Close tears the session down: sends BY on both ports (best-effort) and
// closes the sockets. Receive-loop goroutines exit on the next deadline.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	bye := SessionPacket{
		Cmd:     CmdBye,
		Version: ProtocolVersion,
		Token:   s.token,
		SSRC:    s.ssrc,
	}.Marshal()
	_, _ = s.control.WriteTo(bye, s.remoteControl)
	_, _ = s.data.WriteTo(bye, s.remoteData)

	err1 := s.control.Close()
	err2 := s.data.Close()
	s.wg.Wait()
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *Session) close() {
	if s.control != nil {
		s.control.Close()
	}
	if s.data != nil {
		s.data.Close()
	}
}

// timestamp64 returns microseconds/100 since session start — the 10 kHz
// clock AppleMIDI and RTP-MIDI use.
func (s *Session) timestamp64() uint64 {
	return uint64(time.Since(s.startedAt) / (100 * time.Microsecond))
}

// bindPortPair finds a pair of adjacent UDP ports (even, odd) where both
// are free, and returns two PacketConns. Starts in the 50000-60000 range.
func bindPortPair() (ctrl, data net.PacketConn, err error) {
	for attempt := 0; attempt < 200; attempt++ {
		port := 50000 + 2*randIntN(4900) // even port in [50000, 59800]
		ctrl, err = net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		data, err = net.ListenPacket("udp", fmt.Sprintf(":%d", port+1))
		if err != nil {
			ctrl.Close()
			continue
		}
		return ctrl, data, nil
	}
	return nil, nil, errors.New("rtpmidi: no free adjacent UDP port pair found")
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func randIntN(n int) int {
	if n <= 0 {
		return 0
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := binary.BigEndian.Uint32(b[:])
	return int(v % uint32(n))
}
