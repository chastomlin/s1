package mixer

import "math"

// DriveMode picks the saturation curve. Public so the mixer config
// can pass a typed value rather than a string; callers map the
// song-side enum at configure time.
type DriveMode int

const (
	DriveModeSoft DriveMode = iota
	DriveModeHard
	DriveModeFold
)

// driveStage is the overdrive / distortion node. Three stages in
// sequence:
//
//  1. Pre-saturator gain (drive knob, mapped 1x..20x)
//  2. Saturator curve — tanh, hard clip, or sine wavefolder.
//  3. One-pole low-pass tone control (tone knob, mapped 200 Hz..20 kHz
//     log-scaled) to tame the harmonics the saturator just generated.
//  4. Linear output level (level knob, 0..2).
//
// Soft mode auto-normalises by 1/tanh(drive) so cranking drive adds
// harmonics rather than amplitude — useful for live tweaking without
// blowing speakers. Hard and fold are naturally bounded.
type driveStage struct {
	mode      DriveMode
	gain      float32 // pre-saturator linear gain
	softNorm  float32 // 1/tanh(gain), only used in soft mode
	toneAlpha float32 // one-pole LP coefficient
	level     float32

	zL, zR float32 // tone-LP state
}

// configure recomputes coefficients from a config + sample rate.
// Drive 0..1 → gain 1x..20x. Tone 0..1 → cutoff 200..20000 Hz on a
// log curve (octave-spaced steps feel right under the finger).
func (d *driveStage) configure(p DriveParams, sampleRate float64) {
	dr := clamp01(p.Drive)
	tn := clamp01(p.Tone)
	lvl := p.Level
	if lvl < 0 {
		lvl = 0
	} else if lvl > 2 {
		lvl = 2
	}

	d.gain = float32(1.0 + 19.0*dr) // 1x..20x
	if d.gain < 1 {
		d.gain = 1
	}
	if p.Mode == DriveModeSoft {
		// Auto-normalise soft mode so output stays around ±1.
		t := math.Tanh(float64(d.gain))
		if t < 0.001 {
			t = 0.001
		}
		d.softNorm = float32(1.0 / t)
	} else {
		d.softNorm = 1
	}

	// Tone: log-scaled cutoff between 200 Hz and 20 kHz.
	cutoff := 200.0 * math.Pow(100.0, float64(tn)) // 200..20000
	if cutoff > sampleRate*0.45 {
		cutoff = sampleRate * 0.45
	}
	// One-pole LP coefficient. Higher cutoff → alpha closer to 1
	// (tracks the input directly); lower cutoff → smaller alpha
	// (heavily smoothed).
	d.toneAlpha = float32(1.0 - math.Exp(-2*math.Pi*cutoff/sampleRate))

	d.mode = p.Mode
	d.level = lvl
	// Don't reset state — config tweaks during playback shouldn't pop.
}

// process runs the bus's L/R buffers through the chain in place.
func (d *driveStage) process(bufL, bufR []float32) {
	gain := d.gain
	norm := d.softNorm
	alpha := d.toneAlpha
	level := d.level
	zL, zR := d.zL, d.zR
	for i := range bufL {
		l := bufL[i] * gain
		r := bufR[i] * gain
		switch d.mode {
		case DriveModeHard:
			l = hardClip(l)
			r = hardClip(r)
		case DriveModeFold:
			l = sinFold(l)
			r = sinFold(r)
		default: // soft
			l = float32(math.Tanh(float64(l))) * norm
			r = float32(math.Tanh(float64(r))) * norm
		}
		// One-pole LP for tone control. zN += alpha*(in - zN); out = zN.
		zL += alpha * (l - zL)
		zR += alpha * (r - zR)
		bufL[i] = zL * level
		bufR[i] = zR * level
	}
	d.zL, d.zR = zL, zR
}

// hardClip is a symmetric clipper. Anything past ±1 saturates.
// Inline simple branches; the compiler folds them well.
func hardClip(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// sinFold maps any input through sin(x·π/2), naturally folding at ±1
// without runaway state. Cheap enough at 44.1 kHz × 2 channels and
// gives a musical synthy fold for high drive values.
func sinFold(v float32) float32 {
	return float32(math.Sin(float64(v) * (math.Pi / 2)))
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
