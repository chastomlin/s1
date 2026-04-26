// Package engine is the sequencer backend. It owns transport state, the
// musical clock, and (eventually) audio + MIDI output. It exposes a Unix
// socket on which it speaks the protocol package's JSON command/event format.
//
// This is the skeleton: transport state and position streaming are real.
// Actual MIDI / sample playback is stubbed — those belong to a later pass
// where the audio callback gets properly wired to JACK/PortAudio.
package engine

import (
	"log"
	"sync"
	"time"

	"seqone/internal/protocol"
	"seqone/internal/song"
)

// Engine holds all mutable transport state. All public methods are safe for
// concurrent use; state is guarded by mu. The event broadcaster owns its own
// goroutine and fan-outs to connected clients.
type Engine struct {
	mu sync.Mutex

	song  *song.Song
	clock Clock
	state protocol.TransportState

	// transport tracking
	startedAt time.Time      // wall time when current "play" began
	accumBase time.Duration  // musical time accumulated from previous play segments
	position  Position

	mutes map[string]bool
	solos map[string]bool

	// loop state: when loopEnabled, the playhead wraps from loopToBar back
	// to loopFromBar. Bars are 1-based; the region is [from, to) — to is
	// the first bar NOT in the loop, matching how Cmd.ToBar is specified.
	loopEnabled bool
	loopFromBar int
	loopToBar   int

	sched    *Scheduler
	lastTick int // highest abs-tick already emitted, to guard tempo/seek races

	// timeSrc is the real-time clock the scheduler reads. Defaults to a
	// wall-clock; seqoned swaps in mixer.NewAudioTimeSource at startup so
	// the scheduler is locked to the audio device's sample clock when it's
	// available. Guarded by mu so SetTimeSource can be called concurrently
	// with tick(); reads inside tick() happen under the same lock.
	timeSrc TimeSource

	bus *Bus
	log *log.Logger
}

func New(logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.Default()
	}
	return &Engine{
		state:   protocol.StateStopped,
		clock:   Clock{BPM: 120, BeatsPerBar: 4},
		mutes:   map[string]bool{},
		solos:   map[string]bool{},
		bus:     NewBus(),
		log:     logger,
		timeSrc: wallClockSource{},
	}
}

// SetTimeSource swaps the real-time clock the scheduler reads. Intended
// for one-shot startup wiring (seqoned injects an audio-callback-driven
// source after the mixer's device opens) and for tests that want a fake
// clock. Safe to call while the engine is running.
func (e *Engine) SetTimeSource(ts TimeSource) {
	if ts == nil {
		return
	}
	e.mu.Lock()
	e.timeSrc = ts
	e.mu.Unlock()
}

// Bus returns the event bus so servers can subscribe clients to it.
func (e *Engine) Bus() *Bus { return e.bus }

// CurrentSong returns the song currently loaded, or nil if none. The song
// struct is never mutated in place — Load swaps a whole new pointer — so
// callers may read its fields without further locking.
func (e *Engine) CurrentSong() *song.Song {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.song
}

// Apply handles a single command and returns an error if it couldn't be processed.
// Side-effect events (state, position, loaded) are broadcast on the Bus.
func (e *Engine) Apply(c protocol.Command) error {
	switch c.Cmd {
	case protocol.CmdLoad:
		return e.load(c.Path)
	case protocol.CmdReload:
		return e.reload()
	case protocol.CmdPlay:
		return e.play()
	case protocol.CmdStop:
		return e.stop()
	case protocol.CmdPause:
		return e.pause()
	case protocol.CmdSeek:
		return e.seek(c.Bar, c.Beat)
	case protocol.CmdSetTempo:
		return e.setTempo(c.BPM)
	case protocol.CmdMute:
		return e.setMute(c.Track, c.Muted)
	case protocol.CmdSolo:
		return e.setSolo(c.Track, c.Soloed)
	case protocol.CmdAudition:
		return e.audition(c.Track, c.Note, c.Vel)
	case protocol.CmdSetTrackPan:
		return e.setTrackPan(c.Track, c.Pan)
	case protocol.CmdSetTrackGain:
		return e.setTrackGain(c.Track, c.Gain)
	case protocol.CmdSetTrackEQ:
		if c.EQ == nil {
			return nil
		}
		return e.setTrackEQ(c.Track, *c.EQ)
	case protocol.CmdSetTrackComp:
		if c.Comp == nil {
			return nil
		}
		return e.setTrackComp(c.Track, *c.Comp)
	case protocol.CmdSetTrackFilter:
		if c.Filter == nil {
			return nil
		}
		return e.setTrackFilter(c.Track, *c.Filter)
	case protocol.CmdSetTrackLofi:
		if c.Lofi == nil {
			return nil
		}
		return e.setTrackLofi(c.Track, *c.Lofi)
	case protocol.CmdSetTrackDrive:
		if c.Drive == nil {
			return nil
		}
		return e.setTrackDrive(c.Track, *c.Drive)
	case protocol.CmdSetTrackReverb:
		if c.Reverb == nil {
			return nil
		}
		return e.setTrackReverb(c.Track, *c.Reverb)
	case protocol.CmdLoop:
		return e.setLoop(c.FromBar, c.ToBar, c.Enabled)
	case protocol.CmdQuit:
		// Handled at the server layer (closes listener). No-op here.
		return nil
	}
	return nil
}

