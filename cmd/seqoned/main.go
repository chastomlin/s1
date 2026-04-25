// seqoned is the sequencer engine daemon. It owns transport state and
// (eventually) audio + MIDI output. Clients (the TUI, or anything speaking
// the line-JSON protocol) connect over a Unix domain socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"seqone/internal/engine"
	"seqone/internal/mixer"
	"seqone/internal/protocol"
	"seqone/internal/rtpmidi"
	"seqone/internal/song"
)

func main() {
	socket := flag.String("socket", protocol.DefaultSocketPath(os.Getenv("XDG_RUNTIME_DIR")), "Unix socket path")
	loadPath := flag.String("load", "", "optional song.toml to load on startup")
	midiTarget := flag.String("midi", "", "optional RTP-MIDI peer as host:controlPort (e.g. 127.0.0.1:5004)")
	midiName := flag.String("midi-name", "seqone", "local session name advertised to RTP-MIDI peer")
	noAudio := flag.Bool("no-audio", false, "disable local sample playback (MIDI-only output)")
	flag.Parse()

	logger := log.New(os.Stderr, "seqoned ", log.LstdFlags|log.Lmicroseconds)
	eng := engine.New(logger)

	if *loadPath != "" {
		if err := eng.Apply(protocol.Command{Cmd: protocol.CmdLoad, Path: *loadPath}); err != nil {
			logger.Printf("load %s: %v", *loadPath, err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Local sample mixer — optional, failure falls back to MIDI-only.
	var mix *mixer.Mixer
	if !*noAudio {
		mix = mixer.New(nil, logger)
		if err := mix.Start(); err != nil {
			logger.Printf("mixer: %v — continuing without local audio", err)
			mix = nil
		} else {
			defer mix.Stop()
			// Pre-load samples for a song passed via -load.
			if s := eng.CurrentSong(); s != nil {
				loadSamplesInto(mix, s, logger)
			}
			// Pre-populate the per-track meter set + DSP buses for the
			// loaded song.
			if s := eng.CurrentSong(); s != nil {
				mix.SetTrackMeters(s.TrackOrder)
				configureMixerBuses(mix, s)
			}
			// Subscribe for later loads (TUI-driven or reload) — re-load
			// samples and refresh meters + buses when the track list
			// changes. Also reacts to EvTrackChanged (single-track
			// effect tweak) by re-running configureMixerBuses; only
			// the bus coefficients are recomputed, samples are not
			// reloaded.
			loadedEvents, unsub := eng.Bus().Subscribe()
			go func() {
				defer unsub()
				for ev := range loadedEvents {
					switch ev.Event {
					case protocol.EvLoaded:
						if s := eng.CurrentSong(); s != nil {
							loadSamplesInto(mix, s, logger)
							mix.SetTrackMeters(s.TrackOrder)
							configureMixerBuses(mix, s)
						}
					case protocol.EvTrackChanged:
						if s := eng.CurrentSong(); s != nil {
							configureMixerBuses(mix, s)
						}
					}
				}
			}()
			// Publish meter readings on the bus at ~20 Hz so the TUI
			// can render the per-track + master meters without polling
			// over IPC. Only runs while the mixer exists; tied to ctx.
			go func() {
				t := time.NewTicker(50 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						publishMeterFrame(eng, mix)
					}
				}
			}()
			// Subscribe the sample-trigger bridge.
			triggerEvents, unsub2 := eng.Bus().Subscribe()
			resolve := func(id string) mixer.TrackRoute {
				s := eng.CurrentSong()
				if s == nil {
					return mixer.TrackRoute{}
				}
				t, ok := s.Tracks[id]
				if !ok {
					return mixer.TrackRoute{}
				}
				return mixer.TrackRoute{
					SampleID: t.Sample,
					BaseNote: t.BaseNote,
					Pan:      t.Pan,
					Gain:     t.Gain,
				}
			}
			br := mixer.NewBridge(mix, resolve, logger)
			go func() {
				defer unsub2()
				br.Run(triggerEvents)
			}()
		}
	}

	var midiSess *rtpmidi.Session
	if *midiTarget != "" {
		host, port, err := splitHostPort(*midiTarget)
		if err != nil {
			logger.Fatalf("midi target %q: %v", *midiTarget, err)
		}
		midiSess, err = rtpmidi.Dial(ctx, host, port, *midiName, 3*time.Second, logger)
		if err != nil {
			logger.Fatalf("rtpmidi dial %s:%d: %v", host, port, err)
		}
		logger.Printf("rtpmidi connected to %s:%d", host, port)

		events, unsub := eng.Bus().Subscribe()
		resolve := func(id string) (rtpmidi.TrackRouting, bool) {
			s := eng.CurrentSong()
			if s == nil {
				return rtpmidi.TrackRouting{}, false
			}
			t, ok := s.Tracks[id]
			if !ok {
				return rtpmidi.TrackRouting{}, false
			}
			return routingFor(t), true
		}
		bridge := rtpmidi.NewBridge(midiSess, resolve, logger)
		go func() {
			bridge.Run(events)
			unsub()
		}()
		defer func() {
			unsub()
			_ = midiSess.Close()
		}()
	}

	tickStop := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(tickStop)
	}()
	go eng.Run(tickStop)

	logger.Printf("listening on %s", *socket)
	if err := engine.Serve(ctx, *socket, eng); err != nil {
		logger.Fatalf("serve: %v", err)
	}
	logger.Printf("shutdown")
}

// configureMixerBuses translates each track's EQ + compressor config
// from the loaded song into a mixer.TrackChainConfig and hands the
// list to the mixer. Tracks without active effects are still listed
// (with both flags false) so the mixer knows about them; that lets
// the per-track meter attribute their voice peaks correctly even
// when no DSP is configured.
func configureMixerBuses(m *mixer.Mixer, s *song.Song) {
	cfgs := make([]mixer.TrackChainConfig, 0, len(s.TrackOrder))
	for _, id := range s.TrackOrder {
		t, ok := s.Tracks[id]
		if !ok {
			continue
		}
		cfg := mixer.TrackChainConfig{ID: id}
		if t.EQ.IsActive() {
			cfg.EQ = mixer.EQParams{
				LowFreq:  t.EQ.LowFreq,
				LowGain:  t.EQ.LowGain,
				MidFreq:  t.EQ.MidFreq,
				MidQ:     t.EQ.MidQ,
				MidGain:  t.EQ.MidGain,
				HighFreq: t.EQ.HighFreq,
				HighGain: t.EQ.HighGain,
			}
			cfg.EQOn = true
		}
		if t.Comp.IsActive() {
			cfg.Comp = mixer.CompParams{
				ThresholdDB: t.Comp.ThresholdDB,
				Ratio:       t.Comp.Ratio,
				AttackMs:    t.Comp.AttackMs,
				ReleaseMs:   t.Comp.ReleaseMs,
				MakeupDB:    t.Comp.MakeupDB,
			}
			cfg.CompOn = true
		}
		// Only allocate a bus when at least one effect is on. Tracks
		// without effects bypass the per-bus accumulation entirely
		// (voices fall through to the master scratch path), saving
		// one float-add per frame per voice.
		if cfg.EQOn || cfg.CompOn {
			cfgs = append(cfgs, cfg)
		}
	}
	m.SetTrackBuses(cfgs, 4096)
}

// publishMeterFrame snapshots the mixer's meter bank and pushes a
// single EvMeter event onto the engine bus so subscribed TUIs can
// render levels without per-track polling. No-op when no song is
// loaded (nothing to meter).
func publishMeterFrame(eng *engine.Engine, mix *mixer.Mixer) {
	s := eng.CurrentSong()
	if s == nil {
		return
	}
	mL, mR := mix.MasterLevels()
	tracks := make(map[string]protocol.MeterReadings, len(s.TrackOrder))
	for _, id := range s.TrackOrder {
		l, r := mix.TrackLevels(id)
		// Drop tracks that are completely silent so the wire frame
		// stays compact during long quiet stretches.
		if l.Level == 0 && r.Level == 0 && l.Peak == 0 && r.Peak == 0 {
			continue
		}
		tracks[id] = protocol.MeterReadings{
			LL: l.Level, LP: l.Peak,
			RL: r.Level, RP: r.Peak,
		}
	}
	eng.Bus().Publish(protocol.Event{
		Event: protocol.EvMeter,
		Master: protocol.MeterReadings{
			LL: mL.Level, LP: mL.Peak,
			RL: mR.Level, RP: mR.Peak,
		},
		MeterTracks: tracks,
	})
}

// loadSamplesInto decodes every sample referenced by the song and hands
// the resulting bank to the mixer. Individual decode failures are logged
// and skipped — the engine continues and those triggers simply won't
// produce audio.
func loadSamplesInto(m *mixer.Mixer, s *song.Song, logger *log.Logger) {
	bank := make(map[string]*mixer.Sample, len(s.Samples))
	for id, sm := range s.Samples {
		loaded, err := mixer.LoadWAV(id, sm.Path)
		if err != nil {
			logger.Printf("mixer: skip %s (%s): %v", id, sm.Path, err)
			continue
		}
		bank[id] = loaded
	}
	m.SetSamples(bank)
	logger.Printf("mixer: loaded %d/%d samples", len(bank), len(s.Samples))
}

// routingFor translates a loaded song.Track into the rtpmidi.TrackRouting
// shape the MIDI bridge expects.
func routingFor(t song.Track) rtpmidi.TrackRouting {
	return rtpmidi.TrackRouting{
		Channel:  t.Channel,
		Note:     t.Note,
		IsSample: t.Sample != "",
	}
}

// splitHostPort parses "host:port" or "[host]:port" into its two components.
// We can't use net.SplitHostPort alone because it returns a string port; we
// also want the int.
func splitHostPort(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("port %q is not an integer", p)
	}
	return h, port, nil
}
