package mixer

import (
	"math"
	"testing"
)

// rmsAmplitude returns the root-mean-square amplitude of a buffer.
// Used by the EQ tests to detect "the low band got quieter" without
// caring about per-sample timing.
func rmsAmplitude(buf []float32) float32 {
	var sum float64
	for _, x := range buf {
		sum += float64(x) * float64(x)
	}
	if len(buf) == 0 {
		return 0
	}
	return float32(math.Sqrt(sum / float64(len(buf))))
}

// genSine fills buf with `frames` samples of a sine at freq Hz at the
// given peak amplitude. Stereo identical (mono content).
func genSine(bufL, bufR []float32, freq, sampleRate float64, amp float32) {
	for i := range bufL {
		v := float32(amp) * float32(math.Sin(2*math.Pi*freq*float64(i)/sampleRate))
		bufL[i] = v
		bufR[i] = v
	}
}

// TestEQ_LowShelfCut runs a 100 Hz sine through an EQ that cuts the
// low shelf by 12 dB and checks the RMS drops by roughly that amount
// (≈ 0.25× linear). Tolerance is loose because EQ shape isn't a brick
// wall — a 200 Hz shelf still leaves residual at 100 Hz.
func TestEQ_LowShelfCut(t *testing.T) {
	const fs = 44100
	const frames = 8192
	bufL := make([]float32, frames)
	bufR := make([]float32, frames)
	genSine(bufL, bufR, 100, fs, 0.5)
	before := rmsAmplitude(bufL)

	var eq threeBandEQ
	eq.configure(EQParams{
		LowFreq: 200, LowGain: -12,
		MidFreq: 1000, MidQ: 1.0, MidGain: 0,
		HighFreq: 5000, HighGain: 0,
	}, fs)
	eq.process(bufL, bufR)
	after := rmsAmplitude(bufL)

	if after >= before*0.6 {
		t.Errorf("low shelf -12 dB cut barely affected 100 Hz signal: before=%f after=%f ratio=%f", before, after, after/before)
	}
}

// TestEQ_FlatPasses a sine through a flat EQ (all gains 0 dB) and
// verifies the output amplitude is essentially unchanged. Tolerates
// a tiny biquad transient at the start by skipping the first 256
// samples in the comparison.
func TestEQ_FlatPasses(t *testing.T) {
	const fs = 44100
	const frames = 8192
	bufL := make([]float32, frames)
	bufR := make([]float32, frames)
	genSine(bufL, bufR, 1000, fs, 0.5)
	before := rmsAmplitude(bufL[256:])

	var eq threeBandEQ
	eq.configure(EQParams{
		LowFreq: 200, LowGain: 0,
		MidFreq: 1000, MidQ: 1.0, MidGain: 0,
		HighFreq: 5000, HighGain: 0,
	}, fs)
	eq.process(bufL, bufR)
	after := rmsAmplitude(bufL[256:])

	ratio := after / before
	if ratio < 0.95 || ratio > 1.05 {
		t.Errorf("flat EQ should pass signal within ±5%%: ratio=%f", ratio)
	}
}

// TestComp_BelowThresholdPassesThrough verifies that a signal whose
// peak stays below threshold is unaltered (within the makeup-gain
// factor, here 1.0).
func TestComp_BelowThresholdPassesThrough(t *testing.T) {
	const fs = 44100
	const frames = 4096
	bufL := make([]float32, frames)
	bufR := make([]float32, frames)
	genSine(bufL, bufR, 1000, fs, 0.1) // low-amplitude
	before := rmsAmplitude(bufL)

	var c compressor
	c.configure(CompParams{
		ThresholdDB: -6, // ~0.5 linear
		Ratio:       4.0,
		AttackMs:    1,
		ReleaseMs:   50,
		MakeupDB:    0,
	}, fs)
	c.process(bufL, bufR)
	after := rmsAmplitude(bufL)

	ratio := after / before
	if ratio < 0.95 || ratio > 1.05 {
		t.Errorf("compressor should pass below-threshold signal unchanged: ratio=%f", ratio)
	}
}

// TestComp_AboveThresholdReducesGain pushes a signal past threshold
// and verifies the compressor pulls it down.
func TestComp_AboveThresholdReducesGain(t *testing.T) {
	const fs = 44100
	const frames = 8192
	bufL := make([]float32, frames)
	bufR := make([]float32, frames)
	genSine(bufL, bufR, 1000, fs, 0.9) // way above -6 dB threshold
	before := rmsAmplitude(bufL)

	var c compressor
	c.configure(CompParams{
		ThresholdDB: -12, // ~0.25 linear
		Ratio:       4.0,
		AttackMs:    1,
		ReleaseMs:   50,
		MakeupDB:    0,
	}, fs)
	c.process(bufL, bufR)
	// Skip the first window — attack hasn't settled yet.
	after := rmsAmplitude(bufL[2048:])

	if after >= before {
		t.Errorf("compressor failed to reduce gain on above-threshold signal: before=%f after=%f", before, after)
	}
}

// TestComp_AttackIsSmooth verifies that the gain reduction doesn't
// snap to the steady state instantly — there's at least some attack
// envelope. We do this by comparing the first slice (still attacking)
// to the last slice (steady state) and demanding the first is louder.
func TestComp_AttackIsSmooth(t *testing.T) {
	const fs = 44100
	const frames = 8192
	bufL := make([]float32, frames)
	bufR := make([]float32, frames)
	genSine(bufL, bufR, 1000, fs, 0.9)

	var c compressor
	c.configure(CompParams{
		ThresholdDB: -12,
		Ratio:       6.0,
		AttackMs:    20, // long enough to be measurable in this buffer
		ReleaseMs:   100,
		MakeupDB:    0,
	}, fs)
	c.process(bufL, bufR)

	earlyRMS := rmsAmplitude(bufL[:512])
	lateRMS := rmsAmplitude(bufL[6000:])
	if earlyRMS <= lateRMS {
		t.Errorf("attack envelope should leave the early signal louder than steady-state: early=%f late=%f", earlyRMS, lateRMS)
	}
}