func (e *Engine) load(path string) error {
	s, err := song.Load(path)
	if err != nil {
		e.bus.Publish(protocol.Event{
			Event:    protocol.EvError,
			Severity: "error",
			Msg:      err.Error(),
		})
		return err
	}
	e.mu.Lock()
	// "Same path" means this is a reload (file-watcher, manual r, or post-
	// edit refresh) — the song is structurally the same and the user is
	// almost certainly mid-performance. Reset only what depends on the
	// freshly-parsed data; keep transport, position, and loop state so
	// playback continues unbroken.
	samePath := e.song != nil && e.song.SourcePath == path
	e.song = s
	e.clock.BPM = s.Project.BPM
	// time signature numerator -> beats per bar
	if bpb := parseBeatsPerBar(s.Project.TimeSignature); bpb > 0 {
		e.clock.BeatsPerBar = bpb
	}
	e.sched = NewScheduler(s)
	if samePath {
		// Re-seek the freshly-rebuilt scheduler to the current playhead
		// so events that already played in this pass don't re-emit, and
		// future events fire as expected.
		e.sched.SeekTo(e.lastTick)
	} else {
		e.state = protocol.StateStopped
		e.position = Position{Bar: 1, Beat: 1}
		e.accumBase = 0
		e.lastTick = 0
		// A fresh song's bar numbering is unrelated to the previous one's,
		// so any active loop bounds are meaningless. New song = loop off.
		e.loopEnabled = false
	}
	e.mu.Unlock()

	if !samePath {
		e.emitAllOff()
	}

	e.bus.Publish(protocol.Event{
		Event:        protocol.EvLoaded,
		Patterns:     len(s.Patterns),
		DurationBars: arrangementBars(s),
	})
	e.publishState()
	e.publishPosition()
	return nil
}

func (e *Engine) reload() error {
	e.mu.Lock()
	path := ""
	if e.song != nil {
		path = e.song.SourcePath
	}
	e.mu.Unlock()
	if path == "" {
		return nil
	}
	return e.load(path)
}

func (e *Engine) play() error {
	e.mu.Lock()
	if e.state == protocol.StatePlaying {
		e.mu.Unlock()
		return nil
	}
	e.startedAt = e.timeSrc.Now()
	e.state = protocol.StatePlaying
	e.mu.Unlock()
	e.publishState()
	return nil
}

func (e *Engine) stop() error {
	e.mu.Lock()
	e.state = protocol.StateStopped
	e.accumBase = 0
	e.position = Position{Bar: 1, Beat: 1}
	e.lastTick = 0
	if e.sched != nil {
		e.sched.Reset()
	}
	e.mu.Unlock()
	e.emitAllOff()
	e.publishState()
	e.publishPosition()
	return nil
}

func (e *Engine) pause() error {
	e.mu.Lock()
	if e.state == protocol.StatePlaying {
		e.accumBase += e.timeSrc.Now().Sub(e.startedAt)
	}
	e.state = protocol.StatePaused
	e.mu.Unlock()
	e.publishState()
	return nil
}

