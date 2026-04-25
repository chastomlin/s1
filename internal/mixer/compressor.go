package mixer

import "math"

// compressor is a simple feedforward dynamics processor with a
// stereo-linked peak detector. Linear-domain reduction (target =
// threshold + (peak-threshold)/ratio) keeps the inner loop free of
// pow/log; the curve is a slightly-different shape from a strict-dB
// compressor but indistinguishable on a tracker drum bus.
//
// State is just the smoothed gain reduction (envelope). Coefficients
// are precomputed by configure(); the inner loop does a peak-find,
// one branch (over/under threshold), one division, and an env update.
type compressor struct {
	thresholdLin float32
	ratioInv     float32 // 1.0 / ratio — pre-divided
	attackCoef   float32
	releaseCoef  float32
	makeupLin    float32

	envelope float32 // smoothed gain reduction; idle = 1.0 (no reduction)
}

// configure rebuilds coefficients from a config + sample rate. Attack
// and release are exponential time constants in milliseconds: per-
// sample coefficient = exp(-1 / (ms * 0.001 * fs)).
func (c *compressor) configure(cfg CompParams, sampleRate float64) {
	c.thresholdLin = float32(math.Pow(10, float64(cfg.ThresholdDB)/20))
	if cfg.Ratio < 1.0 {
		cfg.Ratio = 1.0
	}
	c.ratioInv = 1.0 / cfg.Ratio
	c.attackCoef = float32(timeConstantCoef(float64(cfg.AttackMs), sampleRate))
	c.releaseCoef = float32(timeConstantCoef(float64(cfg.ReleaseMs), sampleRate))
	c.makeupLin = float32(math.Pow(10, float64(cfg.MakeupDB)/20))
	c.envelope = 1.0
}

// process compresses one stereo buffer in place. Detector is the
// max-abs of the L+R sample (stereo-linked: both channels get the
// same gain so the centre image stays put rather than wandering when
// one side momentarily exceeds threshold).
func (c *compressor) process(bufL, bufR []float32) {
	n := len(bufL)
	if n > len(bufR) {
		n = len(bufR)
	}
	for i := 0; i < n; i++ {
		// Stereo-linked peak detector.
		peak := absFloat(bufL[i])
		if a := absFloat(bufR[i]); a > peak {
			peak = a
		}

		// Instantaneous gain reduction. Linear-domain target of
		// threshold + (peak-threshold)/ratio; reduction = target/peak.
		// 1.0 means "no reduction" — keeps the math friendly when peak
		// is below threshold or zero.
		gr := float32(1.0)
		if peak > c.thresholdLin && peak > 0 {
			target := c.thresholdLin + (peak-c.thresholdLin)*c.ratioInv
			gr = target / peak
		}

		// Envelope smoothing. Attack when reducing further (gr drops),
		// release when easing back up. coef→1 = slow; coef→0 = instant.
		if gr < c.envelope {
			c.envelope = c.attackCoef*c.envelope + (1-c.attackCoef)*gr
		} else {
			c.envelope = c.releaseCoef*c.envelope + (1-c.releaseCoef)*gr
		}

		appliedGain := c.envelope * c.makeupLin
		bufL[i] *= appliedGain
		bufR[i] *= appliedGain
	}
}

// timeConstantCoef returns the per-sample exponential coefficient for
// a smoothing filter with the given time constant in milliseconds.
// Standard one-pole: y[n] = c*y[n-1] + (1-c)*x[n] where c = e^(-1/τ).
// Returns 0 (instant response) for sub-sample time constants and
// clamps near 1.0 for absurdly long ones.
func timeConstantCoef(ms, sampleRate float64) float64 {
	if ms <= 0 {
		return 0
	}
	samples := ms * 0.001 * sampleRate
	if samples < 1 {
		return 0
	}
	c := math.Exp(-1.0 / samples)
	if c > 0.99999 {
		c = 0.99999
	}
	return c
}

