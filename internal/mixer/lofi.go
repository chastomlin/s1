package mixer

// lofiCrush is the Amiga A500-flavoured lo-fi processor: a sample-and-
// hold downsampler followed by a per-sample bit-depth quantiser.
//
// Sample-and-hold replaces every output sample with the most recently
// captured one, capturing a fresh value only once `phase` ticks past
// 1.0. With step = targetRate/deviceRate, step = 1 captures every
// frame (no rate reduction) and step = 0.5 captures every other frame
// (effective rate halved). No anti-aliasing on either side — the
// foldback is the character.
//
// Bit-depth quantisation maps each output to one of 2^bits steps in
// signed range, then back to float. levels is half-range because
// audio is signed: 8-bit "Paula" has 256 values centred on zero, so
// the quantiser operates with levels = 128.
type lofiCrush struct {
	step       float32 // fraction of a device-rate frame per output frame
	levels     float32 // 2^(bits-1); 0 = no quantisation
	deviceRate float32

	phase        float32 // accumulator in [0, 1)
	heldL, heldR float32 // sample-and-hold state, per channel
}

// configure rebuilds the step + level coefficients from a config.
// Rate is clamped to (0, deviceRate]; Bits to 1..16. Bits == 16 is a
// no-op for the quantiser stage (levels = 0 sentinel).
func (c *lofiCrush) configure(p LofiParams, deviceRate float64) {
	c.deviceRate = float32(deviceRate)
	rate := p.Rate
	if rate <= 0 || rate > c.deviceRate {
		rate = c.deviceRate
	}
	if rate <= 0 {
		c.step = 1
	} else {
		c.step = rate / c.deviceRate
	}
	bits := p.Bits
	if bits >= 16 || bits <= 0 {
		c.levels = 0 // skip the quantiser
	} else {
		c.levels = float32(int(1) << uint(bits-1))
	}
	// Force a fresh capture on the first sample so a freshly-configured
	// node doesn't hold a stale value from before.
	c.phase = 1
	c.heldL = 0
	c.heldR = 0
}

// process runs the bus's L/R buffers through the crusher in place.
// Pure float ops, no allocation, no branching except the once-per-
// hold capture.
func (c *lofiCrush) process(bufL, bufR []float32) {
	step := c.step
	levels := c.levels
	phase := c.phase
	heldL := c.heldL
	heldR := c.heldR
	for i := range bufL {
		phase += step
		if phase >= 1 {
			phase -= 1
			heldL = bufL[i]
			heldR = bufR[i]
		}
		l, r := heldL, heldR
		if levels > 0 {
			l = float32(int32(l*levels)) / levels
			r = float32(int32(r*levels)) / levels
		}
		bufL[i] = l
		bufR[i] = r
	}
	c.phase = phase
	c.heldL = heldL
	c.heldR = heldR
}
