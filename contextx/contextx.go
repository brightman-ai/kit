// Package contextx provides goroutine-local context for tracing and logging.
package contextx

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// Context holds trace context for the current goroutine.
type Context struct {
	TID        string   // 业务流跟踪ID (entry_type/unique_id)
	Stage      string   // 阶段名称 (业务流优先: chat/intent, not http/handler/chat/intent)
	SessionID  string   // 会话ID (用于 Memory 路由和 LanePool 分发)
	TurnID     int64    // Turn 序号 (TS-03 三元组: traceId + sessionId + turnId)
	SpanID     string   // 当前 Span ID
	ParentSpan string   // 父 Span ID
	SpanStack  []string // Span 栈 (用于嵌套)
}

var (
	// storage stores per-goroutine context
	storage sync.Map // map[int64]*Context

	// cleanupThreshold triggers cleanup when storage exceeds this size
	cleanupThreshold = 10000
	cleanupMu        sync.Mutex
)

// GoroutineID returns the current goroutine's ID.
func GoroutineID() int64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	// Stack trace format: "goroutine 123 [running]:"
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	if i < 0 {
		return 0
	}
	id, _ := strconv.ParseInt(string(b[:i]), 10, 64)
	return id
}

// getOrCreate gets or creates context for current goroutine.
func getOrCreate() *Context {
	gid := GoroutineID()
	if ctx, ok := storage.Load(gid); ok {
		return ctx.(*Context)
	}

	ctx := &Context{}
	storage.Store(gid, ctx)

	// Periodic cleanup of dead goroutines
	go maybeCleanup()

	return ctx
}

// Current returns the current goroutine's context.
func Current() *Context {
	gid := GoroutineID()
	if ctx, ok := storage.Load(gid); ok {
		return ctx.(*Context)
	}
	return &Context{}
}

// SetTID sets the trace ID for current goroutine.
// Format: "场景/唯一标识", e.g., "chat/req-001", "upgrade/task-xyz"
func SetTID(tid string) {
	ctx := getOrCreate()
	ctx.TID = tid
}

// GetTID returns the trace ID for current goroutine.
func GetTID() string {
	return Current().TID
}

// SetStage sets the stage name for current goroutine.
func SetStage(stg string) {
	ctx := getOrCreate()
	ctx.Stage = stg
}

// GetStage returns the stage name for current goroutine.
func GetStage() string {
	return Current().Stage
}

// SetSessionID sets the session ID for current goroutine.
func SetSessionID(id string) {
	ctx := getOrCreate()
	ctx.SessionID = id
}

// GetSessionID returns the session ID for current goroutine.
func GetSessionID() string {
	return Current().SessionID
}

// SetTurnID sets the turn ID for current goroutine (TS-03 three-tuple).
func SetTurnID(id int64) {
	ctx := getOrCreate()
	ctx.TurnID = id
}

// GetTurnID returns the turn ID for current goroutine.
func GetTurnID() int64 {
	return Current().TurnID
}

// sessionIDKey is the context.Context key for session ID.
type sessionIDKey struct{}

// turnIDKey is the context.Context key for turn ID (TS-03 three-tuple).
type turnIDKey struct{}

// traceIDKey is the context.Context key for trace ID (TS-03 three-tuple).
type traceIDKey struct{}

// WithSessionID returns a new context.Context with the session ID set.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

// SessionID extracts the session ID from a context.Context.
func SessionID(ctx context.Context) string {
	if id, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return id
	}
	return ""
}

// WithTurnID returns a new context.Context with the turn ID set.
func WithTurnID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, turnIDKey{}, id)
}

// TurnID extracts the turn ID from a context.Context.
func TurnIDFromCtx(ctx context.Context) int64 {
	if id, ok := ctx.Value(turnIDKey{}).(int64); ok {
		return id
	}
	return 0
}

// WithTraceID returns a new context.Context with the trace ID set.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceID extracts the trace ID from a context.Context.
func TraceID(ctx context.Context) string {
	if id, ok := ctx.Value(traceIDKey{}).(string); ok {
		return id
	}
	return ""
}

// InjectTraceContext sets the full TS-03 three-tuple (traceId + sessionId + turnId) into a context.
func InjectTraceContext(ctx context.Context, traceID string, sessionID string, turnID int64) context.Context {
	ctx = WithTraceID(ctx, traceID)
	ctx = WithSessionID(ctx, sessionID)
	ctx = WithTurnID(ctx, turnID)
	return ctx
}

// ExtractTraceContext extracts the full TS-03 three-tuple from a context.
func ExtractTraceContext(ctx context.Context) (traceID string, sessionID string, turnID int64) {
	return TraceID(ctx), SessionID(ctx), TurnIDFromCtx(ctx)
}

// modelIDKey is the context.Context key for model ID.
type modelIDKey struct{}

// WithModelID returns a new context.Context with the model ID set.
// Used to propagate user-selected model from HTTP layer to LLM executor.
func WithModelID(ctx context.Context, modelID string) context.Context {
	return context.WithValue(ctx, modelIDKey{}, modelID)
}