func (e *Engine) seek(bar, beat int) error {
	if bar < 1 {
		bar = 1
	}
	if beat < 1 {
		beat = 1
	}
	e.mu.Lock()
	bpb := e.clock.BeatsPerBar
	totalBeats := (bar-1)*bpb + (beat - 1)
	targetTick := totalBeats * PPQN
	// Integer math with ceiling rounding. Float-based computation lands
	// ~1 ns below the target at non-round BPMs (e.g. 112), and the reverse
	// conversion via ElapsedToTicks then truncates one tick short — so
	// seek(2,1) at 112 bpm visibly lands at bar 1 beat 4 tick 95. Ceiling
	// rounding here paired with integer ElapsedToTicks is exact.
	num := int64(targetTick) * int64(60) * int64(time.Second)
	den := int64(e.clock.BPM) * int64(PPQN)
	var accumNs int64
	if den > 0 {
		accumNs = (num + den - 1) / den
	}
	e.accumBase = time.Duration(accumNs)
	if e.state == protocol.StatePlaying {
		e.startedAt = e.timeSrc.Now()
	}
	e.lastTick = targetTick
	if e.sched != nil {
		e.sched.SeekTo(targetTick)
	}
	// Snap the stored position directly to the target rather than round-
	// tripping through ElapsedToPosition — the displayed position is then
	// correct regardless of any sub-tick drift in accumBase.
	e.position = Position{Bar: bar, Beat: beat, Tick: 0}
	e.mu.Unlock()
	e.emitAllOff()
	e.publishPosition()
	e.tick()
	return nil
}

func (e *Engine) setTempo(bpm int) error {
	if bpm < 20 || bpm > 400 {
		return nil
	}
	e.mu.Lock()
	// Preserve musical position across tempo change: compute current musical
	// time at old tempo, then rebase startedAt at new tempo.
	now := e.timeSrc.Now()
	var musical time.Duration = e.accumBase
	if e.state == protocol.StatePlaying {
		musical += now.Sub(e.startedAt)
	}
	e.clock.BPM = bpm
	e.accumBase = musical
	e.startedAt = now
	e.mu.Unlock()
	return nil
}

func (e *Engine) setMute(track string, muted bool) error {
	if track == "" {
		return nil
	}
	e.mu.Lock()
	if muted {
		e.mutes[track] = true
	} else {
		delete(e.mutes, track)
	}
	e.mu.Unlock()
	return nil
}

// setLoop stores the loop region and enabled flag. Enabling with invalid
// bounds (from<1 or to<=from) silently disables looping instead — the TUI
// guards against this at the call site but we re-check defensively.
func (e *Engine) setLoop(from, to int, enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if enabled && (from < 1 || to <= from) {
		e.loopEnabled = false
		return nil
	}
	e.loopEnabled = enabled
	if enabled {
		e.loopFromBar = from
		e.loopToBar = to
	}
	return nil
}

// auditionReleaseDuration is how long a CmdAudition-generated NoteOn
// sustains before the engine publishes an automatic NoteOff. Terminal
// key events report presses only, not releases, so pitched auditions
// need a timeout to avoid stuck notes.
const auditionReleaseDuration = 400 * time.Millisecond

// audition publishes a one-shot note event exactly as the scheduler
// would for a pattern cell. Sample tracks emit SampleTrigger (with a
// pitch if Note != 0); pitched tracks emit NoteOn and schedule a
// matching NoteOff so the voice doesn't stick on.
func (e *Engine) audition(track string, note, vel int) error {
	if track == "" {
		return nil
	}
	if vel <= 0 {
		vel = 100
	}
	e.mu.Lock()
	s := e.song
	// Respect mute/solo so auditioning a muted track stays silent, matching
	// what the user would hear during playback.
	soloActive := len(e.solos) > 0
	muted := e.mutes[track]
	soloed := e.solos[track]
	e.mu.Unlock()
	if (soloActive && !soloed) || (!soloActive && muted) {
		return nil
	}
	if s == nil {
		return nil
	}
	t, ok := s.Tracks[track]
	if !ok {
		return nil
	}
	if t.Sample != "" {
		e.bus.Publish(protocol.Event{
			Event:    protocol.EvNote,
			NoteKind: protocol.SampleTrigger,
			Track:    track,
			Note:     note,
			Vel:      vel,
		})
		return nil
	}
	// Pitched / instrument track.
	if note <= 0 {
		return nil
	}
	e.bus.Publish(protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.NoteOn,
		Track:    track,
		Note:     note,
		Vel:      vel,
	})
	// Auto-release: terminal keypresses don't carry up-events, so we
	// schedule a NoteOff on a timer. The engine fires it even after the
	// user moves on to a different cell / section.
	time.AfterFunc(auditionReleaseDuration, func() {
		e.bus.Publish(protocol.Event{
			Event:    protocol.EvNote,
			NoteKind: protocol.NoteOff,
			Track:    track,
			Note:     note,
		})
	})
	return nil
}

