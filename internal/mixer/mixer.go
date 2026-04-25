package mixer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"

	"github.com/gen2brain/malgo"
)

const (
	// MaxVoices is the polyphony cap. When all voices are busy a new
	// trigger steals the oldest voice so the mixer never blocks or
	// silently drops triggers.
	MaxVoices = 32

	// masterGain scales the summed voice output to leave headroom above
	// full-scale so two maxed voices don't instantly clip. 0.7 ≈ -3 dB.
	masterGain = 0.7

	// deviceSampleRate is the output rate we request. 44.1 kHz is the
	// most common WAV rate so per-voice resampling is usually a no-op.
	deviceSampleRate = 44100
	deviceChannels   = 2

	// maxAudioBufferFrames caps the size of pre-allocated scratch
	// buffers (per-track buses, master direct path). Real audio
	// drivers typically deliver 256–4096 frame buffers; 8192 is a
	// safe ceiling that keeps the mixer alloc-free for any realistic
	// device. If a driver requests more, the callback truncates.
	maxAudioBufferFrames = 8192
)

// trigger is a one-shot message from the bus-subscribing goroutine to
// the audio callback. sample may be nil to signal "all off". pitchRatio
// is the multiplicative factor applied to the voice step rate — 1.0 is
// native pitch, 2.0 one octave up, 0.5 one octave down. chokeKey makes
// voices with the same key monophonic: a new trigger silences any
// active voice sharing the key before starting its own. Empty chokeKey
// means "polyphonic" — the voice coexists with any others. panL/panR
// are precomputed channel multipliers from a constant-power pan law.
type trigger struct {
	sample     *Sample
	gain       float32
	panL, panR float32
	pitchRatio float64
	chokeKey   string
	allOff     bool
}

// VoiceTrigger is the public spec for activating one voice. SampleID
// is required; everything else has a sane musical default so callers
// only fill in the fields they care about. PitchRatio defaults to 1.0
// (native), TrackGain to 1.0 (unity), Pan to 0.0 (centre), Vel is
// taken as 100 when zero.
type VoiceTrigger struct {
	SampleID   string
	Vel        int
	PitchRatio float64 // 1.0 = native; 0 also treated as native
	ChokeKey   string  // "" = polyphonic
	Pan        float32 // -1.0 .. 1.0; constant-power law, 0 = centre
	TrackGain  float32 // 1.0 = unity (multiplicative on top of velocity)
}

// voice is one playing instance of a sample. Only the audio callback
// mutates voice fields; the bus goroutine exclusively writes to the
// triggers channel and the callback drains it at the top of each buffer.
type voice struct {
	sample     *Sample
	phase      float64 // fractional frame index into sample.Data
	step       float64 // phase advance per device output frame
	gain       float32
	panL, panR float32 // precomputed channel multipliers from constant-power pan law
	chokeKey   string  // voices with matching key are mutually exclusive (Amiga-channel semantics)
	active     bool
	sequence   uint64 // monotonically increasing — oldest voice has the smallest value
}

// Mixer owns the audio device and a fixed voice pool. It is driven
// externally by feeding triggers via Trigger / AllOff; the audio thread
// inside malgo pulls samples through Callback at the rate the device
// demands.
type Mixer struct {
	log *log.Logger

	// Audio device (nil if we're running in test mode without a device).
	ctx    *malgo.AllocatedContext
	device *malgo.Device

	// Sample bank — guarded by sampleMu. Trigger (bus-goroutine) reads
	// it; SetSamples (engine-load path) rewrites it. The audio callback
	// never touches this map — it only sees *Sample pointers captured
	// into trigger messages, which remain valid because we don't reuse
	// Sample structs once published.
	sampleMu sync.RWMutex
	samples  map[string]*Sample

	// Triggers from the bus goroutine to the audio callback. Buffered
	// generously so a burst of simultaneous triggers doesn't block.
	triggers chan trigger

	// Voice pool — mutated only by the audio callback.
	voices     [MaxVoices]voice
	nextSeq    uint64
	deviceRate int // real device rate (may differ from request on some drivers)

	// Meter bank — master L/R and per-track levels. Atomically read
	// by the TUI; written from the audio callback after each buffer.
	meters *meterBank

	// Per-track buses for the EQ → comp DSP chain. Keyed by track ID
	// (= voice chokeKey). Pre-allocated by SetTrackBuses to avoid
	// audio-thread allocations; the audio thread reads buses[id] under
	// busesMu and accumulates into the bus's L/R scratch.
	busesMu     sync.RWMutex
	buses       map[string]*trackBus
	busMaxFrame int

	// directL/R is the master scratch for voices that don't belong to
	// a track bus (e.g., audition triggers before SetTrackBuses runs,
	// or voices whose chokeKey isn't in the buses map). Pre-allocated
	// to maxAudioBufferFrames so the audio callback never allocates.
	directL [maxAudioBufferFrames]float32
	directR [maxAudioBufferFrames]float32

	// Shutdown plumbing.
	closeOnce sync.Once
}

