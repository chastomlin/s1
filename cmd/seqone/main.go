// seqone is the TUI client. It dials the engine daemon over its Unix
// socket, auto-spawning seqoned if one isn't already running, and presents
// a Sequencer-One-style tracks-first view. The "init" subcommand scaffolds
// a new project directory without needing the engine.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"seqone/internal/protocol"
	"seqone/internal/scaffold"
	"seqone/internal/tui"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "init" {
		runInit(os.Args[2:])
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run performs the whole TUI startup/teardown and returns any error. Using
// an inner function lets us cleanly defer the daemon + connection cleanup
// regardless of where a failure happens — os.Exit on errors would skip the
// defers and leak the auto-spawned seqoned process.
func run() error {
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintln(out, "Usage: seqone [flags]              launch the TUI client (auto-starts seqoned if needed)")
		fmt.Fprintln(out, "       seqone init <dir>           scaffold a new project at <dir>")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Flags:")
		flag.PrintDefaults()
	}
	socket := flag.String("socket", protocol.DefaultSocketPath(os.Getenv("XDG_RUNTIME_DIR")), "engine socket")
	songPath := flag.String("song", "", "song.toml to load on startup")
	midiTarget := flag.String("midi", "", "forward -midi host:port to auto-spawned seqoned (ignored if seqoned already running)")
	midiIn := flag.String("midi-in", "", "forward -midi-in (controller device substring or /dev/snd path) to auto-spawned seqoned")
	midiInTrack := flag.String("midi-in-track", "", "forward -midi-in-track (live-play target track id) to auto-spawned seqoned")
	flag.Parse()

	// Try the existing socket first, briefly. If nothing's there, spawn one.
	conn, err := dialWithRetry(*socket, 2, 50*time.Millisecond)
	var daemon *daemonHandle
	if err != nil {
		daemon, err = spawnDaemon(*socket, *midiTarget, *midiIn, *midiInTrack)
		if err != nil {
			return fmt.Errorf("could not start seqoned: %w", err)
		}
		conn, err = dialWithRetry(*socket, 50, 100*time.Millisecond)
		if err != nil {
			_ = daemon.stop()
			return fmt.Errorf("spawned seqoned but couldn't connect at %s: %w (logs: %s)", *socket, err, daemon.logPath)
		}
	}
	defer conn.Close()
	defer func() {
		if daemon != nil {
			_ = daemon.stop()
		}
	}()

	m := tui.New(conn, *songPath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: seqone init <dir>")
		fmt.Fprintln(os.Stderr, "  scaffold a new seqone project (song.toml + patterns/) at <dir>")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	dir := fs.Arg(0)
	if err := scaffold.Write(dir); err != nil {
		fmt.Fprintf(os.Stderr, "seqone init: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("scaffolded %s\n", dir)
	fmt.Println("next:")
	fmt.Printf("  $EDITOR %s/song.toml        # add samples, instruments, tracks\n", dir)
	fmt.Printf("  $EDITOR %s/patterns/a.pat   # fill in track cells\n", dir)
	fmt.Printf("  seqone --song %s/song.toml  # open the TUI (engine auto-starts)\n", dir)
}

func dialWithRetry(path string, tries int, backoff time.Duration) (net.Conn, error) {
	var lastErr error
	for i := 0; i < tries; i++ {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(backoff)
	}
	return nil, lastErr
}

// daemonHandle tracks a seqoned process that this seqone instance spawned.
// The log file and the child process both need cleanup on TUI exit.
type daemonHandle struct {
	cmd     *exec.Cmd
	logFile *os.File
	logPath string
}

func (d *daemonHandle) stop() error {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return nil
	}
	defer func() {
		if d.logFile != nil {
			_ = d.logFile.Close()
		}
	}()

	_ = d.cmd.Process.Signal(syscall.SIGTERM)

	// cmd.Wait reaps the zombie and returns once the child actually exits.
	// Guarded by a timeout in case seqoned is stuck — we escalate to
	// SIGKILL rather than let seqone hang on exit.
	done := make(chan error, 1)
	go func() { done <- d.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done // reap
		return fmt.Errorf("seqoned did not exit within 3s; killed")
	}
}

// spawnDaemon locates the seqoned binary (sibling to our own exe first,
// then $PATH), starts it with the given socket path, and redirects its
// stdout/stderr to a log file so it doesn't corrupt the TUI alt-screen.
// Non-empty midiTarget / midiIn / midiInTrack are forwarded as -midi /
// -midi-in / -midi-in-track respectively.
func spawnDaemon(socketPath, midiTarget, midiIn, midiInTrack string) (*daemonHandle, error) {
	exe, err := locateSeqoned()
	if err != nil {
		return nil, err
	}
	logPath := defaultLogPath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}

	args := []string{"-socket", socketPath}
	if midiTarget != "" {
		args = append(args, "-midi", midiTarget)
	}
	if midiIn != "" {
		args = append(args, "-midi-in", midiIn)
	}
	if midiInTrack != "" {
		args = append(args, "-midi-in-track", midiInTrack)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// New process group so Ctrl-C in a terminal doesn't racing-kill the
	// child before the TUI has a chance to shut it down cleanly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %s: %w", exe, err)
	}
	return &daemonHandle{cmd: cmd, logFile: logFile, logPath: logPath}, nil
}

// locateSeqoned finds the seqoned binary. Prefers a sibling file to the
// running seqone executable (covers `bin/seqoned` next to `bin/seqone`),
// then falls back to $PATH.
func locateSeqoned() (string, error) {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "seqoned")
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}
	return exec.LookPath("seqoned")
}

func defaultLogPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "seqoned.log")
	}
	return filepath.Join(os.TempDir(), "seqoned.log")
}
