// Package protocol defines the wire format between the TUI client and the
// engine daemon. It's line-delimited JSON over a Unix domain socket.
//
// Commands flow client -> engine. Events flow engine -> client.
package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultSocketPath returns the conventional socket path for the engine.
// Uses $XDG_RUNTIME_DIR if set, falls back to /tmp.
func DefaultSocketPath(runtimeDir string) string {
	if runtimeDir == "" {
		return "/tmp/seqoned.sock"
	}
	return runtimeDir + "/seqoned.sock"
}

// --- Commands ---------------------------------------------------------------

type CmdKind string

const (
	CmdLoad     CmdKind = "load"
	CmdReload   CmdKind = "reload"
	CmdPlay     CmdKind = "play"
	CmdStop     CmdKind = "stop"
	CmdPause    CmdKind = "pause"
	CmdSeek     CmdKind = "seek"
	CmdLoop     CmdKind = "loop"
	CmdSetTempo CmdKind = "set_tempo"
	CmdMute     CmdKind = "mute"
	CmdSolo     CmdKind = "solo"
	CmdAudition     CmdKind = "audition"
	CmdSetTrackPan  CmdKind = "set_track_pan"
	CmdSetTrackGain CmdKind = "set_track_gain"
	CmdSetTrackEQ     CmdKind = "set_track_eq"
	CmdSetTrackComp   CmdKind = "set_track_comp"
	CmdSetTrackFilter CmdKind = "set_track_filter"
	CmdSetTrackLofi   CmdKind = "set_track_lofi"
	CmdSetTrackDrive  CmdKind = "set_track_drive"
	CmdSetTrackReverb CmdKind = "set_track_reverb"
	// CmdSetMidiInTrack tells the engine which track receives Note On/Off
	// from the configured MIDI input device (-midi-in). The TUI sends this
	// whenever the user moves the section-view selection so live keys
	// follow whatever's highlighted. Empty Track disables routing.
	CmdSetMidiInTrack CmdKind = "set_midi_in_track"
	CmdQuit           CmdKind = "quit"
)