// setTrackPan / setTrackGain / setTrackEQ / setTrackComp swap the
// affected Track in the engine's song under a clone-and-replace
// pattern so concurrent readers (the mixer's resolver, the rtpmidi
// bridge) keep seeing a consistent map. After the update, an
// EvTrackChanged event tells seqoned to reconfigure the mixer's
// per-track buses and the TUI to refresh its mix view.
func (e *Engine) setTrackPan(trackID string, pan float32) error {
	return e.mutateTrack(trackID, func(t *song.Track) { t.Pan = pan })
}

func (e *Engine) setTrackGain(trackID string, gain float32) error {
	return e.mutateTrack(trackID, func(t *song.Track) { t.Gain = gain })
}

func (e *Engine) setTrackEQ(trackID string, cfg protocol.EQConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		t.EQ = song.EQConfig{
			Enabled: cfg.Enabled,
			LowFreq: cfg.LowFreq, LowGain: cfg.LowGain,
			MidFreq: cfg.MidFreq, MidQ: cfg.MidQ, MidGain: cfg.MidGain,
			HighFreq: cfg.HighFreq, HighGain: cfg.HighGain,
		}
	})
}

func (e *Engine) setTrackComp(trackID string, cfg protocol.CompConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		t.Comp = song.CompConfig{
			Enabled:     cfg.Enabled,
			ThresholdDB: cfg.ThresholdDB,
			Ratio:       cfg.Ratio,
			AttackMs:    cfg.AttackMs,
			ReleaseMs:   cfg.ReleaseMs,
			MakeupDB:    cfg.MakeupDB,
		}
	})
}

func (e *Engine) setTrackFilter(trackID string, cfg protocol.FilterConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		ft := song.FilterType(cfg.Type)
		switch ft {
		case song.FilterLowpass, song.FilterHighpass, song.FilterBandpass:
			// ok
		default:
			ft = song.FilterLowpass
		}
		t.Filter = song.FilterConfig{
			Enabled:   cfg.Enabled,
			Type:      ft,
			Cutoff:    cfg.Cutoff,
			Resonance: cfg.Resonance,
		}
	})
}

func (e *Engine) setTrackLofi(trackID string, cfg protocol.LofiConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		t.Lofi = song.LofiConfig{
			Enabled: cfg.Enabled,
			Bits:    cfg.Bits,
			Rate:    cfg.Rate,
		}
	})
}

func (e *Engine) setTrackDrive(trackID string, cfg protocol.DriveConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		dt := song.DriveType(cfg.Type)
		switch dt {
		case song.DriveSoft, song.DriveHard, song.DriveFold:
			// ok
		default:
			dt = song.DriveSoft
		}
		t.Drive = song.DriveConfig{
			Enabled: cfg.Enabled,
			Type:    dt,
			Drive:   cfg.Drive,
			Tone:    cfg.Tone,
			Level:   cfg.Level,
		}
	})
}

func (e *Engine) setTrackReverb(trackID string, cfg protocol.ReverbConfigCmd) error {
	return e.mutateTrack(trackID, func(t *song.Track) {
		t.Reverb = song.ReverbConfig{
			Enabled: cfg.Enabled,
			Size:    cfg.Size,
			Damping: cfg.Damping,
			Mix:     cfg.Mix,
		}
	})
}

// mutateTrack applies fn to a copy of the named track and swaps the
// engine's song pointer to one that holds the new map. Concurrent
// readers either see the old map (intact) or the new map (intact).
func (e *Engine) mutateTrack(trackID string, fn func(*song.Track)) error {
	if trackID == "" {
		return nil
	}
	e.mu.Lock()
	if e.song == nil {
		e.mu.Unlock()
		return nil
	}
	t, ok := e.song.Tracks[trackID]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	fn(&t)
	// Clone the tracks map so we don't mutate the one any other
	// goroutine may currently be iterating.
	newTracks := make(map[string]song.Track, len(e.song.Tracks))
	for k, v := range e.song.Tracks {
		newTracks[k] = v
	}
	newTracks[trackID] = t
	newSong := *e.song
	newSong.Tracks = newTracks
	e.song = &newSong
	e.mu.Unlock()

	e.bus.Publish(protocol.Event{
		Event: protocol.EvTrackChanged,
		Track: trackID,
	})
	return nil
}

