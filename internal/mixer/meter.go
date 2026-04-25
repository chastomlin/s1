package mixer

import (
	"math"
	"sync"
	"sync/atomic"
)

// MeterReading is a snapshot of one channel's level. Level is a smoothed
// VU-style amplitude (fast attack, slow release); Peak is a fast-attack
// peak with hold-and-decay for a programme-meter overlay. Both are in
// [0, 1] (linear, not dB).
type MeterReading struct {
	Level float32
	Peak  float32
}

// meterChannel holds the running level and peak for one channel of one
// meter. Updates run on the audio thread; the TUI reads via atomic
// loads (stale-OK at meter granularity). Float32 is round-tripped
// through the bit pattern in atomic.Uint32 — the values themselves are
// always finite and non-negative.
type meterChannel struct {
	levelBits atomic.Uint32 // math.Float32bits
	peakBits  atomic.Uint32
}

func (c *meterChannel) read() MeterReading {
	return MeterReading{
		Level: math.Float32frombits(c.levelBits.Load()),
		Peak:  math.Float32frombits(c.peakBits.Load()),
	}
}

func (c *meterChannel) store(level, peak float32) {
	c.levelBits.Store(math.Float32bits(level))
	c.peakBits.Store(math.Float32bits(peak))
}

// Time-constant approximations chosen to feel right at typical audio
// buffer rates (~5–20 ms). Tuned by ear, not lab — fast enough to feel
// responsive, slow enough that you can read it without your eyes
// twitching.
const (
	meterVUAttack   = 0.45  // approach incoming peak quickly when it rises
	meterVURelease  = 0.04  // slow drop when the signal quiets — VU-like
	meterPeakDecay  = 0.985 // ~1 s hold-then-decay for the peak indicator
	meterFloorBelow = 1e-4  // pin to zero below this so the meter rests cleanly at silence
)

// update folds one buffer's worth of channel peak (max abs sample) into
// the meter. Always increases level on a louder buffer (attack); slowly
// drops level when the signal quiets (release). Peak follows instantly
// up, decays exponentially down.
func updateMeter(c *meterChannel, bufferPeak float32) {
	cur := c.read()
	level := cur.Level
	peak := cur.Peak

	if bufferPeak > level {
		level += (bufferPeak - level) * meterVUAttack
	} else {
		level += (bufferPeak - level) * meterVURelease
	}
	if level < meterFloorBelow {
		level = 0
	}
	if bufferPeak > peak {
		peak = bufferPeak
	} else {
		peak *= meterPeakDecay
	}
	if peak < meterFloorBelow {
		peak = 0
	}

	c.store(level, peak)
}

// meterBank holds the master L/R meter plus per-track meters keyed by
// chokeKey (track ID). The set of per-track keys is fixed when a song
// loads (via SetTracks) so the audio thread can index into the map
// without growing it; updates after that are pure stores.
type meterBank struct {
	mu sync.RWMutex // guards the tracks map's identity (not the meterChannel cells)

	masterL meterChannel
	masterR meterChannel

	tracksL map[string]*meterChannel
	tracksR map[string]*meterChannel
}

func newMeterBank() *meterBank {
	return &meterBank{
		tracksL: map[string]*meterChannel{},
		tracksR: map[string]*meterChannel{},
	}
}

// SetTracks replaces the per-track meter map atomically. Called when a
// song loads or reloads — keeps the audio-thread reads safe by handing
// it new map pointers wholesale. Existing meterChannel pointers stay
// reachable for any in-flight reads.
func (b *meterBank) SetTracks(trackIDs []string) {
	l := make(map[string]*meterChannel, len(trackIDs))
	r := make(map[string]*meterChannel, len(trackIDs))
	for _, id := range trackIDs {
		l[id] = &meterChannel{}
		r[id] = &meterChannel{}
	}
	b.mu.Lock()
	b.tracksL = l
	b.tracksR = r
	b.mu.Unlock()
}

// updateTrack folds a per-track buffer-peak pair into the right meter
// channels. No-op for unknown track IDs (e.g., audition before a song
// loads, or trigger from a track that was deleted mid-buffer).
func (b *meterBank) updateTrack(trackID string, peakL, peakR float32) {
	if trackID == "" {
		return
	}
	b.mu.RLock()
	cl := b.tracksL[trackID]
	cr := b.tracksR[trackID]
	b.mu.RUnlock()
	if cl != nil {
		updateMeter(cl, peakL)
	}
	if cr != nil {
		updateMeter(cr, peakR)
	}
}

// Master returns the master L/R reading. Safe to call from any
// goroutine; reads are atomic loads and may briefly observe a
// just-stored level paired with the previous peak (or vice versa) —
// inconsequential at TUI refresh rates.
func (b *meterBank) Master() (l, r MeterReading) {
	return b.masterL.read(), b.masterR.read()
}

// Track returns the L/R reading for trackID. Both channels are zero
// when the track isn't known to the meter bank.
func (b *meterBank) Track(trackID string) (l, r MeterReading) {
	b.mu.RLock()
	cl := b.tracksL[trackID]
	cr := b.tracksR[trackID]
	b.mu.RUnlock()
	if cl != nil {
		l = cl.read()
	}
	if cr != nil {
		r = cr.read()
	}
	return l, r
}
