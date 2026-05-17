package obs

import (
	"context"
	"sync"
	"time"
)

const defaultLogCoalesceWindow = 30 * time.Second

// LogCoalescer is for high-frequency observational logs, such as polling or
// heartbeat paths. It emits the first event, emits immediately when the caller's
// fingerprint changes, and otherwise emits at most once per window with a count
// of suppressed duplicate observations.
type LogCoalescer struct {
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	states map[string]*logCoalesceState
}

type logCoalesceState struct {
	fingerprint string
	firstSeen   time.Time
	lastSeen    time.Time
	lastEmit    time.Time
	suppressed  int
}

// NewLogCoalescer creates a coalescer with a minimum emit window. A non-positive
// window falls back to 30s so callers cannot accidentally create a noisy logger.
func NewLogCoalescer(window time.Duration) *LogCoalescer {
	return newLogCoalescer(window, time.Now)
}

func newLogCoalescer(window time.Duration, now func() time.Time) *LogCoalescer {
	if window <= 0 {
		window = defaultLogCoalesceWindow
	}
	if now == nil {
		now = time.Now
	}
	return &LogCoalescer{
		window: window,
		now:    now,
		states: make(map[string]*logCoalesceState),
	}
}

// Info emits an INFO log according to the coalescing policy.
//
// key is the stable identity of the event stream and is intentionally not logged
// by the coalescer; callers should pass any safe identity fields explicitly in
// args. fingerprint is the semantic state. When fingerprint changes, the event
// is emitted immediately even if the window has not elapsed.
func (c *LogCoalescer) Info(ctx context.Context, logger Logger, key, fingerprint, msg string, args ...any) {
	if c == nil {
		logger.Info(ctx, msg, args...)
		return
	}

	emit, coalesceArgs := c.observe(key, fingerprint)
	if !emit {
		return
	}

	out := make([]any, 0, len(args)+len(coalesceArgs))
	out = append(out, args...)
	out = append(out, coalesceArgs...)
	logger.Info(ctx, msg, out...)
}

func (c *LogCoalescer) observe(key, fingerprint string) (bool, []any) {
	if key == "" {
		key = "_"
	}
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.states[key]
	if !ok {
		c.states[key] = &logCoalesceState{
			fingerprint: fingerprint,
			firstSeen:   now,
			lastSeen:    now,
			lastEmit:    now,
		}
		return true, coalesceArgs("first", 1, 0, c.window, 0, 0)
	}

	st.lastSeen = now
	if st.fingerprint != fingerprint {
		suppressed := st.suppressed
		ageMS := millisSince(st.firstSeen, now)
		silenceMS := millisSince(st.lastEmit, now)
		st.fingerprint = fingerprint
		st.firstSeen = now
		st.lastEmit = now
		st.suppressed = 0
		return true, coalesceArgs("changed", suppressed+1, suppressed, c.window, ageMS, silenceMS)
	}

	if now.Sub(st.lastEmit) >= c.window {
		suppressed := st.suppressed
		ageMS := millisSince(st.firstSeen, now)
		silenceMS := millisSince(st.lastEmit, now)
		st.firstSeen = now
		st.lastEmit = now
		st.suppressed = 0
		return true, coalesceArgs("interval", suppressed+1, suppressed, c.window, ageMS, silenceMS)
	}

	st.suppressed++
	return false, nil
}

func coalesceArgs(reason string, count, suppressed int, window time.Duration, ageMS, silenceMS int64) []any {
	return []any{
		"coalesced", true,
		"coalesce_reason", reason,
		"coalesce_count", count,
		"coalesce_suppressed", suppressed,
		"coalesce_window_ms", window.Milliseconds(),
		"coalesce_age_ms", ageMS,
		"coalesce_silence_ms", silenceMS,
	}
}

func millisSince(start, end time.Time) int64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
