package workstream

import (
	"context"
	"sync/atomic"
	"time"
)

// HeartbeatEmitter wraps an emitter and sends a synthetic running status when
// no user-visible event has been emitted for interval. It is provider-agnostic:
// native streaming models, non-streaming models, tool execution, and CLI
// adapters all share the same workstream liveness contract.
func HeartbeatEmitter(ctx context.Context, next Emitter, interval time.Duration) Emitter {
	if next == nil {
		next = Discard()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	wrapped := func(ev Event) bool {
		last.Store(time.Now().UnixNano())
		return next(ev)
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, last.Load())) < interval {
					continue
				}
				wrapped(StatusEvent("running").
					WithSource("synthetic").
					WithMeta("heartbeat", true))
			}
		}
	}()

	return wrapped
}
