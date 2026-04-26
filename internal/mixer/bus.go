package mixer

// trackBus is a per-track scratch L/R buffer plus its EQ + compressor.
// Voices that share a chokeKey accumulate into the bus's bufL/bufR
// during a callback; once all voices have been mixed, the bus's
// active effects process the stereo pair in place; finally the bus
// sums into the master output.
//
// All slices are pre-allocated up to maxFrames at SetTrackBuses time
// and resliced (not reallocated) per buffer, keeping the audio thread
// allocation-free. The eq/comp are bypass-checked per buffer so the
// "no effects" case is a copy plus a sum, no DSP.
type trackBus struct {
	bufL     []float32
	bufR     []float32
	eq       threeBandEQ
	eqOn     bool
	drive    driveStage
	driveOn  bool
	filter   svFilter
	filterOn bool
	lofi     lofiCrush
	lofiOn   bool
	comp     compressor
	compOn   bool
	reverb   reverbStage
	reverbOn bool
}

// reset zeros the bus's scratch buffers so a fresh accumulation starts
// from silence. Length stays the same; the audio thread reslices as
// needed for shorter callback frame counts.
func (b *trackBus) reset(frames int) {
	for i := 0; i < frames; i++ {
		b.bufL[i] = 0
		b.bufR[i] = 0
	}
}

// process runs the bus's effect chain over the first `frames` samples
// of bufL/bufR in place. Chain order:
// EQ → Drive → Filter → Lofi → Comp → Reverb. Skips inactive effects
// so a track without configured DSP pays no cost.
func (b *trackBus) process(frames int) {
	if !b.eqOn && !b.driveOn && !b.filterOn && !b.lofiOn && !b.compOn && !b.reverbOn {
		return
	}
	l := b.bufL[:frames]
	r := b.bufR[:frames]
	if b.eqOn {
		b.eq.process(l, r)
	}
	if b.driveOn {
		b.drive.process(l, r)
	}
	if b.filterOn {
		b.filter.process(l, r)
	}
	if b.lofiOn {
		b.lofi.process(l, r)
	}
	if b.compOn {
		b.comp.process(l, r)
	}
	if b.reverbOn {
		b.reverb.process(l, r)
	}
}

// TrackChainConfig is the per-track configuration the mixer expects
// when SetTrackBuses is called. Populated by seqoned from the loaded
// song.Track entries — keeps package mixer ignorant of song details.
type TrackChainConfig struct {
	ID       string
	EQ       EQParams
	EQOn     bool
	Drive    DriveParams
	DriveOn  bool
	Filter   FilterParams
	FilterOn bool
	Lofi     LofiParams
	LofiOn   bool
	Comp     CompParams
	CompOn   bool
	Reverb   ReverbParams
	ReverbOn bool
}

// ReverbParams is the public mirror of song.ReverbConfig.
type ReverbParams struct {
	Size    float32
	Damping float32
	Mix     float32
}

// DriveParams is the public mirror of song.DriveConfig.
type DriveParams struct {
	Mode  DriveMode
	Drive float32
	Tone  float32
	Level float32
}

// FilterParams is the public mirror of song.FilterConfig.
type FilterParams struct {
	Mode      FilterMode
	Cutoff    float32
	Resonance float32
}

// LofiParams is the public mirror of song.LofiConfig.
type LofiParams struct {
	Bits int
	Rate float32
}

// EQParams is the public mirror of song.EQConfig — same fields,
// independent type so the mixer doesn't import song.
type EQParams struct {
	LowFreq, LowGain       float32
	MidFreq, MidQ, MidGain float32
	HighFreq, HighGain     float32
}

// CompParams is the public mirror of song.CompConfig.
type CompParams struct {
	ThresholdDB float32
	Ratio       float32
	AttackMs    float32
	ReleaseMs   float32
	MakeupDB    float32
}
