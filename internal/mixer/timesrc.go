package mixer

import "time"

// AudioTimeSource is the engine.TimeSource implementation that derives
// real-time progress from the audio device's frame counter rather than
// from time.Now. Once the device is open and firing onData callbacks,
// every Now() call returns origin + (frames / sampleRate) — strictly
// monotonic, locked to the sample clock, and immune to GC pauses or OS
// scheduler drift.
//
// Created once at startup (after Mixer.Start succeeds) and handed to
// engine.SetTimeSource. Safe to call Now from any goroutine; the
// underlying counter is atomic.
type AudioTimeSource struct {
	mixer  *Mixer
	origin time.Time
}

// NewAudioTimeSource captures a wall-clock origin so the returned times
// look like real wall times to callers that compare against time.Time
// instances captured before this source was wired in. The audio device
// doesn't need to be running yet — Now falls back to wall time until
// the first audio callback bumps the frame counter.
func NewAudioTimeSource(m *Mixer) *AudioTimeSource {
	return &AudioTimeSource{mixer: m, origin: time.Now()}
}

// Now returns origin + (audioFramesRendered / deviceRate) as a time.Time.
// Before the device opens (frames == 0 || rate <= 0) it falls back to
// time.Now() so the engine's tick still advances during the brief window
// between New and the first onData.
func (a *AudioTimeSource) Now() time.Time {
	if a == nil || a.mixer == nil {
		return time.Now()
	}
	frames := a.mixer.AudioFramesRendered()
	rate := a.mixer.DeviceRate()
	if frames == 0 || rate <= 0 {
		return time.Now()
	}
	d := time.Duration(frames) * time.Second / time.Duration(rate)
	return a.origin.Add(d)
}
