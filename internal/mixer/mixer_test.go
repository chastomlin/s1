package mixer

import (
	"encoding/binary"
	"io"
	"log"
	"math"
	"testing"

	"seqone/internal/protocol"
)

// testSample builds a synthetic Sample of the given frame count at 44.1k
// mono — used by voice-pool tests that don't need a real WAV on disk.
func testSample(id string, frames int, constant float32) *Sample {
	data := make([]float32, frames)
	for i := range data {
		data[i] = constant
	}
	return &Sample{
		ID: id, Data: data, NumChans: 1, SampleRate: 44100,
	}
}

// runCallback invokes the audio callback the way miniaudio would: with a
// byte buffer sized for `frames` stereo float32 frames. Returns the
// decoded interleaved [L0, R0, L1, R1, ...] so tests can inspect output.
func runCallback(m *Mixer, frames int) []float32 {
	buf := make([]byte, frames*2*4)
	m.onData(buf, nil, uint32(frames))
	out := make([]float32, frames*2)
	for i := range out {
		bits := binary.LittleEndian.Uint32(buf[i*4 : (i+1)*4])
		out[i] = math.Float32frombits(bits)
	}
	return out
}

func TestTrigger_ActivatesVoice(t *testing.T) {
	bank := map[string]*Sample{"k": testSample("k", 32, 0.5)}
	m := NewForTest(bank)
	m.Trigger("k", 100)
	runCallback(m, 8)
	if n := m.ActiveVoices(); n != 1 {
		t.Errorf("after trigger: active voices = %d, want 1", n)
	}
}

func TestTrigger_UnknownSampleIsDropped(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 32, 0.5)})
	m.Trigger("ghost", 100)
	runCallback(m, 8)
	if n := m.ActiveVoices(); n != 0 {
		t.Errorf("unknown trigger activated %d voices", n)
	}
}

func TestVoiceFinishes_MarksInactive(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 8, 0.5)})
	m.Trigger("k", 100)
	// First callback: 8 frames at step 1.0 → voice plays entire buffer.
	runCallback(m, 8)
	// One more frame fires the end-of-sample path.
	runCallback(m, 2)
	if n := m.ActiveVoices(); n != 0 {
		t.Errorf("voice should be inactive after playback: active=%d", n)
	}
}

func TestAllOff_SilencesVoices(t *testing.T) {
	bank := map[string]*Sample{"k": testSample("k", 1024, 0.5)}
	m := NewForTest(bank)
	for i := 0; i < 5; i++ {
		m.Trigger("k", 100)
	}
	runCallback(m, 4)
	if m.ActiveVoices() == 0 {
		t.Fatal("voices should be active before AllOff")
	}
	m.AllOff()
	runCallback(m, 4)
	if n := m.ActiveVoices(); n != 0 {
		t.Errorf("AllOff left %d voices active", n)
	}
}

func TestVoiceStealing_WhenPoolFull(t *testing.T) {
	bank := map[string]*Sample{"k": testSample("k", 1024, 0.5)}
	m := NewForTest(bank)
	for i := 0; i < MaxVoices+4; i++ {
		m.Trigger("k", 100)
	}
	runCallback(m, 4)
	if n := m.ActiveVoices(); n > MaxVoices {
		t.Errorf("exceeded voice cap: active=%d max=%d", n, MaxVoices)
	}
}

func TestCallback_ProducesOutputForActiveVoice(t *testing.T) {
	// Non-zero sample content should produce non-zero output frames.
	m := NewForTest(map[string]*Sample{"k": testSample("k", 64, 0.5)})
	m.Trigger("k", 127)
	out := runCallback(m, 16)
	var peak float32
	for _, v := range out {
		a := float32(math.Abs(float64(v)))
		if a > peak {
			peak = a
		}
	}
	if peak <= 0 {
		t.Errorf("expected non-zero output while voice plays; peak=%f", peak)
	}
}

func TestCallback_SilentWithNoVoices(t *testing.T) {
	m := NewForTest(map[string]*Sample{})
	out := runCallback(m, 8)
	for _, v := range out {
		if v != 0 {
			t.Errorf("expected silence, got %f", v)
			break
		}
	}
}

func TestSetSamples_Swaps(t *testing.T) {
	m := NewForTest(map[string]*Sample{"a": testSample("a", 16, 0.5)})
	m.SetSamples(map[string]*Sample{"b": testSample("b", 16, 0.5)})
	// Old sample is gone — trigger should no-op.
	m.Trigger("a", 100)
	runCallback(m, 4)
	if n := m.ActiveVoices(); n != 0 {
		t.Errorf("trigger for swapped-out sample spawned voice: %d", n)
	}
	// New sample is present.
	m.Trigger("b", 100)
	runCallback(m, 4)
	if n := m.ActiveVoices(); n != 1 {
		t.Errorf("trigger for new sample: active=%d want 1", n)
	}
}

// --- Bridge tests -----------------------------------------------------------

func TestBridge_RoutesSampleTriggersOnly(t *testing.T) {
	m := NewForTest(map[string]*Sample{"kick": testSample("kick", 16, 0.5)})
	// Resolver maps pattern track "k" to sample id "kick".
	resolve := func(id string) TrackRoute {
		if id == "k" {
			return TrackRoute{SampleID: "kick", BaseNote: 60}
		}
		return TrackRoute{}
	}
	br := NewBridge(m, resolve, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 4)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteOn, Track: "bass", Note: 60, Vel: 100} // ignored
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "unmapped", Vel: 100}
	ch <- protocol.Event{Event: protocol.EvPosition, Bar: 1} // ignored
	close(ch)
	br.Run(ch)

	runCallback(m, 4)
	if n := m.ActiveVoices(); n != 1 {
		t.Errorf("expected 1 voice from one valid SampleTrigger, got %d", n)
	}
}

func TestBridge_AllOffSilences(t *testing.T) {
	m := NewForTest(map[string]*Sample{"k": testSample("k", 1024, 0.5)})
	br := NewBridge(m, func(string) TrackRoute { return TrackRoute{SampleID: "k", BaseNote: 60} }, log.New(io.Discard, "", 0))

	ch := make(chan protocol.Event, 2)
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.SampleTrigger, Track: "k", Vel: 100}
	ch <- protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteAllOff}
	close(ch)
	br.Run(ch)

	runCallback(m, 4)
	if n := m.ActiveVoices(); n != 0 {
		t.Errorf("AllOff event failed to silence: active=%d", n)
	}
}
