package engine

import "time"

// Clock converts between wall-clock time and musical time (bar, beat, tick)
// at a given tempo. Tick resolution is fixed at PPQN ticks per beat.
const PPQN = 96

type Clock struct {
	BPM         int
	BeatsPerBar int
}

// Position is musical time within a song.
type Position struct {
	Bar  int // 1-based
	Beat int // 1-based within bar
	Tick int // 0..PPQN-1 within beat
}

// ElapsedToPosition converts a wall duration since transport start to a
// musical Position at the current tempo.
func (c Clock) ElapsedToPosition(d time.Duration) Position {
	if c.BPM <= 0 {
		return Position{Bar: 1, Beat: 1}
	}
	bpb := c.BeatsPerBar
	if bpb <= 0 {
		bpb = 4
	}
	totalTicks := c.ElapsedToTicks(d)
	beats := totalTicks / PPQN
	tick := totalTicks % PPQN
	bar := beats / bpb
	beat := beats % bpb
	return Position{Bar: bar + 1, Beat: beat + 1, Tick: tick}
}

// ElapsedToTicks converts a wall duration to absolute musical ticks at the
// current tempo. Returns 0 for non-positive BPM. Uses integer arithmetic
// so the inverse of seek's nanosecond calculation is exact — float math
// leaves non-round BPMs (e.g. 112) one tick short of the target.
func (c Clock) ElapsedToTicks(d time.Duration) int {
	if c.BPM <= 0 {
		return 0
	}
	ns := d.Nanoseconds()
	num := ns * int64(c.BPM) * int64(PPQN)
	den := int64(60) * int64(time.Second)
	return int(num / den)
}
