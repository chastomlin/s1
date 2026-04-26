package mixer

import (
	"testing"
	"time"
)

func TestAudioTimeSourceFallsBackBeforeDeviceOpens(t *testing.T) {
	// New mixer with no Start — audioFrames is 0, deviceRate may be the
	// default. Now() must not return the captured origin (which would mean
	// "no time has passed") — it should fall back to wall time so the
	// engine's tick still advances while waiting for the device.
	m := New(nil, nil)
	ts := NewAudioTimeSource(m)
	a := ts.Now()
	time.Sleep(2 * time.Millisecond)
	b := ts.Now()
	if delta := b.Sub(a); delta < time.Millisecond {
		t.Fatalf("expected wall-clock fallback to advance, got delta=%v", delta)
	}
}

func TestAudioTimeSourceConvertsFramesToDuration(t *testing.T) {
	m := New(nil, nil)
	m.deviceRate = 48000
	ts := NewAudioTimeSource(m)
	origin := ts.origin

	// Simulate the audio callback bumping the counter by one second of
	// frames. Now should return origin + 1s, regardless of how much wall
	// time has actually elapsed.
	m.audioFrames.Add(48000)
	now := ts.Now()
	want := origin.Add(time.Second)
	if delta := now.Sub(want); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("Now()-want = %v; expected ~0", delta)
	}

	// Another second should advance another second exactly.
	m.audioFrames.Add(48000)
	now2 := ts.Now()
	if d := now2.Sub(now); d < 999*time.Millisecond || d > 1001*time.Millisecond {
		t.Fatalf("second-step delta = %v; expected ~1s", d)
	}
}

func TestAudioTimeSourceMonotonic(t *testing.T) {
	m := New(nil, nil)
	m.deviceRate = 48000
	ts := NewAudioTimeSource(m)

	last := ts.Now()
	for i := 0; i < 100; i++ {
		m.audioFrames.Add(256) // typical buffer size
		now := ts.Now()
		if !now.After(last) && !now.Equal(last) {
			t.Fatalf("step %d: Now went backward (%v -> %v)", i, last, now)
		}
		last = now
	}
}
