package midiin

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Device describes one ALSA raw-MIDI input port the kernel exposes
// under /dev/snd/midiCxDy. Path is what you'd pass to Open; Name is
// a human-friendly identifier built from /proc/asound/cards plus the
// device index, intended for users to recognise their controller in
// a list.
type Device struct {
	Path string
	Name string
}

// List enumerates available raw MIDI input devices. Returns an empty
// slice (not an error) when /dev/snd is missing or empty so the caller
// can render "no devices" without special-casing.
func List() ([]Device, error) {
	entries, err := os.ReadDir("/dev/snd")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cardNames := readCardNames()
	var out []Device
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "midiC") {
			continue
		}
		card, dev, ok := parseMidiName(name)
		if !ok {
			continue
		}
		path := filepath.Join("/dev/snd", name)
		label := name
		if cn, ok := cardNames[card]; ok {
			label = fmt.Sprintf("%s (card %d, device %d)", cn, card, dev)
		}
		out = append(out, Device{Path: path, Name: label})
	}
	return out, nil
}

// FindByName looks for a device whose Name contains `q` (case-
// insensitive substring match). Useful so users can spell "Launch"
// rather than the full model + USB id string.
func FindByName(q string) (Device, error) {
	devs, err := List()
	if err != nil {
		return Device{}, err
	}
	q = strings.ToLower(q)
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), q) || strings.Contains(strings.ToLower(d.Path), q) {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("no MIDI input matching %q (have %d device(s))", q, len(devs))
}

// Listen opens the device at path and pumps MIDI messages into the
// returned channel until ctx is cancelled or the file is closed by
// the kernel (controller unplugged). The goroutine drains and exits
// cleanly in either case; close() on the file unblocks the read.
//
// The channel is buffered (256) to absorb short bursts; when full,
// new messages are dropped rather than stalling the read goroutine.
// CC streams are bursty enough that lossy delivery is the right call.
func Listen(ctx context.Context, path string) (<-chan Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	out := make(chan Message, 256)
	go func() {
		defer close(out)
		defer f.Close()
		var p Parser
		var buf [256]byte
		var msgs []Message
		// A separate goroutine watches ctx and force-closes the file so
		// the blocking Read returns. Without this the goroutine outlives
		// the engine on shutdown.
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = f.Close()
			case <-done:
			}
		}()
		defer close(done)
		for {
			n, err := f.Read(buf[:])
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return
				}
				return
			}
			msgs = p.Feed(buf[:n], msgs[:0])
			for _, m := range msgs {
				select {
				case out <- m:
				default:
					// channel full — drop. Logged at the bridge layer
					// where there's a logger handy.
				}
			}
		}
	}()
	return out, nil
}

// readCardNames returns a map of card index → friendly name parsed
// from /proc/asound/cards. Returns an empty map (not an error) on
// failure so List can degrade to /dev/snd device-file names.
func readCardNames() map[int]string {
	out := map[int]string{}
	body, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		return out
	}
	// Format is two lines per card:
	//   " 0 [name           ]: driver - long name"
	//   "                       extra info"
	// We pluck index + the [name] field.
	for _, line := range strings.Split(string(body), "\n") {
		idx, name, ok := parseCardLine(line)
		if !ok {
			continue
		}
		out[idx] = name
	}
	return out
}

func parseCardLine(line string) (int, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, "", false
	}
	// Expect: "<n> [name   ]: driver - long"
	openBr := strings.IndexByte(line, '[')
	closeBr := strings.IndexByte(line, ']')
	if openBr < 0 || closeBr < 0 || closeBr <= openBr {
		return 0, "", false
	}
	idxStr := strings.TrimSpace(line[:openBr])
	idx, err := atoiSafe(idxStr)
	if err != nil {
		return 0, "", false
	}
	name := strings.TrimSpace(line[openBr+1 : closeBr])
	return idx, name, true
}

func atoiSafe(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// parseMidiName extracts (card, device) from "midiCxDy".
func parseMidiName(name string) (int, int, bool) {
	// "midiC0D0" → card=0 device=0
	rest := strings.TrimPrefix(name, "midiC")
	if rest == name {
		return 0, 0, false
	}
	d := strings.IndexByte(rest, 'D')
	if d < 0 {
		return 0, 0, false
	}
	card, err := atoiSafe(rest[:d])
	if err != nil {
		return 0, 0, false
	}
	dev, err := atoiSafe(rest[d+1:])
	if err != nil {
		return 0, 0, false
	}
	return card, dev, true
}
