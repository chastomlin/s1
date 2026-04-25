package mixer

import "math"

// biquad is a single second-order IIR section in transposed direct
// form II. b0/b1/b2 + a1/a2 are coefficients (a0 normalised to 1);
// z1/z2 carry state across samples. Per-channel state lives in
// caller-owned pairs so a stereo filter is just two biquads sharing
// coefficients but with independent z1/z2.
type biquad struct {
	b0, b1, b2 float32
	a1, a2     float32
	z1L, z2L   float32 // L channel state
	z1R, z2R   float32 // R channel state
}

// processStereo runs one stereo sample through the filter and returns
// the output. Allocation-free, branch-free — safe for the audio
// thread's inner loop.
func (q *biquad) processStereo(xL, xR float32) (yL, yR float32) {
	yL = q.b0*xL + q.z1L
	q.z1L = q.b1*xL - q.a1*yL + q.z2L
	q.z2L = q.b2*xL - q.a2*yL
	yR = q.b0*xR + q.z1R
	q.z1R = q.b1*xR - q.a1*yR + q.z2R
	q.z2R = q.b2*xR - q.a2*yR
	return yL, yR
}

// reset clears the filter's internal state. Called when a new song
// loads, when the filter is reconfigured, or when the user toggles
// effects on a track to avoid clicks from stale sample state.
func (q *biquad) reset() {
	q.z1L, q.z2L, q.z1R, q.z2R = 0, 0, 0, 0
}

// setLowShelf configures this biquad as a low-shelf filter at freq Hz
// with gainDB at the boost/cut. Q is fixed at the standard 0.707
// (Butterworth shelf) — the only knob users want is gain, and varying
// Q across the cookbook formulas just changes the shelf-edge ringing.
func (q *biquad) setLowShelf(sampleRate, freq, gainDB float64) {
	A := math.Pow(10, gainDB/40)
	w0 := 2 * math.Pi * freq / sampleRate
	cosw0 := math.Cos(w0)
	sinw0 := math.Sin(w0)
	S := 1.0 // shelf slope
	alpha := sinw0 / 2 * math.Sqrt((A+1/A)*(1/S-1)+2)
	twoSqrtA := 2 * math.Sqrt(A) * alpha

	b0 := A * ((A + 1) - (A-1)*cosw0 + twoSqrtA)
	b1 := 2 * A * ((A - 1) - (A+1)*cosw0)
	b2 := A * ((A + 1) - (A-1)*cosw0 - twoSqrtA)
	a0 := (A + 1) + (A-1)*cosw0 + twoSqrtA
	a1 := -2 * ((A - 1) + (A+1)*cosw0)
	a2 := (A + 1) + (A-1)*cosw0 - twoSqrtA

	q.b0 = float32(b0 / a0)
	q.b1 = float32(b1 / a0)
	q.b2 = float32(b2 / a0)
	q.a1 = float32(a1 / a0)
	q.a2 = float32(a2 / a0)
}

// setHighShelf is the high-frequency cousin of setLowShelf.
func (q *biquad) setHighShelf(sampleRate, freq, gainDB float64) {
	A := math.Pow(10, gainDB/40)
	w0 := 2 * math.Pi * freq / sampleRate
	cosw0 := math.Cos(w0)
	sinw0 := math.Sin(w0)
	S := 1.0
	alpha := sinw0 / 2 * math.Sqrt((A+1/A)*(1/S-1)+2)
	twoSqrtA := 2 * math.Sqrt(A) * alpha

	b0 := A * ((A + 1) + (A-1)*cosw0 + twoSqrtA)
	b1 := -2 * A * ((A - 1) + (A+1)*cosw0)
	b2 := A * ((A + 1) + (A-1)*cosw0 - twoSqrtA)
	a0 := (A + 1) - (A-1)*cosw0 + twoSqrtA
	a1 := 2 * ((A - 1) - (A+1)*cosw0)
	a2 := (A + 1) - (A-1)*cosw0 - twoSqrtA

	q.b0 = float32(b0 / a0)
	q.b1 = float32(b1 / a0)
	q.b2 = float32(b2 / a0)
	q.a1 = float32(a1 / a0)
	q.a2 = float32(a2 / a0)
}

// setPeak configures the filter as a parametric peak (constant Q) at
// freq Hz with the given gainDB and Q (bandwidth). Q ≈ 0.707 is the
// classic mid-band; higher Q = narrower bell.
func (q *biquad) setPeak(sampleRate, freq, gainDB, qFactor float64) {
	A := math.Pow(10, gainDB/40)
	w0 := 2 * math.Pi * freq / sampleRate
	cosw0 := math.Cos(w0)
	sinw0 := math.Sin(w0)
	alpha := sinw0 / (2 * qFactor)

	b0 := 1 + alpha*A
	b1 := -2 * cosw0
	b2 := 1 - alpha*A
	a0 := 1 + alpha/A
	a1 := -2 * cosw0
	a2 := 1 - alpha/A

	q.b0 = float32(b0 / a0)
	q.b1 = float32(b1 / a0)
	q.b2 = float32(b2 / a0)
	q.a1 = float32(a1 / a0)
	q.a2 = float32(a2 / a0)
}

// threeBandEQ cascades a low-shelf, parametric peak (mid), and high-
// shelf in series. Each band's gain is in dB; gain 0 dB on a band
// means the biquad is configured but stays flat. The mixer skips
// processing entirely when EQConfig.IsActive() reports false.
type threeBandEQ struct {
	low, mid, high biquad
}

// configure rebuilds all three biquad coefficients from a config and
// sample rate. Resets state so abrupt parameter changes don't leak
// stale samples through the filter.
func (e *threeBandEQ) configure(cfg EQParams, sampleRate float64) {
	e.low.setLowShelf(sampleRate, float64(cfg.LowFreq), float64(cfg.LowGain))
	e.mid.setPeak(sampleRate, float64(cfg.MidFreq), float64(cfg.MidGain), float64(cfg.MidQ))
	e.high.setHighShelf(sampleRate, float64(cfg.HighFreq), float64(cfg.HighGain))
	e.low.reset()
	e.mid.reset()
	e.high.reset()
}

// process runs one stereo buffer pair through the cascade in place.
func (e *threeBandEQ) process(bufL, bufR []float32) {
	n := len(bufL)
	if n > len(bufR) {
		n = len(bufR)
	}
	for i := 0; i < n; i++ {
		l, r := e.low.processStereo(bufL[i], bufR[i])
		l, r = e.mid.processStereo(l, r)
		l, r = e.high.processStereo(l, r)
		bufL[i] = l
		bufR[i] = r
	}
}