// New creates a mixer configured for the given sample bank. Samples are
// held by reference — don't free or overwrite the underlying data while
// the mixer runs.
func New(samples map[string]*Sample, logger *log.Logger) *Mixer {
	if logger == nil {
		logger = log.Default()
	}
	m := &Mixer{
		log:        logger,
		samples:    samples,
		triggers:   make(chan trigger, 256),
		deviceRate: deviceSampleRate,
		meters:     newMeterBank(),
	}
	return m
}

// SetTrackMeters tells the meter bank which track IDs should have
// dedicated per-track meters allocated. Typically called from the
// engine on EvLoaded with the set of [[tracks]] IDs from the song.
// Tracks not in this list silently drop off the meter (the bank just
// won't have a channel for them, so updateTrack is a no-op).
func (m *Mixer) SetTrackMeters(trackIDs []string) {
	m.meters.SetTracks(trackIDs)
}

// SetTrackBuses configures the per-track DSP chain (EQ + comp) for
// each known track. Allocates pre-sized scratch buffers and bakes
// filter coefficients from the configs at the device's current sample
// rate. Calling with an empty list clears the buses map — the mixer
// then sums voices straight into master without any per-track chain.
func (m *Mixer) SetTrackBuses(configs []TrackChainConfig, maxFrames int) {
	if maxFrames <= 0 {
		maxFrames = 4096
	}
	sampleRate := float64(m.deviceRate)
	if sampleRate <= 0 {
		sampleRate = deviceSampleRate
	}
	next := make(map[string]*trackBus, len(configs))
	for _, cfg := range configs {
		bus := &trackBus{
			bufL: make([]float32, maxFrames),
			bufR: make([]float32, maxFrames),
		}
		if cfg.EQOn {
			bus.eq.configure(cfg.EQ, sampleRate)
			bus.eqOn = true
		}
		if cfg.CompOn {
			bus.comp.configure(cfg.Comp, sampleRate)
			bus.compOn = true
		}
		next[cfg.ID] = bus
	}
	m.busesMu.Lock()
	m.buses = next
	m.busMaxFrame = maxFrames
	m.busesMu.Unlock()
}

// MasterLevels returns the current master L/R meter reading.
func (m *Mixer) MasterLevels() (l, r MeterReading) {
	return m.meters.Master()
}

// TrackLevels returns the current per-track L/R meter reading. Both
// channels are zero when the trackID isn't currently metered.
func (m *Mixer) TrackLevels(trackID string) (l, r MeterReading) {
	return m.meters.Track(trackID)
}

// Start opens the system default audio output and begins playback.
// Returns a cleanup error if the device can't be opened — callers should
// log this and fall back to MIDI-only rather than aborting the daemon.
func (m *Mixer) Start() error {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		// Swallow backend chatter; malgo emits a lot on startup.
	})
	if err != nil {
		return fmt.Errorf("malgo init: %w", err)
	}
	m.ctx = ctx

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = deviceChannels
	cfg.SampleRate = deviceSampleRate
	cfg.Alsa.NoMMap = 1

	cb := malgo.DeviceCallbacks{
		Data: m.onData,
	}
	dev, err := malgo.InitDevice(ctx.Context, cfg, cb)
	if err != nil {
		_ = ctx.Uninit()
		return fmt.Errorf("malgo init device: %w", err)
	}
	m.device = dev
	if rate := int(dev.SampleRate()); rate > 0 {
		m.deviceRate = rate
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		_ = ctx.Uninit()
		return fmt.Errorf("malgo device start: %w", err)
	}
	m.log.Printf("mixer: audio device open — %d Hz, %d ch, %d voices",
		m.deviceRate, deviceChannels, MaxVoices)
	return nil
}

// Stop halts playback and releases the device. Idempotent.
func (m *Mixer) Stop() {
	m.closeOnce.Do(func() {
		if m.device != nil {
			m.device.Uninit()
		}
		if m.ctx != nil {
			_ = m.ctx.Uninit()
			m.ctx.Free()
		}
	})
}