func (e *Engine) setSolo(track string, soloed bool) error {
	if track == "" {
		return nil
	}
	e.mu.Lock()
	if soloed {
		e.solos[track] = true
	} else {
		delete(e.solos, track)
	}
	e.mu.Unlock()
	return nil
}

// Run drives the scheduler-emit ticker until ctx is cancelled.
// Wakes at 1 kHz so emitted events land within ~1 ms of their scheduled
// musical tick — fast enough that grid-quantization jitter is inaudible
// (16ths at 124 BPM are 121 ms apart, so ±1 ms is <1% of a grid step).
// Phase 1 of the clock overhaul will replace this wall-clock ticker
// with the audio callback's sample counter for true device-locked timing.
func (e *Engine) Run(stop <-chan struct{}) {
	t := time.NewTicker(time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.tick()
		}
	}
}

func (e *Engine) tick() {
	e.mu.Lock()
	if e.state != protocol.StatePlaying {
		e.mu.Unlock()
		return
	}
	elapsed := e.accumBase + e.timeSrc.Now().Sub(e.startedAt)
	pos := e.clock.ElapsedToPosition(elapsed)
	nowTick := e.clock.ElapsedToTicks(elapsed)

	// Decide whether this tick crosses the loop-out boundary. The wrap
	// has to be detected before the first drain so we can cap that drain
	// at the loop end (events past the boundary belong to the next pass).
	willWrap := e.loopEnabled && e.loopToBar > e.loopFromBar && pos.Bar >= e.loopToBar

	// Phase 1 — drain events that should fire on this tick before the
	// wrap (i.e. the tail of the current pass). Without this cap, events
	// in the last few ticks of the loop region were silently dropped.
	loopEndTick := 0
	if willWrap {
		loopEndTick = (e.loopToBar - 1) * e.clock.BeatsPerBar * PPQN
	}
	emitTo := nowTick
	if willWrap && loopEndTick < emitTo {
		emitTo = loopEndTick
	}
	var noteEvents []NoteEvent
	if e.sched != nil && emitTo > e.lastTick {
		noteEvents = e.sched.Advance(emitTo)
		e.lastTick = emitTo
	}

	// Phase 2 — apply the wrap with phase preservation. The plain reset
	// (accumBase = fromBeatsTime, startedAt = now) discards the overshoot:
	// the wrap is detected up to one tick interval (~33 ms) past the
	// boundary, then pass 2's first event has to wait for the *next* tick
	// — another ~33 ms — so the loop point ends up with up to ~66 ms of
	// dead time. Audible as a "pause before the phrase starts again."
	//
	// Fix: bake the overshoot back into accumBase, then immediately drain
	// any pass-2 events that fall inside the overshoot window. Pass 2's
	// downbeat fires on the same tick as the wrap and the loop boundary
	// stays musically tight.
	wrapped := false
	if willWrap {
		fromBeats := (e.loopFromBar - 1) * e.clock.BeatsPerBar
		fromBeatsTime := beatsToDuration(fromBeats, e.clock.BPM)
		// Overshoot in ticks at the moment of detection; clamp at zero so
		// a tick that lands exactly on the boundary doesn't go negative.
		overshootTicks := nowTick - loopEndTick
		if overshootTicks < 0 {
			overshootTicks = 0
		}
		overshootTime := ticksToDuration(overshootTicks, e.clock.BPM)

		e.accumBase = fromBeatsTime + overshootTime
		e.startedAt = e.timeSrc.Now()
		elapsed = e.accumBase
		pos = e.clock.ElapsedToPosition(elapsed)
		nowTick = e.clock.ElapsedToTicks(elapsed)

		fromBeatsTick := fromBeats * PPQN
		if e.sched != nil {
			e.sched.SeekTo(fromBeatsTick)
		}
		// lastTick set to the start of pass 2 (not nowTick) so the
		// post-wrap drain below picks up events in [fromBeatsTick, nowTick)
		// — i.e. anything that should already have fired by now.
		e.lastTick = fromBeatsTick
		wrapped = true
	}

	// Phase 3 — drain pass-2 events whose AbsTick falls inside the
	// overshoot window. For a typical 33 ms / ~6-tick overshoot this
	// fires the downbeat (and anything else at the very start of the
	// loop) on the same tick as the wrap.
	if wrapped && e.sched != nil && nowTick > e.lastTick {
		noteEvents = append(noteEvents, e.sched.Advance(nowTick)...)
		e.lastTick = nowTick
	}

	changed := pos != e.position
	e.position = pos
	section := currentSection(e.song, pos, e.clock.BeatsPerBar)
	// Snapshot mute/solo so we can filter after releasing the lock.
	soloActive := len(e.solos) > 0
	mutes := cloneBoolMap(e.mutes)
	solos := cloneBoolMap(e.solos)
	e.mu.Unlock()

	if wrapped {
		// Soft cleanup: held MIDI notes get released so they don't hang
		// past the loop wrap, but sample voices keep decaying — the
		// mixer's per-track choke handles overlap with the next pass's
		// onset so we don't need (and don't want) a hard AllOff here.
		e.bus.Publish(protocol.Event{Event: protocol.EvNote, NoteKind: protocol.NoteHoldsRelease})
	}

	for _, n := range noteEvents {
		if soloActive {
			if !solos[n.Track] {
				continue
			}
		} else if mutes[n.Track] {
			continue
		}
		e.bus.Publish(protocol.Event{
			Event:    protocol.EvNote,
			NoteKind: n.Kind,
			Track:    n.Track,
			Section:  n.Section,
			Note:     n.Note,
			Vel:      n.Vel,
		})
	}

	if !changed {
		return
	}
	e.bus.Publish(protocol.Event{
		Event:   protocol.EvPosition,
		Bar:     pos.Bar,
		Beat:    pos.Beat,
		Tick:    pos.Tick,
		Section: section,
	})
}