// Command is the envelope the TUI sends to the engine.
// Only fields relevant to Cmd are populated.
type Command struct {
	Cmd CmdKind `json:"cmd"`

	Path    string `json:"path,omitempty"`
	Bar     int    `json:"bar,omitempty"`
	Beat    int    `json:"beat,omitempty"`
	FromBar int    `json:"from_bar,omitempty"`
	ToBar   int    `json:"to_bar,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
	BPM     int    `json:"bpm,omitempty"`
	Track   string `json:"track,omitempty"`
	Muted   bool   `json:"muted,omitempty"`
	Soloed  bool   `json:"soloed,omitempty"`

	// audition
	Note int `json:"note,omitempty"`
	Vel  int `json:"vel,omitempty"`

	// set_track_pan / set_track_gain
	Pan  float32 `json:"pan,omitempty"`
	Gain float32 `json:"gain,omitempty"`

	// set_track_eq
	EQ *EQConfigCmd `json:"eq,omitempty"`

	// set_track_comp
	Comp *CompConfigCmd `json:"comp,omitempty"`

	// set_track_filter
	Filter *FilterConfigCmd `json:"filter,omitempty"`

	// set_track_lofi
	Lofi *LofiConfigCmd `json:"lofi,omitempty"`

	// set_track_drive
	Drive *DriveConfigCmd `json:"drive,omitempty"`

	// set_track_reverb
	Reverb *ReverbConfigCmd `json:"reverb,omitempty"`
}

// EQConfigCmd carries a track's full 3-band EQ state in a single
// transient command. The TUI mix view pushes one of these on any
// param change so the engine can update the in-memory song atomically.
// Enabled is the explicit bypass flag, sent on every push so toggling
// the bypass round-trips without disturbing the band settings.
type EQConfigCmd struct {
	Enabled  bool    `json:"enabled"`
	LowFreq  float32 `json:"low_freq"`
	LowGain  float32 `json:"low_gain"`
	MidFreq  float32 `json:"mid_freq"`
	MidQ     float32 `json:"mid_q"`
	MidGain  float32 `json:"mid_gain"`
	HighFreq float32 `json:"high_freq"`
	HighGain float32 `json:"high_gain"`
}

// CompConfigCmd carries the compressor params for set_track_comp.
type CompConfigCmd struct {
	Enabled     bool    `json:"enabled"`
	ThresholdDB float32 `json:"threshold_db"`
	Ratio       float32 `json:"ratio"`
	AttackMs    float32 `json:"attack_ms"`
	ReleaseMs   float32 `json:"release_ms"`
	MakeupDB    float32 `json:"makeup_db"`
}

// FilterConfigCmd carries the resonant-filter params. Type is the
// SVF mode string ("lowpass" / "highpass" / "bandpass").
type FilterConfigCmd struct {
	Enabled   bool    `json:"enabled"`
	Type      string  `json:"type"`
	Cutoff    float32 `json:"cutoff"`
	Resonance float32 `json:"resonance"`
}

// LofiConfigCmd carries the bitcrush + sample-rate-reduction params.
type LofiConfigCmd struct {
	Enabled bool    `json:"enabled"`
	Bits    int     `json:"bits"`
	Rate    float32 `json:"rate"`
}

// DriveConfigCmd carries the overdrive params. Type is the saturator
// curve string ("soft" / "hard" / "fold").
type DriveConfigCmd struct {
	Enabled bool    `json:"enabled"`
	Type    string  `json:"type"`
	Drive   float32 `json:"drive"`
	Tone    float32 `json:"tone"`
	Level   float32 `json:"level"`
}

// ReverbConfigCmd carries the Freeverb-style reverb params.
type ReverbConfigCmd struct {
	Enabled bool    `json:"enabled"`
	Size    float32 `json:"size"`
	Damping float32 `json:"damping"`
	Mix     float32 `json:"mix"`
}

// --- Events -----------------------------------------------------------------

type EventKind string

const (
	EvLoaded       EventKind = "loaded"
	EvState        EventKind = "state"
	EvPosition     EventKind = "position"
	EvNote         EventKind = "note"
	// EvCC carries a MIDI Control Change event the engine wants forwarded
	// out of the RTP-MIDI bridge unmodified. Used today for pass-through
	// of mod wheel / sustain from a hardware controller; the engine
	// publishes one of these per inbound CC event on a pitched track.
	EvCC           EventKind = "cc"
	EvMeter        EventKind = "meter"
	EvTrackChanged EventKind = "track_changed"
	EvError        EventKind = "error"
)

// NoteKind distinguishes the musical sub-types of an EvNote event.
type NoteKind string

const (
	NoteOn        NoteKind = "on"
	NoteOff       NoteKind = "off"
	SampleTrigger NoteKind = "trigger"
	// NoteAllOff is a hard cleanup — silence all sample voices and
	// release every held MIDI note. Sent on stop, load, and seek.
	NoteAllOff NoteKind = "all_off"
	// NoteHoldsRelease is a soft cleanup for loop wrap — release any
	// held MIDI notes (so pitched ties don't hang past the wrap) but
	// let sample voices decay naturally so their tails don't cut off
	// at the loop boundary. The mixer ignores this event; the MIDI
	// bridge treats it like NoteAllOff (CC 123 on every channel).
	NoteHoldsRelease NoteKind = "holds_release"
)

type TransportState string

const (
	StateStopped TransportState = "stopped"
	StatePlaying TransportState = "playing"
	StatePaused  TransportState = "paused"
)

// Event is the envelope the engine sends to the TUI.
type Event struct {
	Event EventKind `json:"event"`

	// loaded
	Patterns     int      `json:"patterns,omitempty"`
	DurationBars int      `json:"duration_bars,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`

	// state
	State TransportState `json:"state,omitempty"`

	// position
	Bar     int    `json:"bar,omitempty"`
	Beat    int    `json:"beat,omitempty"`
	Tick    int    `json:"tick,omitempty"`
	Section string `json:"section,omitempty"`

	// note
	NoteKind NoteKind `json:"note_kind,omitempty"`
	Track    string   `json:"track,omitempty"`
	Note     int      `json:"note,omitempty"`
	Vel      int      `json:"vel,omitempty"`

	// cc — populated for EvCC. Channel is 1..16 (matches Track.Channel);
	// CC and CCValue are the standard MIDI 0..127 numbers.
	Channel int `json:"channel,omitempty"`
	CC      int `json:"cc,omitempty"`
	CCValue int `json:"cc_value,omitempty"`

	// meter — published periodically (~20 Hz) when audio is enabled.
	// Master is the post-master-gain L/R reading; MeterTracks carries
	// per-track L/R for every track in the loaded song.
	Master       MeterReadings            `json:"master,omitempty"`
	MeterTracks  map[string]MeterReadings `json:"meter_tracks,omitempty"`

	// error
	Severity string `json:"severity,omitempty"`
	Msg      string `json:"msg,omitempty"`
}

// MeterReadings is one stereo meter snapshot. LL/RL are the smoothed
// VU-style amplitudes (slow); LP/RP are the fast-attack peak indicators
// for the programme-meter overlay. All values are linear in [0, 1].
type MeterReadings struct {
	LL float32 `json:"ll,omitempty"`
	RL float32 `json:"rl,omitempty"`
	LP float32 `json:"lp,omitempty"`
	RP float32 `json:"rp,omitempty"`
}

// --- Framing ----------------------------------------------------------------

// WriteCommand writes one command followed by '\n'.
func WriteCommand(w io.Writer, c Command) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// WriteEvent writes one event followed by '\n'.
func WriteEvent(w io.Writer, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ReadCommands yields each decoded Command from r until EOF or error.
// Scanner buffer is large enough for any realistic command.
func ReadCommands(r io.Reader, fn func(Command) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		var c Command
		if err := json.Unmarshal(s.Bytes(), &c); err != nil {
			return fmt.Errorf("decode command: %w", err)
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return s.Err()
}

// ReadEvents yields each decoded Event from r until EOF or error.
func ReadEvents(r io.Reader, fn func(Event) error) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		var e Event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return s.Err()
}