// SetSamples atomically replaces the current sample bank. Safe to call
// while audio is playing; in-flight voices keep playing from their
// captured *Sample pointers (which remain reachable via the old map's
// values until GC).
func (m *Mixer) SetSamples(s map[string]*Sample) {
	m.sampleMu.Lock()
	m.samples = s
	m.sampleMu.Unlock()
}

// Trigger enqueues a new sample playback at native pitch, polyphonic
// (no choke). Convenience wrapper around Play.
func (m *Mixer) Trigger(sampleID string, vel int) {
	m.Play(VoiceTrigger{SampleID: sampleID, Vel: vel})
}

// TriggerPitched is Trigger with an explicit pitch ratio. Convenience
// wrapper around Play.
func (m *Mixer) TriggerPitched(sampleID string, vel int, pitchRatio float64) {
	m.Play(VoiceTrigger{SampleID: sampleID, Vel: vel, PitchRatio: pitchRatio})
}

// TriggerVoice is Trigger with explicit pitch + choke key. Convenience
// wrapper around Play.
func (m *Mixer) TriggerVoice(sampleID string, vel int, pitchRatio float64, chokeKey string) {
	m.Play(VoiceTrigger{
		SampleID:   sampleID,
		Vel:        vel,
		PitchRatio: pitchRatio,
		ChokeKey:   chokeKey,
	})
}

// Play is the canonical trigger entry point. Resolves defaults, clamps
// pathological values, and pushes a trigger message onto the audio
// queue. Safe to call from any goroutine. Drops on a full queue with
// a log line — losing one hit is preferable to blocking the engine
// when the audio thread is stalled.
func (m *Mixer) Play(t VoiceTrigger) {
	m.sampleMu.RLock()
	s, ok := m.samples[t.SampleID]
	m.sampleMu.RUnlock()
	if !ok || s == nil {
		return
	}
	gain := float32(t.Vel) / 127.0
	if gain <= 0 {
		gain = 0.78 // velocity 100 default when the engine sends 0
	}
	if gain > 1 {
		gain = 1
	}
	// Per-track gain multiplies on top of velocity. 0 → unity for
	// back-compat (zero-value VoiceTrigger from helpers).
	tg := t.TrackGain
	if tg == 0 {
		tg = 1
	}
	gain *= tg
	pitchRatio := t.PitchRatio
	if pitchRatio <= 0 || math.IsNaN(pitchRatio) || math.IsInf(pitchRatio, 0) {
		pitchRatio = 1.0
	}
	if pitchRatio < 1.0/64 {
		pitchRatio = 1.0 / 64
	} else if pitchRatio > 64 {
		pitchRatio = 64
	}
	panL, panR := panLaw(t.Pan)
	select {
	case m.triggers <- trigger{
		sample:     s,
		gain:       gain,
		panL:       panL,
		panR:       panR,
		pitchRatio: pitchRatio,
		chokeKey:   t.ChokeKey,
	}:
	default:
		m.log.Printf("mixer: trigger queue full, dropped %q", t.SampleID)
	}
}

// panLaw returns the L/R channel multipliers for a constant-power pan
// position in [-1, 1]. At pan=0 both channels see ~0.707 (-3 dB), so
// summed energy stays the same as a single mono signal at unity. Out-
// of-range or non-finite inputs collapse to centre.
func panLaw(pan float32) (l, r float32) {
	if math.IsNaN(float64(pan)) || math.IsInf(float64(pan), 0) {
		pan = 0
	}
	if pan < -1 {
		pan = -1
	} else if pan > 1 {
		pan = 1
	}
	// Map -1..1 to 0..π/2: -1 → 0 (full left), +1 → π/2 (full right).
	angle := float64(pan+1) * (math.Pi / 4)
	l = float32(math.Cos(angle))
	r = float32(math.Sin(angle))
	return l, r
}

// AllOff silences every active voice on the next audio callback. Used by
// the engine's all-off event on stop/seek/load so held tails don't leak
// into the next musical moment.
func (m *Mixer) AllOff() {
	select {
	case m.triggers <- trigger{allOff: true}:
	default:
	}
}

