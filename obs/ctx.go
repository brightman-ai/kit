// Package obs provides observability primitives for the deepwork application.
//
// Design: T5-TS-OBS-DESIGN.md r2 | CAP-TSOBS-C1 ContextPropagation
// Philosophy: Single-path context.Context propagation. No goroutine-local. No middleware.
package obs

import "context"

// unexported keys prevent external package conflicts
type tidKey struct{}
type stgKey struct{}

// WithTrace injects a Trace ID into the context.
// TID identifies an execution context (not a business entity).
// Call once at trace boundary: HTTP handler entry, CLI command start, goroutine spawn with new trace.
func WithTrace(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, tidKey{}, tid)
}

// WithStage injects a business stage coordinate into the context.
// STG format: "{subsystem}/{phase}", e.g. "council/round/scatter".
// Call when entering a new business phase. Immutable per ctx — creates new ctx.
func WithStage(ctx context.Context, stg string) context.Context {
	return context.WithValue(ctx, stgKey{}, stg)
}

// Trace reads the TID from context. Returns "" if not set.
func Trace(ctx context.Context) string {
	if v, ok := ctx.Value(tidKey{}).(string); ok {
		return v
	}
	return ""
}

// Stage reads the STG from context. Returns "" if not set.
func Stage(ctx context.Context) string {
	if v, ok := ctx.Value(stgKey{}).(string); ok {
		return v
	}
	return ""
}

// DropCancel preserves all ctx values (TID/STG) while disconnecting from
// the parent's cancel/deadline signal. Use at lifecycle boundaries where
// a child operation must outlive the parent request.
//
// Resource ownership discipline: DropCancel only preserves obs metadata.
// After calling DropCancel, you MUST NOT reuse parent-owned request-scoped
// resources (DB tx, HTTP streams, deadlines). Independently acquire what you need:
//
//	baseCtx := obs.DropCancel(ctx)
//	childCtx, cancel := context.WithTimeout(baseCtx, ownTimeout)
//	defer cancel()
//
// Requires Go 1.21+.
func DropCancel(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// Go launches a goroutine with ctx propagation. This is a spawn boundary guard:
// it ensures the goroutine receives the context (forgetting ctx = compile error).
//
// Zero recover: panics in fn are fatal (DDC-I-14). Business layer may add
// defer/recover explicitly outside obs.Go if isolation is needed, but obs
// does not make that choice for you.
func Go(ctx context.Context, fn func(context.Context)) {
	go func() {
		fn(ctx)
	}()
}
