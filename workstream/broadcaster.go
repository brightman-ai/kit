package workstream

import (
	"context"
	"sync"
)

// Broadcaster fans a single producing turn's workstream events out to many
// independent subscribers. It is the live-tail substrate for reattach (WLS-16):
// the turn that owns it Publishes every event once; each attached client gets its
// own buffered channel so a slow client never blocks the producer or its peers.
//
// A small ring buffer keeps the most recent events of the CURRENT turn so a client
// that subscribes mid-turn first receives that backlog (turn-local catch-up) and
// then the live tail with no gap. The buffer is intentionally bounded and in-memory
// — it is NOT the long-history catch-up (that is transcript-backed, C2); it only
// closes the window between "turn started" and "client attached".
//
// Lifecycle: the producer creates one Broadcaster per in-flight turn, Publishes
// events, and calls Close when the turn ends. Close drains every subscriber
// channel closed so each Subscribe consumer sees end-of-stream.
type Broadcaster struct {
	mu         sync.Mutex
	subs       map[int]*subscriber
	nextID     int
	ring       []Event
	ringCap    int
	subBufSize int
	closed     bool
}

type subscriber struct {
	ch      chan Event
	ctx     context.Context
	stop    chan struct{} // closed when this subscriber is removed (release or Close)
	dropped bool          // a bounded-buffer overflow has occurred for this subscriber
}

const (
	defaultRingCap    = 256
	defaultSubBufSize = 256
)

// NewBroadcaster builds a Broadcaster. ringCap is the number of recent events kept
// for mid-turn catch-up; subBufSize is each subscriber's channel buffer (a slow
// subscriber that exceeds it drops the OLDEST buffered event rather than blocking
// the producer). Non-positive values fall back to sane defaults.
func NewBroadcaster(ringCap, subBufSize int) *Broadcaster {
	if ringCap <= 0 {
		ringCap = defaultRingCap
	}
	if subBufSize <= 0 {
		subBufSize = defaultSubBufSize
	}
	return &Broadcaster{
		subs:       make(map[int]*subscriber),
		ring:       make([]Event, 0, ringCap),
		ringCap:    ringCap,
		subBufSize: subBufSize,
	}
}

// Subscribe registers a new consumer and returns its event channel plus a release
// func. The channel first replays the current ring-buffer backlog (mid-turn
// catch-up) and then carries live events until the turn ends (Close) or the
// caller's ctx is done. The channel is closed when the stream ends; the caller
// MUST call release when it stops reading (idempotent; releasing also unblocks the
// producer from ever targeting a abandoned subscriber).
//
// If the Broadcaster is already closed, Subscribe returns a channel pre-loaded
// with whatever remains in the ring buffer and then closed — so a client that
// attaches the instant a turn finishes still sees the tail, never an empty stream.
func (b *Broadcaster) Subscribe(ctx context.Context) (<-chan Event, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// Size the channel so the full backlog fits without blocking the seed below.
	bufSize := b.subBufSize
	if len(b.ring) > bufSize {
		bufSize = len(b.ring)
	}
	ch := make(chan Event, bufSize)
	for _, ev := range b.ring {
		ch <- ev // guaranteed non-blocking: cap >= len(ring)
	}

	if b.closed {
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	sub := &subscriber{ch: ch, ctx: ctx, stop: make(chan struct{})}
	b.subs[id] = sub

	release := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(s.ch)
			close(s.stop)
		}
	}

	// Auto-release when the subscriber's ctx is done so an abandoned client cannot
	// keep its slot forever. The goroutine also exits via stop (release / Close).
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				release()
			case <-sub.stop:
			}
		}()
	}

	return ch, release
}

// Publish delivers ev to every current subscriber and records it in the ring
// buffer for later mid-turn catch-up. A subscriber whose buffer is full has its
// OLDEST queued event evicted to make room — the producer is never blocked by a
// slow consumer. No-op after Close.
func (b *Broadcaster) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	// Record in ring buffer (drop oldest when full).
	if len(b.ring) >= b.ringCap {
		copy(b.ring, b.ring[1:])
		b.ring[len(b.ring)-1] = ev
	} else {
		b.ring = append(b.ring, ev)
	}
	for _, s := range b.subs {
		b.deliver(s, ev)
	}
}

// deliver pushes ev to a single subscriber without blocking. b.mu must be held.
func (b *Broadcaster) deliver(s *subscriber, ev Event) {
	select {
	case s.ch <- ev:
		return
	default:
	}
	// Buffer full: evict the oldest queued event and retry once. This keeps the
	// stream moving forward (favouring recency) instead of stalling the producer.
	select {
	case <-s.ch:
		s.dropped = true
	default:
	}
	select {
	case s.ch <- ev:
	default:
		s.dropped = true
	}
}

// Close ends the turn's stream: every subscriber channel is closed (signalling
// end-of-stream) and further Publish/Subscribe-as-live calls become no-ops.
// Idempotent.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subs {
		close(s.ch)
		close(s.stop)
		delete(b.subs, id)
	}
}

// SubscriberCount reports how many live subscribers are currently attached. Used by
// the producer to decide whether a finished turn's broadcaster can be reclaimed.
func (b *Broadcaster) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