// onData is the audio callback invoked by miniaudio at high priority.
// Keep it allocation-free; drain triggers with a non-blocking loop then
// sum all active voices into the output buffer.
func (m *Mixer) onData(outBytes, _ []byte, frames uint32) {
	// Drain triggers first so events that arrived between buffers apply
	// to this buffer's output.
	for {
		select {
		case t := <-m.triggers:
			if t.allOff {
				for i := range m.voices {
					m.voices[i].active = false
				}
			} else if t.sample != nil {
				m.assignVoice(t)
			}
		default:
			goto drained
		}
	}
drained:

	nFrames := int(frames)
	if nFrames > maxAudioBufferFrames {
		nFrames = maxAudioBufferFrames
	}

	// Snapshot the buses map. SetTrackBuses swaps the map pointer
	// under a write lock; the audio thread holds an RLock just long
	// enough to grab the current snapshot, then iterates without it.
	m.busesMu.RLock()
	buses := m.buses
	m.busesMu.RUnlock()

	// Reset bus + direct scratch to silence so this buffer's voices
	// accumulate into a clean slate.
	for _, bus := range buses {
		bus.reset(nFrames)
	}
	directL := m.directL[:nFrames]
	directR := m.directR[:nFrames]
	for i := range directL {
		directL[i] = 0
		directR[i] = 0
	}

	// Per-voice peak accumulators for the meter bank. We meter at the
	// pre-bus point (raw voice contribution post-pan/gain). Bus
	// effects can colour and re-amplify the signal, so the per-track
	// meter is a "what was sent to the bus" reading rather than a
	// post-effect output reading — close enough for visual feedback,
	// and means a flat-EQ track meters identically to before.
	var voicePeakL [MaxVoices]float32
	var voicePeakR [MaxVoices]float32

	// 1) Mix every active voice into either its track's bus or the
	//    direct master scratch (when no bus exists for the chokeKey).
	for v := range m.voices {
		vp := &m.voices[v]
		if !vp.active {
			continue
		}
		var bufL, bufR []float32
		if bus := buses[vp.chokeKey]; bus != nil {
			bufL = bus.bufL[:nFrames]
			bufR = bus.bufR[:nFrames]
		} else {
			bufL = directL
			bufR = directR
		}
		for f := 0; f < nFrames; f++ {
			if !vp.active {
				break
			}
			sl, sr := sampleAt(vp.sample, vp.phase)
			cl := sl * vp.gain * vp.panL
			cr := sr * vp.gain * vp.panR
			bufL[f] += cl
			bufR[f] += cr
			if a := absFloat(cl); a > voicePeakL[v] {
				voicePeakL[v] = a
			}
			if a := absFloat(cr); a > voicePeakR[v] {
				voicePeakR[v] = a
			}
			vp.phase += vp.step
			if int(vp.phase) >= vp.sample.FrameCount() {
				vp.active = false
			}
		}
	}

	// 2) Run each bus's effect chain over its own scratch (in place).
	for _, bus := range buses {
		bus.process(nFrames)
	}

	// 3) Sum direct + bus outputs into the master, apply masterGain,
	//    write the interleaved float32 stereo output, and capture the
	//    master peak for the meter.
	var masterPeakL, masterPeakR float32
	for f := 0; f < nFrames; f++ {
		l := directL[f]
		r := directR[f]
		for _, bus := range buses {
			l += bus.bufL[f]
			r += bus.bufR[f]
		}
		l *= masterGain
		r *= masterGain
		if a := absFloat(l); a > masterPeakL {
			masterPeakL = a
		}
		if a := absFloat(r); a > masterPeakR {
			masterPeakR = a
		}
		writeF32LE(outBytes, int(f*8+0), l)
		writeF32LE(outBytes, int(f*8+4), r)
	}

	// Push this buffer's peaks into the meter bank. Per-track values
	// fold across all voices that share a chokeKey (track id) — kick
	// retriggers within a buffer combine, so the meter reflects the
	// total energy of that track this buffer.
	if m.meters != nil {
		updateMeter(&m.meters.masterL, masterPeakL)
		updateMeter(&m.meters.masterR, masterPeakR)
		var trackL [MaxVoices]float32
		var trackR [MaxVoices]float32
		var trackKeys [MaxVoices]string
		nKeys := 0
		merge := func(key string, l, r float32) {
			for k := 0; k < nKeys; k++ {
				if trackKeys[k] == key {
					if l > trackL[k] {
						trackL[k] = l
					}
					if r > trackR[k] {
						trackR[k] = r
					}
					return
				}
			}
			trackKeys[nKeys] = key
			trackL[nKeys] = l
			trackR[nKeys] = r
			nKeys++
		}
		for v := range m.voices {
			if voicePeakL[v] == 0 && voicePeakR[v] == 0 {
				continue
			}
			merge(m.voices[v].chokeKey, voicePeakL[v], voicePeakR[v])
		}
		for k := 0; k < nKeys; k++ {
			m.meters.updateTrack(trackKeys[k], trackL[k], trackR[k])
		}
		// Decay tracks that didn't sound this buffer so the meter
		// drops back to silence cleanly. Only meters we know about.
		m.meters.mu.RLock()
		for id, cl := range m.meters.tracksL {
			seen := false
			for k := 0; k < nKeys; k++ {
				if trackKeys[k] == id {
					seen = true
					break
				}
			}
			if !seen {
				updateMeter(cl, 0)
				if cr := m.meters.tracksR[id]; cr != nil {
					updateMeter(cr, 0)
				}
			}
		}
		m.meters.mu.RUnlock()
	}
}