// ModelID extracts the model ID from a context.Context.
// Returns empty string if not set.
func ModelID(ctx context.Context) string {
	if id, ok := ctx.Value(modelIDKey{}).(string); ok {
		return id
	}
	return ""
}

// Clear removes context for current goroutine.
// Should be called when goroutine finishes to avoid memory leak.
func Clear() {
	gid := GoroutineID()
	storage.Delete(gid)
}

// Clone returns a copy of current context for passing to child goroutine.
func Clone() *Context {
	c := Current()
	return &Context{
		TID:        c.TID,
		Stage:      c.Stage,
		SessionID:  c.SessionID,
		TurnID:     c.TurnID,
		SpanID:     c.SpanID,
		ParentSpan: c.ParentSpan,
		SpanStack:  append([]string{}, c.SpanStack...), // 深拷贝
	}
}

// Inherit sets current goroutine's context from a parent context.
func Inherit(parent *Context) {
	if parent == nil {
		return
	}
	ctx := getOrCreate()
	ctx.TID = parent.TID
	ctx.Stage = parent.Stage
	ctx.SessionID = parent.SessionID
	ctx.TurnID = parent.TurnID
	ctx.SpanID = parent.SpanID
	ctx.ParentSpan = parent.ParentSpan
	ctx.SpanStack = append([]string{}, parent.SpanStack...)
}

// ========== Span API ==========

var spanCounter uint64

// generateSpanID generates a unique span ID.
func generateSpanID(name string) string {
	id := atomic.AddUint64(&spanCounter, 1)
	return fmt.Sprintf("%s-%d", name, id)
}

// StartSpan starts a new span and returns the span ID.
func StartSpan(name string) string {
	ctx := getOrCreate()
	spanID := generateSpanID(name)

	// Push current span to stack
	if ctx.SpanID != "" {
		ctx.SpanStack = append(ctx.SpanStack, ctx.SpanID)
		ctx.ParentSpan = ctx.SpanID
	}
	ctx.SpanID = spanID
	return spanID
}

// EndSpan ends the current span and restores parent.
func EndSpan() {
	ctx := getOrCreate()
	if len(ctx.SpanStack) > 0 {
		// Pop from stack
		ctx.SpanID = ctx.SpanStack[len(ctx.SpanStack)-1]
		ctx.SpanStack = ctx.SpanStack[:len(ctx.SpanStack)-1]
		if len(ctx.SpanStack) > 0 {
			ctx.ParentSpan = ctx.SpanStack[len(ctx.SpanStack)-1]
		} else {
			ctx.ParentSpan = ""
		}
	} else {
		ctx.SpanID = ""
		ctx.ParentSpan = ""
	}
}

// GetSpanID returns the current span ID.
func GetSpanID() string {
	return Current().SpanID
}

// GetParentSpan returns the parent span ID.
func GetParentSpan() string {
	return Current().ParentSpan
}

// WithSpan runs a function within a span.
func WithSpan(name string, fn func()) {
	StartSpan(name)
	defer EndSpan()
	fn()
}

// EnterStage sets stage and returns a cleanup function that restores previous.
// Usage: defer contextx.EnterStage("intent")()
func EnterStage(sub string) func() {
	prev := GetStage()
	current := prev
	if current != "" {
		current = current + "/" + sub
	} else {
		current = sub
	}
	SetStage(current)
	return func() {
		SetStage(prev)
	}
}

// WithTID returns a function that sets TID and returns a cleanup function.
// Usage: defer contextx.WithTID("chat/req-001")()
func WithTID(tid string) func() {
	SetTID(tid)
	return Clear
}

// WithStage returns a function that sets stage and restores previous on cleanup.
// Usage: defer contextx.WithStage("http/handler")()
func WithStage(stg string) func() {
	prev := GetStage()
	SetStage(stg)
	return func() {
		SetStage(prev)
	}
}

// Go starts a new goroutine with inherited context.
func Go(fn func()) {
	parent := Clone()
	go func() {
		Inherit(parent)
		defer Clear()
		fn()
	}()
}

// maybeCleanup periodically cleans up storage for dead goroutines.
func maybeCleanup() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()

	// Count entries
	count := 0
	storage.Range(func(_, _ interface{}) bool {
		count++
		return count < cleanupThreshold
	})

	if count < cleanupThreshold {
		return
	}

	// Get all active goroutine IDs from stack traces
	buf := make([]byte, 64*1024*1024) // 64MB buffer
	n := runtime.Stack(buf, true)
	buf = buf[:n]

	active := make(map[int64]bool)
	for _, line := range bytes.Split(buf, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("goroutine ")) {
			line = bytes.TrimPrefix(line, []byte("goroutine "))
			i := bytes.IndexByte(line, ' ')
			if i > 0 {
				if id, err := strconv.ParseInt(string(line[:i]), 10, 64); err == nil {
					active[id] = true
				}
			}
		}
	}

	// Remove dead goroutine contexts
	storage.Range(func(key, _ interface{}) bool {
		gid := key.(int64)
		if !active[gid] {
			storage.Delete(gid)
		}
		return true
	})
}
