package engine

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"

	"seqone/internal/protocol"
)

// Serve accepts connections on a Unix domain socket at socketPath until ctx
// is cancelled. Each client gets its own goroutine pair: one reading
// commands, one writing events from the engine bus.
func Serve(ctx context.Context, socketPath string, eng *Engine) error {
	_ = os.Remove(socketPath) // best-effort cleanup of stale socket
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		lis.Close()
		_ = os.Remove(socketPath)
	}()

	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				wg.Wait()
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConn(ctx, conn, eng)
		}()
	}
}

func handleConn(ctx context.Context, conn net.Conn, eng *Engine) {
	defer conn.Close()

	events, unsub := eng.Bus().Subscribe()
	// unsub is also called explicitly below before waiting on the writer
	// goroutine; this defer is a belt-and-braces for the panic case.
	defer unsub()

	// Writer goroutine: forward bus events to the client until the
	// subscription channel is closed (via unsub) or WriteEvent fails.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for ev := range events {
			if err := protocol.WriteEvent(conn, ev); err != nil {
				return
			}
		}
	}()

	// Reader: block in the current goroutine on incoming commands.
	_ = protocol.ReadCommands(conn, func(c protocol.Command) error {
		if c.Cmd == protocol.CmdQuit {
			return errors.New("quit")
		}
		if err := eng.Apply(c); err != nil {
			_ = protocol.WriteEvent(conn, protocol.Event{
				Event:    protocol.EvError,
				Severity: "error",
				Msg:      err.Error(),
			})
		}
		return nil
	})

	// Close the conn first so any in-flight WriteEvent in the writer
	// fails, then unsub to close the events channel (unblocking the
	// writer if it's sitting in `range events` with nothing to send).
	conn.Close()
	unsub()
	<-writerDone
	_ = ctx // kept for symmetry / future hooks
}
