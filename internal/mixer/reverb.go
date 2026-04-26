package mixer

// reverbStage is a Freeverb-style reverb (Schroeder-Moorer): 8 lowpassed
// feedback combs in parallel feeding 4 series allpasses, replicated for
// L and R with a small per-channel offset for stereo spread.
//
// Constants are the canonical Freeverb tunings, scaled to the device
// sample rate. The mix knob crossfades dry vs wet linearly. Tail
// energy lives in the comb buffers, so process() keeps producing
// output for ~2 seconds after the input goes silent — exactly the
// behaviour you want from a tail.
type reverbStage struct {
	combsL    [numCombs]reverbComb
	combsR    [numCombs]reverbComb
	allpL     [numAllpasses]reverbAllpass
	allpR     [numAllpasses]reverbAllpass
	mixWet    float32
	mixDry    float32
	allocRate int
}

const (
	numCombs      = 8
	numAllpasses  = 4
	stereoSpread  = 23 // samples added to right channel delay for spread
	allpassFB     = 0.5
	combDampScale = 0.4
	combFBScale   = 0.28
	combFBOffset  = 0.7
	gainCorrect   = 0.015 // input attenuation so wet signal sits at sane level
)

// Tunings are the original Freeverb delay lengths (in samples at 44.1
// kHz). We scale these by deviceRate/44100 so the same parameters
// produce the same character at any sample rate.
var (
	combTunings    = [numCombs]int{1116, 1188, 1277, 1356, 1422, 1491, 1557, 1617}
	allpassTunings = [numAllpasses]int{556, 441, 341, 225}
)

// reverbComb is a Freeverb LBCF (lowpassed comb): a delay line with
// feedback through a one-pole low-pass that progressively rolls off
// the high end of the tail. damp1/damp2 set the lowpass; fb is the
// feedback coefficient (the size of the room).
type reverbComb struct {
	buf   []float32
	idx   int
	last  float32
	fb    float32
	damp1 float32
	damp2 float32
}

func (c *reverbComb) process(in float32) float32 {
	out := c.buf[c.idx]
	c.last = out*c.damp2 + c.last*c.damp1
	c.buf[c.idx] = in + c.last*c.fb
	c.idx++
	if c.idx >= len(c.buf) {
		c.idx = 0
	}
	return out
}

// reverbAllpass is a Schroeder all-pass section. Disperses echoes
// without colouring — the diffusion stage that turns a comb-bank
// "metallic" sum into a smooth tail.
type reverbAllpass struct {
	buf []float32
	idx int
	fb  float32
}

func (a *reverbAllpass) process(in float32) float32 {
	bufout := a.buf[a.idx]
	out := -in + bufout
	a.buf[a.idx] = in + bufout*a.fb
	a.idx++
	if a.idx >= len(a.buf) {
		a.idx = 0
	}
	return out
}

// configure (re)builds the comb/allpass buffers and sets feedback +
// damping coefficients. Buffer allocation only happens when the
// device sample rate has changed since last configure — re-tweaking
// size or damping mid-playback just updates coefficients, preserving
// the tail. Stereo spread is baked into the right-channel delay
// lengths.
func (r *reverbStage) configure(p ReverbParams, sampleRate float64) {
	rate := int(sampleRate + 0.5)
	if rate <= 0 {
		rate = 44100
	}
	if r.allocRate != rate {
		// Rebuild buffers for the new sample rate. Old tail is lost,
		// but this only fires when the audio device opens for the
		// first time or its rate changes — not on every knob tweak.
		scale := float64(rate) / 44100.0
		for i, t := range combTunings {
			lL := int(float64(t)*scale + 0.5)
			lR := lL + stereoSpread
			r.combsL[i].buf = make([]float32, lL)
			r.combsR[i].buf = make([]float32, lR)
			r.combsL[i].idx = 0
			r.combsR[i].idx = 0
			r.combsL[i].last = 0
			r.combsR[i].last = 0
		}
		for i, t := range allpassTunings {
			lL := int(float64(t)*scale + 0.5)
			lR := lL + stereoSpread
			r.allpL[i].buf = make([]float32, lL)
			r.allpR[i].buf = make([]float32, lR)
			r.allpL[i].idx = 0
			r.allpR[i].idx = 0
			r.allpL[i].fb = allpassFB
			r.allpR[i].fb = allpassFB
		}
		r.allocRate = rate
	} else {
		// Same rate — keep allpass feedback in sync but leave comb
		// buffers + state alone so the tail flows continuously
		// across param tweaks.
		for i := range r.allpL {
			r.allpL[i].fb = allpassFB
			r.allpR[i].fb = allpassFB
		}
	}

	size := clamp01(p.Size)
	damp := clamp01(p.Damping)
	mix := clamp01(p.Mix)

	fb := combFBOffset + float32(size)*combFBScale
	d1 := float32(damp) * combDampScale
	d2 := 1 - d1
	for i := range r.combsL {
		r.combsL[i].fb = fb
		r.combsL[i].damp1 = d1
		r.combsL[i].damp2 = d2
		r.combsR[i].fb = fb
		r.combsR[i].damp1 = d1
		r.combsR[i].damp2 = d2
	}

	r.mixWet = mix
	r.mixDry = 1 - mix
}

// process runs the bus's L/R buffers through the reverb network in
// place. Each output sample is mixWet * tail + mixDry * input. Even
// when the input is silent the combs continue to produce the decay
// from previously-buffered energy, which is why the tail sustains
// past the dry signal.
func (r *reverbStage) process(bufL, bufR []float32) {
	mixWet := r.mixWet
	mixDry := r.mixDry
	for i := range bufL {
		dryL := bufL[i]
		dryR := bufR[i]
		// Sum the two channels into a mono input for the comb bank,
		// pre-attenuated so the wet signal lands at unity-ish.
		in := (dryL + dryR) * gainCorrect

		var sumL, sumR float32
		for c := 0; c < numCombs; c++ {
			sumL += r.combsL[c].process(in)
			sumR += r.combsR[c].process(in)
		}
		for a := 0; a < numAllpasses; a++ {
			sumL = r.allpL[a].process(sumL)
			sumR = r.allpR[a].process(sumR)
		}
		bufL[i] = dryL*mixDry + sumL*mixWet
		bufR[i] = dryR*mixDry + sumR*mixWet
	}
}