// emitAllOff broadcasts a single note event instructing downstream listeners
// (future MIDI/sample backends, TUI status) to silence anything currently
// sounding. Called on load, stop, and seek.
func (e *Engine) emitAllOff() {
	e.bus.Publish(protocol.Event{
		Event:    protocol.EvNote,
		NoteKind: protocol.NoteAllOff,
	})
}

func cloneBoolMap(m map[string]bool) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *Engine) publishState() {
	e.mu.Lock()
	st := e.state
	e.mu.Unlock()
	e.bus.Publish(protocol.Event{Event: protocol.EvState, State: st})
}

func (e *Engine) publishPosition() {
	e.mu.Lock()
	pos := e.position
	section := currentSection(e.song, pos, e.clock.BeatsPerBar)
	e.mu.Unlock()
	e.bus.Publish(protocol.Event{
		Event:   protocol.EvPosition,
		Bar:     pos.Bar,
		Beat:    pos.Beat,
		Tick:    pos.Tick,
		Section: section,
	})
}

// --- Helpers ---------------------------------------------------------------

// beatsToDuration is the inverse of Clock.ElapsedToTicks for whole beats.
// Integer ns arithmetic keeps the round-trip exact for non-round BPMs
// (e.g. 119) where float math would leave us off by one tick.
func beatsToDuration(beats, bpm int) time.Duration {
	if bpm <= 0 {
		return 0
	}
	return time.Duration(int64(beats) * int64(time.Second) * 60 / int64(bpm))
}

// ticksToDuration mirrors beatsToDuration at PPQN granularity.
func ticksToDuration(ticks, bpm int) time.Duration {
	if bpm <= 0 || ticks == 0 {
		return 0
	}
	return time.Duration(int64(ticks) * int64(time.Second) * 60 / (int64(bpm) * int64(PPQN)))
}

func parseBeatsPerBar(ts string) int {
	// "4/4" -> 4, "3/4" -> 3, "6/8" -> 6
	for i := 0; i < len(ts); i++ {
		if ts[i] == '/' {
			n := 0
			for j := 0; j < i; j++ {
				if ts[j] < '0' || ts[j] > '9' {
					return 0
				}
				n = n*10 + int(ts[j]-'0')
			}
			return n
		}
	}
	return 0
}

func arrangementBars(s *song.Song) int {
	total := 0
	for _, slot := range s.Arrangement {
		if sec, ok := s.Sections[slot.Section]; ok {
			total += sec.Bars * slot.Repeat
		}
	}
	return total
}

// currentSection returns the name of the arranged section the playhead is
// currently inside, or "" if out of range. Repeated slots are walked as
// Repeat×Bars consecutive bars.
func currentSection(s *song.Song, pos Position, beatsPerBar int) string {
	if s == nil {
		return ""
	}
	barOffset := pos.Bar - 1
	for _, slot := range s.Arrangement {
		sec, ok := s.Sections[slot.Section]
		if !ok {
			continue
		}
		span := sec.Bars * slot.Repeat
		if barOffset < span {
			return slot.Section
		}
		barOffset -= span
	}
	return ""
}
