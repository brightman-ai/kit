// Package broadcast provides a generic fan-out event distributor.
//
// A Broadcaster has exactly one event source (channel) and N subscribers.
// It is the sole consumer of the source channel, eliminating the channel-race
// anti-pattern where multiple goroutines compete to read from the same channel.
//
// Usage across deepwork subsystems:
//
//   - BS-11 CLI Bridge: CLI stdout → Broadcaster → [RingBuffer, WS-1, WS-2, ...]
//   - BS-03 Conversation: EventBus → Broadcaster → [SSE-1, SSE-2, ...]
//   - BS-08 CLI Session: PTY stdout → Broadcaster → [WS] (single subscriber, but correct pattern)
//   - BS-12 Council: Per-channel events → Broadcaster → [SSE subscribers]
//
// Design decisions:
//   - Non-blocking publish to subscribers (slow consumers get dropped, not blocking)
//   - Subscriber channels are buffered (configurable, default 64)
//   - When source closes, all subscriber channels are closed
//   - Thread-safe subscribe/unsubscribe during active broadcast
package broadcast

import "sync"

// Broadcaster[T] distributes events from a single source to N subscribers.
type Broadcaster[T any] struct {
	mu   sync.RWMutex
	subs map[chan T]struct{}
}

// New creates a Broadcaster and starts consuming from sourceCh.
// onEvent is called for each event before broadcasting (e.g., write to buffer).
// When sourceCh closes, all subscriber channels are closed and onDone is called.
func New[T any](sourceCh <-chan T, onEvent func(T), onDone func()) *Broadcaster[T] {
	b := &Broadcaster[T]{
		subs: make(map[chan T]struct{}),
	}
	go b.run(sourceCh, onEvent, onDone)
	return b
}

// Subscribe returns a buffered channel that receives broadcast events.
// The caller must Unsubscribe when done to prevent goroutine leaks.
func (b *Broadcaster[T]) Subscribe(bufSize int) chan T {
	if bufSize <= 0 {
		bufSize = 64
	}
	ch := make(chan T, bufSize)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and drains its channel.
func (b *Broadcaster[T]) Unsubscribe(ch chan T) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	for len(ch) > 0 {
		<-ch
	}
}

// SubscriberCount returns the number of active subscribers.
func (b *Broadcaster[T]) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Broadcaster[T]) run(sourceCh <-chan T, onEvent func(T), onDone func()) {
	for evt := range sourceCh {
		if onEvent != nil {
			onEvent(evt)
		}
		b.mu.RLock()
		for ch := range b.subs {
			select {
			case ch <- evt:
			default:
				// Slow subscriber: drop (catch up via replay buffer)
			}
		}
		b.mu.RUnlock()
	}
	// Source closed: close all subscriber channels
	b.mu.Lock()
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
	b.mu.Unlock()
	if onDone != nil {
		onDone()
	}
}
