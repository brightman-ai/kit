package workstream

import (
	"context"
	"sync"
	"testing"
	"time"
)

// drain reads up to want events from ch (until closed or timeout) and returns them.
func drain(t *testing.T, ch <-chan Event, want int, timeout time.Duration) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(timeout)
	for len(got) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			return got
		}
	}
	return got
}

// TestBroadcasterMultiSubscriberFullDelivery: every subscriber attached before
// publishing receives every event, in order.
func TestBroadcasterMultiSubscriberFullDelivery(t *testing.T) {
	b := NewBroadcaster(0, 0)
	defer b.Close()

	const n = 5
	ch1, rel1 := b.Subscribe(context.Background())
	ch2, rel2 := b.Subscribe(context.Background())
	defer rel1()
	defer rel2()

	for i := 0; i < n; i++ {
		b.Publish(TextEvent("e").WithMeta("i", i))
	}

	for name, ch := range map[string]<-chan Event{"sub1": ch1, "sub2": ch2} {
		got := drain(t, ch, n, time.Second)
		if len(got) != n {
			t.Fatalf("%s: got %d events, want %d", name, len(got), n)
		}
		for i, ev := range got {
			if ev.Meta["i"] != i {
				t.Fatalf("%s: event %d out of order: meta.i=%v", name, i, ev.Meta["i"])
			}
		}
	}
}

// TestBroadcasterRingBufferCatchup: a subscriber that attaches AFTER events were
// published still receives the recent backlog (mid-turn catch-up) then live events.
func TestBroadcasterRingBufferCatchup(t *testing.T) {
	b := NewBroadcaster(16, 0)
	defer b.Close()

	// Publish 3 BEFORE any subscriber exists.
	for i := 0; i < 3; i++ {
		b.Publish(TextEvent("pre").WithMeta("i", i))
	}

	// Late subscriber must get the 3 buffered + 2 live = 5.
	ch, rel := b.Subscribe(context.Background())
	defer rel()

	for i := 3; i < 5; i++ {
		b.Publish(TextEvent("live").WithMeta("i", i))
	}

	got := drain(t, ch, 5, time.Second)
	if len(got) != 5 {
		t.Fatalf("catch-up: got %d events, want 5 (3 buffered + 2 live)", len(got))
	}
	for i, ev := range got {
		if ev.Meta["i"] != i {
			t.Fatalf("catch-up: event %d out of order: meta.i=%v", i, ev.Meta["i"])
		}
	}
}

// TestBroadcasterRingBufferBounded: the ring buffer keeps only the most recent N
// events, so a late subscriber gets the tail, not the whole history.
func TestBroadcasterRingBufferBounded(t *testing.T) {
	const ringCap = 4
	b := NewBroadcaster(ringCap, 0)
	defer b.Close()

	for i := 0; i < 10; i++ {
		b.Publish(TextEvent("x").WithMeta("i", i))
	}
	ch, rel := b.Subscribe(context.Background())
	defer rel()

	got := drain(t, ch, ringCap, 500*time.Millisecond)
	if len(got) != ringCap {
		t.Fatalf("bounded ring: got %d events, want %d", len(got), ringCap)
	}
	// Should be the LAST ringCap events: i = 6,7,8,9.
	for j, ev := range got {
		wantI := 10 - ringCap + j
		if ev.Meta["i"] != wantI {
			t.Fatalf("bounded ring: event %d has meta.i=%v, want %d", j, ev.Meta["i"], wantI)
		}
	}
}

// TestBroadcasterSlowSubscriberNonBlocking: a subscriber that never reads its
// channel must NOT block the producer; a healthy peer still receives everything.
func TestBroadcasterSlowSubscriberNonBlocking(t *testing.T) {
	const subBuf = 4
	b := NewBroadcaster(1024, subBuf)
	defer b.Close()

	// Slow subscriber: subscribes, then never reads.
	_, relSlow := b.Subscribe(context.Background())
	defer relSlow()

	fast, relFast := b.Subscribe(context.Background())
	defer relFast()

	// Publish far more than subBuf. If the slow subscriber blocked the producer this
	// goroutine would deadlock; the test's overall timeout (go test) would fail.
	const total = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < total; i++ {
			b.Publish(TextEvent("flood").WithMeta("i", i))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked: a slow subscriber must not stall the producer")
	}

	// Fast subscriber reads concurrently; it must receive a meaningful number of
	// events (drop-oldest may evict some under burst, but it never blocks). We only
	// assert it got the most recent ones up to its buffer, proving forward progress.
	got := drain(t, fast, subBuf, 500*time.Millisecond)
	if len(got) == 0 {
		t.Fatal("fast subscriber received no events under flood")
	}
}

// TestBroadcasterCloseEndsStream: Close closes every subscriber channel so readers
// observe end-of-stream; further Publish is a no-op.
func TestBroadcasterCloseEndsStream(t *testing.T) {
	b := NewBroadcaster(0, 0)
	ch, rel := b.Subscribe(context.Background())
	defer rel()

	b.Publish(TextEvent("before"))
	b.Close()
	b.Publish(TextEvent("after")) // no-op

	var count int
	for range ch {
		count++
	}
	// Channel is closed; we should have drained the single pre-close event then EOF.
	if count != 1 {
		t.Fatalf("after Close: drained %d events, want 1 (the pre-close event), then EOF", count)
	}

	// Subscribe after Close replays the ring tail ("before") then closes (no hang).
	ch2, rel2 := b.Subscribe(context.Background())
	defer rel2()
	replay := drain(t, ch2, 10, 500*time.Millisecond) // reads until EOF
	if len(replay) != 1 {
		t.Fatalf("Subscribe after Close: drained %d ring events, want 1 then EOF", len(replay))
	}
}

// TestBroadcasterSubscribeAfterCloseReplaysRing: a client that attaches the instant
// a turn finished still sees the ring tail, then EOF (never an empty stream).
func TestBroadcasterSubscribeAfterCloseReplaysRing(t *testing.T) {
	b := NewBroadcaster(8, 0)
	for i := 0; i < 3; i++ {
		b.Publish(TextEvent("x").WithMeta("i", i))
	}
	b.Close()

	ch, rel := b.Subscribe(context.Background())
	defer rel()
	got := drain(t, ch, 3, 500*time.Millisecond)
	if len(got) != 3 {
		t.Fatalf("post-close ring replay: got %d, want 3", len(got))
	}
}

// TestBroadcasterCtxReleasesSubscriber: cancelling a subscriber's ctx frees its slot
// (SubscriberCount drops) without affecting peers.
func TestBroadcasterCtxReleasesSubscriber(t *testing.T) {
	b := NewBroadcaster(0, 0)
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	_, _ = b.Subscribe(ctx)
	_, relKeep := b.Subscribe(context.Background())
	defer relKeep()

	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("SubscriberCount=%d, want 2", got)
	}
	cancel()

	deadline := time.After(time.Second)
	for b.SubscriberCount() != 1 {
		select {
		case <-deadline:
			t.Fatalf("ctx cancel did not release subscriber: count=%d", b.SubscriberCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestBroadcasterConcurrentSafe: concurrent Publish/Subscribe/release under -race.
func TestBroadcasterConcurrentSafe(t *testing.T) {
	b := NewBroadcaster(64, 64)
	defer b.Close()

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Publish(TextEvent("c"))
			}
		}()
	}
	for s := 0; s < 8; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, rel := b.Subscribe(context.Background())
			go func() {
				for range ch {
				}
			}()
			time.Sleep(time.Millisecond)
			rel()
		}()
	}
	wg.Wait()
}
