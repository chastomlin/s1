package mixer

import (
	"io"
	"log"
	"math"
	"testing"

	"seqone/internal/protocol"
)

// TestTriggerPitched_ScalesVoiceStep verifies that an octave-up pitch
// ratio doubles the voice's step rate (samples play back twice as fast).
// Without this, transposition on sample tracks would be silent.
func TestTriggerPitched_ScalesVoiceStep(t *testing.T) {
	m := NewForTest(map[string]*Sample{
		"k": testSample("k", 4096, 0.5),
	})
	m.deviceRate = 44100 // native rate == sample rate → base step 1.0
	m.TriggerPitched("k", 100, 2.0)
	// Drain the trigger into a voice via one zero-frame callback.
	runCallback(m, 0)

	var active *voice
	for i := range m.voices {
		if m.voices[i].active {
			active = &m.voices[i]
			break
		}
	}
	if active == nil {
		t.Fatal("no voice activated after TriggerPitched")
	}
	if math.Abs(active.step-2.0) > 1e-6 {
		t.Errorf("voice step = %f, want 2.0 (one octave up)", active.step)
	}
}

// TestTriggerPitched_ClampsInsaneRatios protects against NaN / Inf /
// zero ratios from poisoning the voice pool. Clamped values still play
// something audible instead of the voice getting stuck forever.
func TestTriggerPitched_ClampsInsaneRatios(t *testing.T) {
	m := NewForTest(map[string]*Sample{
		"k": testSample("k", 64, 0.5),
	})
	for _, ratio := range []float64{math.NaN(), math.Inf(1), 0, -1.0, 1e9} {
		m.TriggerPitched("k", 100, ratio)
	}
	runCallback(m, 0)
	// All five triggers should have produced some voice without crashing.
	// With voice stealing, we expect at least 1 active voice.
	if m.ActiveVoices() < 1 {
		t.Errorf("no voices activated from clamped-ratio triggers")
	}
}

// TestBridge_PitchedSampleTriggerTransposesViaBaseNote walks the full
// bus-to-mixer path: a SampleTrigger event with Note=72 and BaseNote=60
// should end up as a voice playing one octave up (step ratio ×2 vs
// native).
func TestBridge_PitchedSampleTriggerTransposesViaBaseNote(t *testing.T) {
	m := NewForTest(map[string]*Sample{
		"k_smp": testSample("k", 4096, 0.5),
	})
	m.deviceRate = 44100
	resolve := func(id string) TrackRoute {
		if id == "k" {
			return TrackRoute{SampleID: "k_smp", BaseNote: 60}
		}
		return TrackRoute{}
	}
	br := NewBridge(m, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.SampleTrigger,
		Track:    "k",
		Note:     72, // C5, one octave above base C4
		Vel:      100,
	}
	close(ch)
	br.Run(ch)

	runCallback(m, 0)
	var found *voice
	for i := range m.voices {
		if m.voices[i].active {
			found = &m.voices[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no voice triggered by bridge")
	}
	if math.Abs(found.step-2.0) > 1e-6 {
		t.Errorf("step = %f, want 2.0 (one octave up from C4→C5)", found.step)
	}
}

// TestTriggerVoice_ChokeReplacesSameKey pins the per-track monophony
// rule: a second TriggerVoice with the same chokeKey silences the
// first voice rather than stacking — fixes loop-wrap phasing where a
// sample's tail would otherwise overlap with the next iteration's onset.
func TestTriggerVoice_ChokeReplacesSameKey(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 4096, 0.5)})
	m.deviceRate = 44100

	m.TriggerVoice("k", 100, 1.0, "track-k")
	runCallback(m, 0)
	if got := m.ActiveVoices(); got != 1 {
		t.Fatalf("after 1st trigger: voices = %d, want 1", got)
	}

	m.TriggerVoice("k", 100, 1.0, "track-k")
	runCallback(m, 0)
	// Second trigger same choke → previous voice silenced, new one
	// replaces it. Total active: 1.
	if got := m.ActiveVoices(); got != 1 {
		t.Errorf("after 2nd same-choke trigger: voices = %d, want 1 (previous voice should have been silenced)", got)
	}
}

// TestTriggerVoice_DifferentKeysCoexist sanity-checks that the choke is
// scoped: two triggers with distinct chokeKeys both keep playing, so
// kick + snare on different tracks don't cut each other.
func TestTriggerVoice_DifferentKeysCoexist(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 4096, 0.5)})
	m.deviceRate = 44100

	m.TriggerVoice("k", 100, 1.0, "track-k")
	m.TriggerVoice("k", 100, 1.0, "track-s")
	runCallback(m, 0)
	if got := m.ActiveVoices(); got != 2 {
		t.Errorf("different choke keys: voices = %d, want 2 (no cross-choke)", got)
	}
}

// TestTriggerVoice_EmptyChokeKeyIsPolyphonic — the legacy code path
// (Trigger / TriggerPitched without a key) should keep stacking voices
// like before, so existing callers and tests stay working.
func TestTriggerVoice_EmptyChokeKeyIsPolyphonic(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 4096, 0.5)})
	m.deviceRate = 44100

	m.Trigger("k", 100)
	m.Trigger("k", 100)
	runCallback(m, 0)
	if got := m.ActiveVoices(); got != 2 {
		t.Errorf("polyphonic Trigger: voices = %d, want 2", got)
	}
}

// TestBridge_HoldsReleaseDoesNotSilenceSamples pins the loop-wrap
// behaviour: NoteHoldsRelease (the engine's soft cleanup on loop wrap)
// is for releasing held MIDI notes only — the audio mixer must leave
// its sample voices alone so a drum tail decays naturally past the
// wrap rather than getting hard-clipped.
func TestBridge_HoldsReleaseDoesNotSilenceSamples(t *testing.T) {
	m := NewForTest(map[string]*Sample{
		"k_smp": testSample("k", 4096, 0.5),
	})
	m.deviceRate = 44100
	resolve := func(string) TrackRoute {
		return TrackRoute{SampleID: "k_smp", BaseNote: 60}
	}
	br := NewBridge(m, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 2)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteHoldsRelease}
	close(ch)
	br.Run(ch)

	runCallback(m, 0)
	if got := m.ActiveVoices(); got != 1 {
		t.Errorf("after HoldsRelease: voices = %d, want 1 (sample tail must keep playing)", got)
	}
}

// TestBridge_BareSampleTriggerIsNativePitch verifies that an event with
// Note=0 plays at native pitch (the mixer skips the transposition math).
func TestBridge_BareSampleTriggerIsNativePitch(t *testing.T) {
	m := NewForTest(map[string]*Sample{
		"k_smp": testSample("k", 4096, 0.5),
	})
	m.deviceRate = 44100
	resolve := func(string) TrackRoute {
		return TrackRoute{SampleID: "k_smp", BaseNote: 60}
	}
	br := NewBridge(m, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	close(ch)
	br.Run(ch)

	runCallback(m, 0)
	for i := range m.voices {
		if m.voices[i].active {
			if math.Abs(m.voices[i].step-1.0) > 1e-6 {
				t.Errorf("step = %f, want 1.0 (native pitch for bare trigger)", m.voices[i].step)
			}
			return
		}
	}
	t.Fatal("no voice active")
}
