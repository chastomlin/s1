package mixer

import "math"

// FilterMode picks the response curve. Public so the mixer config can
// pass a typed value rather than a string; the song-side string is
// mapped at configure time by callers.
type FilterMode int

const (
	FilterModeLowpass FilterMode = iota
	FilterModeHighpass
	FilterModeBandpass
)

// svFilter is a resonant 2-pole filter built on the existing biquad
// (RBJ cookbook coefficients). Despite the name — kept for API
// continuity — the implementation is now a single biquad re-tuned
// per buffer for the chosen mode. RBJ formulas are unconditionally
// stable across the full cutoff range and self-oscillate cleanly at
// high Q, which is what we want for a musical "classic" filter.
//
// Resonance 0..1 maps to Q 0.5..10. Below ~0.7 the response is just
// the rolloff slope; from 0.7 upward a peak builds at the cutoff and
// gets sharper toward 1.0.
type svFilter struct {
	bq   biquad
	mode FilterMode
}

// configure recomputes biquad coefficients for the chosen mode at the
// given device sample rate. State is preserved so parameter changes
// during playback don't pop — biquads relax to new coefficients in a
// few samples even with stale z1/z2.
func (s *svFilter) configure(p FilterParams, sampleRate float64) {
	cutoff := float64(p.Cutoff)
	maxC := sampleRate * 0.49 // stay just below Nyquist
	if cutoff < 20 {
		cutoff = 20
	} else if cutoff > maxC {
		cutoff = maxC
	}
	res := p.Resonance
	if res < 0 {
		res = 0
	} else if res > 1 {
		res = 1
	}
	// 0.5 = no peak (over-damped), ~0.707 = Butterworth, 10 = sharp
	// resonance peak. The 9.5 multiplier lands the upper limit at the
	// edge of stable self-oscillation for the RBJ topology.
	Q := 0.5 + float64(res)*9.5

	w0 := 2 * math.Pi * cutoff / sampleRate
	cosw := math.Cos(w0)
	sinw := math.Sin(w0)
	alpha := sinw / (2 * Q)

	a0 := 1 + alpha
	a1 := -2 * cosw
	a2 := 1 - alpha

	var b0, b1, b2 float64
	switch p.Mode {
	case FilterModeHighpass:
		b0 = (1 + cosw) / 2
		b1 = -(1 + cosw)
		b2 = (1 + cosw) / 2
	case FilterModeBandpass:
		// Constant peak gain (0 dB at fc): b0 = α, b1 = 0, b2 = -α.
		b0 = alpha
		b1 = 0
		b2 = -alpha
	default: // lowpass
		b0 = (1 - cosw) / 2
		b1 = 1 - cosw
		b2 = (1 - cosw) / 2
	}

	s.bq.b0 = float32(b0 / a0)
	s.bq.b1 = float32(b1 / a0)
	s.bq.b2 = float32(b2 / a0)
	s.bq.a1 = float32(a1 / a0)
	s.bq.a2 = float32(a2 / a0)
	s.mode = p.Mode
}

// process runs the filter over an L/R buffer pair in place.
func (s *svFilter) process(bufL, bufR []float32) {
	n := len(bufL)
	if n > len(bufR) {
		n = len(bufR)
	}
	for i := 0; i < n; i++ {
		l, r := s.bq.processStereo(bufL[i], bufR[i])
		bufL[i] = l
		bufR[i] = r
	}
}