func absFloat(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// sampleAt returns left/right amplitudes at a fractional frame index with
// linear interpolation, handling mono-to-stereo duplication.
func sampleAt(s *Sample, phase float64) (float32, float32) {
	i := int(phase)
	frac := float32(phase - float64(i))
	nFrames := s.FrameCount()
	if i+1 >= nFrames {
		// Near the tail: hold the final frame. Voice will be marked
		// inactive on the next iteration.
		if s.NumChans == 1 {
			x := s.Data[nFrames-1]
			return x, x
		}
		return s.Data[(nFrames-1)*2], s.Data[(nFrames-1)*2+1]
	}
	if s.NumChans == 1 {
		a := s.Data[i]
		b := s.Data[i+1]
		x := a + (b-a)*frac
		return x, x
	}
	// Stereo: interpolate each channel independently.
	al := s.Data[i*2]
	ar := s.Data[i*2+1]
	bl := s.Data[(i+1)*2]
	br := s.Data[(i+1)*2+1]
	return al + (bl-al)*frac, ar + (br-ar)*frac
}

// assignVoice finds a free voice — or steals the oldest one if all
// are busy — and starts it playing the trigger's sample. Step =
// nativeResampleRatio * pitchRatio, so transposition works like
// changing the sample rate. When chokeKey is non-empty, any active
// voice carrying the same key is silenced first so the new voice
// replaces it (one-voice-per-track).
func (m *Mixer) assignVoice(t trigger) {
	if t.chokeKey != "" {
		for i := range m.voices {
			if m.voices[i].active && m.voices[i].chokeKey == t.chokeKey {
				m.voices[i].active = false
			}
		}
	}
	idx := -1
	var oldestSeq uint64 = math.MaxUint64
	for i := range m.voices {
		if !m.voices[i].active {
			idx = i
			break
		}
		if m.voices[i].sequence < oldestSeq {
			oldestSeq = m.voices[i].sequence
			idx = i
		}
	}
	if idx < 0 {
		return
	}
	pitchRatio := t.pitchRatio
	if pitchRatio <= 0 {
		pitchRatio = 1.0
	}
	m.nextSeq++
	m.voices[idx] = voice{
		sample:   t.sample,
		phase:    0,
		step:     (float64(t.sample.SampleRate) / float64(m.deviceRate)) * pitchRatio,
		gain:     t.gain,
		panL:     t.panL,
		panR:     t.panR,
		chokeKey: t.chokeKey,
		active:   true,
		sequence: m.nextSeq,
	}
}

// writeF32LE writes a float32 at offset in little-endian byte order.
func writeF32LE(buf []byte, off int, v float32) {
	if off+4 > len(buf) {
		return
	}
	bits := math.Float32bits(v)
	binary.LittleEndian.PutUint32(buf[off:off+4], bits)
}

// ActiveVoices is a debugging/testing helper returning how many voices
// are currently playing.
func (m *Mixer) ActiveVoices() int {
	n := 0
	for i := range m.voices {
		if m.voices[i].active {
			n++
		}
	}
	return n
}

// errTestOnly is a placeholder used by tests that skip device startup.
var errTestOnly = errors.New("test mode")

// NewForTest builds a mixer without opening an audio device. Used by
// unit tests to exercise trigger/voice logic via direct onData calls.
func NewForTest(samples map[string]*Sample) *Mixer {
	return &Mixer{
		log:        log.Default(),
		samples:    samples,
		triggers:   make(chan trigger, 256),
		deviceRate: deviceSampleRate,
		meters:     newMeterBank(),
	}
}
