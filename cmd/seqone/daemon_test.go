package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSpawnAndStopDaemon asserts the core auto-spawn mechanism: build the
// seqoned binary into a temp dir, spawn it via spawnDaemon, observe its
// socket come up, then tear it down cleanly.
func TestSpawnAndStopDaemon(t *testing.T) {
	dir := t.TempDir()
	// Build seqoned into the same dir the test binary lives in (whatever
	// temp dir go-test uses). We put it on $PATH so locateSeqoned finds it.
	binDir := t.TempDir()
	seqonedPath := filepath.Join(binDir, "seqoned")
	build := exec.Command("go", "build", "-o", seqonedPath, "seqone/cmd/seqoned")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build seqoned: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	socket := filepath.Join(dir, "seqoned.sock")
	// Redirect log into the temp dir by shadowing $XDG_RUNTIME_DIR.
	t.Setenv("XDG_RUNTIME_DIR", dir)

	daemon, err := spawnDaemon(socket, "", "", "")
	if err != nil {
		t.Fatalf("spawnDaemon: %v", err)
	}

	// Wait up to 2s for the socket to accept connections.
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		_ = daemon.stop()
		t.Fatalf("spawned seqoned never started listening on %s: %v", socket, err)
	}
	_ = conn.Close()

	// Stop should terminate the child and return without error.
	if err := daemon.stop(); err != nil {
		t.Errorf("daemon.stop: %v", err)
	}

	// Log file was created in the temp dir.
	if _, err := os.Stat(filepath.Join(dir, "seqoned.log")); err != nil {
		t.Errorf("expected seqoned.log in XDG_RUNTIME_DIR: %v", err)
	}

	// Socket listener is gone — a fresh dial should fail.
	if _, err := net.Dial("unix", socket); err == nil {
		t.Error("expected dial to fail after daemon.stop, but it succeeded")
	}
}
