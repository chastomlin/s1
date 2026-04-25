package mixer

import (
	"math"
	"testing"
)

// TestPanLaw_ConstantPower pins the pan-law shape: full-left silences
// R, full-right silences L, and centre pans give ~0.707 on each side
// so a centred mono signal has the same energy as before introducing
// pan.
func TestPanLaw_ConstantPower(t *testing.T) {
	cases := []struct {
		pan      float32
		wantL    float32
		wantR    float32
		toleranc float32
	}{
		{-1, 1.0, 0.0, 1e-5},
		{1, 0.0, 1.0, 1e-5},
		{0, 0.7071, 0.7071, 1e-3},
	}
	for _, c := range cases {
		l, r := panLaw(c.pan)
		if absF32(l-c.wantL) > c.toleranc || absF32(r-c.wantR) > c.toleranc {
			t.Errorf("pan=%v: got L=%f R=%f, want L=%f R=%f", c.pan, l, r, c.wantL, c.wantR)
		}
	}
}

// TestPanLaw_ClampsOutOfRange clips wild pan values so a typo or bug
// doesn't blow up the mixer.
func TestPanLaw_ClampsOutOfRange(t *testing.T) {
	for _, pan := range []float32{-2, 2, float32(math.NaN())} {
		l, r := panLaw(pan)
		if math.IsNaN(float64(l)) || math.IsNaN(float64(r)) || l < 0 || r < 0 || l > 1 || r > 1 {
			t.Errorf("pan=%v: got L=%f R=%f, want clamped values in [0,1]", pan, l, r)
		}
	}
}

// TestVoice_HardLeftSilencesRight verifies the mixing-side wiring:
// playing a constant-amplitude sample at pan=-1 produces output on
// the L bus only.
func TestVoice_HardLeftSilencesRight(t *testing.T) {
	s := testSample("k", 64, 0.5)
	m := NewForTest(map[string]*Sample{"k": s})
	m.deviceRate = 44100
	m.Play(VoiceTrigger{SampleID: "k", Vel: 100, Pan: -1, TrackGain: 1})

	// Render one buffer's worth of frames and inspect output bytes.
	const frames = 16
	buf := make([]byte, frames*8) // stereo float32
	m.onData(buf, nil, frames)

	// Read the first frame's L/R as float32 little-endian.
	l := readF32LE(buf, 0)
	r := readF32LE(buf, 4)
	if l == 0 {
		t.Errorf("hard-left voice produced silence on L: l=%f", l)
	}
	if absF32(r) > 1e-6 {
		t.Errorf("hard-left voice leaked into R: r=%f", r)
	}
}

// TestVoice_TrackGainScalesAmplitude — gain 2.0 doubles the output
// amplitude vs gain 1.0 (within rounding).
func TestVoice_TrackGainScalesAmplitude(t *testing.T) {
	render := func(trackGain float32) float32 {
		s := testSample("k", 64, 0.5)
		m := NewForTest(map[string]*Sample{"k": s})
		m.deviceRate = 44100
		m.Play(VoiceTrigger{SampleID: "k", Vel: 100, TrackGain: trackGain})
		const frames = 16
		buf := make([]byte, frames*8)
		m.onData(buf, nil, frames)
		return readF32LE(buf, 0) // L sample at frame 0
	}
	unity := render(1.0)
	doubled := render(2.0)
	ratio := doubled / unity
	if absF32(ratio-2.0) > 0.05 {
		t.Errorf("doubling gain didn't double amplitude: unity=%f doubled=%f ratio=%f", unity, doubled, ratio)
	}
}

func absF32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func readF32LE(buf []byte, off int) float32 {
	if off+4 > len(buf) {
		return 0
	}
	bits := uint32(buf[off]) | uint32(buf[off+1])<<8 | uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	return math.Float32frombits(bits)
}
