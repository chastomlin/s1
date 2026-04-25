package engine

import (
	"sync"

	"seqone/internal/protocol"
)

// Bus is a simple fan-out event broadcaster. Subscribers receive every
// published event on their own buffered channel; a slow subscriber is dropped
// rather than allowed to block the engine.
type Bus struct {
	mu   sync.Mutex
	subs map[chan protocol.Event]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: map[chan protocol.Event]struct{}{}}
}

// Subscribe registers a new subscriber and returns its channel plus an
// unsubscribe func. Channel is buffered so brief bursts don't drop events.
func (b *Bus) Subscribe() (<-chan protocol.Event, func()) {
	ch := make(chan protocol.Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(ev protocol.Event) {
	b.mu.Lock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Drop this event for this subscriber rather than block.
		}
	}
	b.mu.Unlock()
}
