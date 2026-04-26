package engine

import "time"

// TimeSource is the abstract real-time clock the scheduler reads to
// convert wall duration into musical ticks. The engine calls Now() at
// ~1 kHz inside e.tick() while playing and captures it at play / seek /
// setTempo to compute musical-time deltas.
//
// The default is the wall clock (good for tests, MIDI-only mode, and
// anywhere a real audio device isn't available). When the audio mixer
// is running, seqoned swaps in mixer.NewAudioTimeSource so the engine
// is locked to the device's sample clock — that eliminates GC and OS
// scheduler drift, which would otherwise show up as audible jitter on
// long-running playback. A TimeSource implementation must be safe to
// call from a single goroutine (the engine's tick) and must return a
// strictly-monotonic time.Time.
type TimeSource interface {
	Now() time.Time
}

// wallClockSource is the default TimeSource used when none is injected.
// Equivalent in every way to bare time.Now(), so existing tests and the
// -no-audio code path keep working unchanged.
type wallClockSource struct{}

func (wallClockSource) Now() time.Time { return time.Now() }
