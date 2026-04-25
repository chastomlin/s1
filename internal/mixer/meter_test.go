package mixer

import (
	"testing"
)

// TestMeter_Attack rises immediately on a louder buffer and never
// exceeds the input. The fast-attack coefficient is large enough that
// a single update lands close to the input within a few buffers.
func TestMeter_Attack(t *testing.T) {
	var m meterChannel
	for i := 0; i < 8; i++ {
		updateMeter(&m, 0.8)
	}
	got := m.read().Level
	if got < 0.7 {
		t.Errorf("level after 8 buffers @ 0.8 = %f, want >= 0.7", got)
	}
	if got > 0.8+1e-3 {
		t.Errorf("level overshoot input: %f > 0.8", got)
	}
}

// TestMeter_Release decays slowly when the input drops to silence —
// matches the VU "slow needle return" feel.
func TestMeter_Release(t *testing.T) {
	var m meterChannel
	for i := 0; i < 16; i++ {
		updateMeter(&m, 0.9)
	}
	beforeRelease := m.read().Level
	for i := 0; i < 4; i++ {
		updateMeter(&m, 0)
	}
	afterRelease := m.read().Level
	if afterRelease >= beforeRelease {
		t.Errorf("level should drop on silence: before=%f after=%f", beforeRelease, afterRelease)
	}
	if afterRelease < beforeRelease*0.5 {
		t.Errorf("release too fast — VU should ease, before=%f after=%f", beforeRelease, afterRelease)
	}
}

// TestMeter_PeakHoldThenDecay verifies the peak indicator rises
// instantly to the loudest buffer, holds at that value for a while
// (no drop on a silent next buffer), then decays exponentially.
func TestMeter_PeakHoldThenDecay(t *testing.T) {
	var m meterChannel
	updateMeter(&m, 0.95)
	if got := m.read().Peak; got < 0.95-1e-6 {
		t.Errorf("peak should track input instantly: got %f, want 0.95", got)
	}
	// One silent buffer — peak should barely move (decay coef ~0.985).
	updateMeter(&m, 0)
	got := m.read().Peak
	if got < 0.92 {
		t.Errorf("peak decayed too fast on first silent buffer: %f", got)
	}
	// After many silent buffers it should reach the floor.
	for i := 0; i < 1000; i++ {
		updateMeter(&m, 0)
	}
	if final := m.read().Peak; final != 0 {
		t.Errorf("peak should reach the floor (0) after long silence, got %f", final)
	}
}

// TestMeterBank_TrackMetersIsolated verifies that updating one track's
// meter doesn't bleed into another's reading.
func TestMeterBank_TrackMetersIsolated(t *testing.T) {
	b := newMeterBank()
	b.SetTracks([]string{"k", "s"})

	for i := 0; i < 8; i++ {
		b.updateTrack("k", 0.8, 0.7)
		b.updateTrack("s", 0, 0)
	}
	kL, kR := b.Track("k")
	sL, sR := b.Track("s")
	if kL.Level == 0 || kR.Level == 0 {
		t.Errorf("kick meter should be non-zero: L=%f R=%f", kL.Level, kR.Level)
	}
	if sL.Level != 0 || sR.Level != 0 {
		t.Errorf("snare meter should be silent: L=%f R=%f", sL.Level, sR.Level)
	}
}

// TestMixer_TriggerLightsUpMaster runs a sample through the mixer's
// onData a few times and confirms the master meter rises above the
// silence floor. End-to-end smoke test for the audio→meter path.
func TestMixer_TriggerLightsUpMaster(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 4096, 0.5)})
	m.deviceRate = 44100
	m.SetTrackMeters([]string{"k"})

	m.TriggerVoice("k", 100, 1.0, "k")
	// Pump several buffers so the meter has time to attack.
	for i := 0; i < 8; i++ {
		runCallback(m, 64)
	}
	mL, mR := m.MasterLevels()
	if mL.Level == 0 && mR.Level == 0 {
		t.Errorf("master meter never rose: L=%+v R=%+v", mL, mR)
	}
	tL, tR := m.TrackLevels("k")
	if tL.Level == 0 && tR.Level == 0 {
		t.Errorf("track meter never rose: L=%+v R=%+v", tL, tR)
	}
}
