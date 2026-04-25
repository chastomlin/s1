// Package mixer is the local sample playback backend. It subscribes to the
// engine's event bus, mixes triggered samples into a stereo float32 output
// stream, and hands the stream to a system audio device (via miniaudio/malgo).
package mixer

import (
	"fmt"
	"os"

	"github.com/go-audio/wav"
)

// Sample is a decoded WAV held in memory as interleaved float32 PCM in the
// range [-1, 1]. Voices advance through Data at a per-frame step derived
// from SampleRate / deviceRate, so we don't need an offline resampler.
type Sample struct {
	ID         string
	Path       string
	Data       []float32 // interleaved: L R L R … for stereo, L L L … for mono
	NumChans   int       // 1 or 2
	SampleRate int       // source rate in Hz
}

// LoadWAV decodes the WAV at path into a Sample. Supports 8/16/24/32-bit
// integer PCM in mono or stereo. Samples at unusual bit depths or channel
// counts are rejected with an error (not silently misinterpreted).
func LoadWAV(id, path string) (*Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := wav.NewDecoder(f)
	if !dec.IsValidFile() {
		return nil, fmt.Errorf("%s: not a valid WAV file", path)
	}
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		return nil, fmt.Errorf("%s: decode: %w", path, err)
	}
	if buf == nil || buf.Format == nil {
		return nil, fmt.Errorf("%s: empty or malformed PCM data", path)
	}
	ch := buf.Format.NumChannels
	if ch != 1 && ch != 2 {
		return nil, fmt.Errorf("%s: unsupported channel count %d (want 1 or 2)", path, ch)
	}

	// go-audio returns int PCM; scale to float32 [-1, 1] using the source
	// bit depth so we don't clip on 24/32-bit files.
	bitDepth := buf.SourceBitDepth
	if bitDepth <= 0 {
		bitDepth = int(dec.BitDepth)
	}
	scale := 1.0 / float32(int64(1)<<(bitDepth-1))

	src := buf.Data
	dst := make([]float32, len(src))
	for i, s := range src {
		dst[i] = float32(s) * scale
	}
	return &Sample{
		ID:         id,
		Path:       path,
		Data:       dst,
		NumChans:   ch,
		SampleRate: buf.Format.SampleRate,
	}, nil
}

// FrameCount returns the number of audio frames (one frame = one sample
// across all channels) in Data.
func (s *Sample) FrameCount() int {
	if s == nil || s.NumChans == 0 {
		return 0
	}
	return len(s.Data) / s.NumChans
}
