package mixer

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeWAV emits a minimum WAV file (RIFF/fmt/data) at the given bit depth,
// channel count and sample rate. numFrames is frames-per-channel. The
// content is a gentle triangle wave so tests can verify decoded values
// roundtrip correctly without being fooled by all-zero edge cases.
func writeWAV(t *testing.T, path string, bitDepth, channels, rate, numFrames int) {
	t.Helper()
	var samples []int32
	peak := int32(1)<<(bitDepth-1) - 1
	for i := 0; i < numFrames; i++ {
		// triangle wave period = 8 frames; value cycles between ±peak/2
		phase := i % 8
		var v int32
		switch {
		case phase < 4:
			v = int32(phase) * peak / 8
		default:
			v = int32(8-phase) * peak / 8
		}
		for c := 0; c < channels; c++ {
			samples = append(samples, v)
		}
	}

	bytesPerSample := bitDepth / 8
	dataSize := len(samples) * bytesPerSample
	fmtSize := 16
	riffSize := 4 + (8 + fmtSize) + (8 + dataSize)

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(riffSize))
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(fmtSize))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*channels*bytesPerSample))
	binary.Write(&buf, binary.LittleEndian, uint16(channels*bytesPerSample))
	binary.Write(&buf, binary.LittleEndian, uint16(bitDepth))

	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	for _, s := range samples {
		switch bitDepth {
		case 16:
			binary.Write(&buf, binary.LittleEndian, int16(s))
		case 24:
			b := []byte{byte(s), byte(s >> 8), byte(s >> 16)}
			buf.Write(b)
		case 32:
			binary.Write(&buf, binary.LittleEndian, s)
		}
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWAV_16BitStereo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.wav")
	writeWAV(t, p, 16, 2, 44100, 32)

	s, err := LoadWAV("s", p)
	if err != nil {
		t.Fatalf("LoadWAV: %v", err)
	}
	if s.NumChans != 2 {
		t.Errorf("channels = %d, want 2", s.NumChans)
	}
	if s.SampleRate != 44100 {
		t.Errorf("rate = %d, want 44100", s.SampleRate)
	}
	if s.FrameCount() != 32 {
		t.Errorf("frames = %d, want 32", s.FrameCount())
	}
	// Values should sit in [-1, 1]. Our triangle peaks at roughly ±peak/2.
	var max float32
	for _, v := range s.Data {
		if math.Abs(float64(v)) > float64(max) {
			max = float32(math.Abs(float64(v)))
		}
	}
	if max < 0.1 || max > 1.0 {
		t.Errorf("peak amplitude %f out of expected range", max)
	}
}

func TestLoadWAV_16BitMono(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.wav")
	writeWAV(t, p, 16, 1, 44100, 16)
	s, err := LoadWAV("s", p)
	if err != nil {
		t.Fatalf("LoadWAV: %v", err)
	}
	if s.NumChans != 1 || s.FrameCount() != 16 {
		t.Errorf("mono: channels=%d frames=%d", s.NumChans, s.FrameCount())
	}
}

func TestLoadWAV_24BitStereo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.wav")
	writeWAV(t, p, 24, 2, 48000, 16)
	s, err := LoadWAV("s", p)
	if err != nil {
		t.Fatalf("LoadWAV 24-bit: %v", err)
	}
	if s.SampleRate != 48000 {
		t.Errorf("rate = %d, want 48000", s.SampleRate)
	}
}

func TestLoadWAV_RejectsTooManyChannels(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.wav")
	writeWAV(t, p, 16, 4, 44100, 8)
	_, err := LoadWAV("s", p)
	if err == nil {
		t.Error("expected error for 4-channel WAV")
	}
}

func TestLoadWAV_NonExistent(t *testing.T) {
	if _, err := LoadWAV("missing", "/nope/nope/nope.wav"); err == nil {
		t.Error("expected error for missing file")
	}
}
