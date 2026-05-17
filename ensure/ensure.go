// Package ensure provides assertion utilities for development.
// Assertions are used to catch programming errors early.
//
// Deep-Ensure: 早暴露 + 契约化 + 零容忍 + 性能友好
package ensure

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/brightman-ai/kit/contextx"
)

// OnFailed is called when an assertion fails.
// Can be overridden for testing or custom behavior.
var OnFailed = func(msg string) {
	panic(msg)
}

// debugMode controls whether TrueDbg assertions are active.
var debugMode = false

// SetDebugMode enables or disables debug-only assertions.
func SetDebugMode(enabled bool) {
	debugMode = enabled
}

// True asserts that condition is true.
// If false, panics with formatted message.
//
// Usage:
//
//	ensure.True(len(items) > 0, "items must not be empty")
//	ensure.True(user != nil, "user %s not found", userID)
func True(condition bool, format string, args ...any) {
	if !condition {
		fail(format, args...)
	}
}

// NotNil asserts that obj is not nil.
// If nil, panics with formatted message.
//
// Usage:
//
//	ensure.NotNil(db, "database connection required")
func NotNil(obj any, format string, args ...any) {
	if obj == nil {
		fail(format, args...)
	}
}

// TrueDbg asserts that condition is true, but only in debug mode.
// In release mode, this is a no-op.
//
// Usage:
//
//	ensure.TrueDbg(index >= 0 && index < len(arr), "index out of bounds: %d", index)
func TrueDbg(condition bool, format string, args ...any) {
	if debugMode && !condition {
		fail(format, args...)
	}
}

// ========== Specialized versions for performance-sensitive paths ==========
// These avoid interface{} boxing overhead.

// True1 is a single-argument specialized version of True.
func True1[T1 any](condition bool, format string, arg1 T1) {
	if !condition {
		fail(format, arg1)
	}
}

// True2 is a two-argument specialized version of True.
func True2[T1, T2 any](condition bool, format string, arg1 T1, arg2 T2) {
	if !condition {
		fail(format, arg1, arg2)
	}
}

// True3 is a three-argument specialized version of True.
func True3[T1, T2, T3 any](condition bool, format string, arg1 T1, arg2 T2, arg3 T3) {
	if !condition {
		fail(format, arg1, arg2, arg3)
	}
}

// NotNil1 is a single-argument specialized version of NotNil.
func NotNil1[T1 any](obj any, format string, arg1 T1) {
	if obj == nil {
		fail(format, arg1)
	}
}

// NotNil2 is a two-argument specialized version of NotNil.
func NotNil2[T1, T2 any](obj any, format string, arg1 T1, arg2 T2) {
	if obj == nil {
		fail(format, arg1, arg2)
	}
}

// ========== Debug specialized versions ==========

// True1Dbg is a single-argument specialized debug version.
func True1Dbg[T1 any](condition bool, format string, arg1 T1) {
	if debugMode && !condition {
		fail(format, arg1)
	}
}

// True2Dbg is a two-argument specialized debug version.
func True2Dbg[T1, T2 any](condition bool, format string, arg1 T1, arg2 T2) {
	if debugMode && !condition {
		fail(format, arg1, arg2)
	}
}

// fail triggers assertion failure.
func fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	location := getLocation(3) // skip fail, True/NotNil, caller

	// Include trace context for debugging
	ctx := contextx.Current()
	ctxInfo := ""
	if ctx.TID != "" || ctx.SpanID != "" || ctx.Stage != "" {
		ctxInfo = fmt.Sprintf(" [tid=%s span=%s stg=%s gid=%d]",
			ctx.TID, ctx.SpanID, ctx.Stage, contextx.GoroutineID())
	}

	fullMsg := fmt.Sprintf("ASSERTION FAILED at %s%s: %s\n%s", location, ctxInfo, msg, debug.Stack())
	OnFailed(fullMsg)
}

// getLocation returns "file:line" for the caller.
func getLocation(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown:0"
	}
	// Get just filename, not full path
	short := file
	for i := len(file) - 1; i > 0; i-- {
		if file[i] == '/' {
			short = file[i+1:]
			break
		}
	}
	return fmt.Sprintf("%s:%d", short, line)
}

// ========== Error context helpers ==========

// Unreachable marks code that should never be reached.
// Always panics.
//
// Usage:
//
//	switch status {
//	case Running:
//	    // ...
//	default:
//	    ensure.Unreachable("unexpected status: %d", status)
//	}
func Unreachable(format string, args ...any) {
	fail("unreachable code: "+format, args...)
}

// TODO marks incomplete implementation.
// Panics with a message about unimplemented functionality.
//
// Usage:
//
//	func NewFeature() {
//	    ensure.TODO("NewFeature not implemented yet")
//	}
func TODO(format string, args ...any) {
	fail("TODO: "+format, args...)
}

// NoError asserts that err is nil.
// If not nil, panics with error details.
//
// Usage:
//
//	ensure.NoError(json.Unmarshal(data, &obj), "failed to parse config")
func NoError(err error, format string, args ...any) {
	if err != nil {
		msg := fmt.Sprintf(format, args...)
		fail("%s: %v", msg, err)
	}
}

// NoError1 is a single-argument specialized version of NoError.
func NoError1[T1 any](err error, format string, arg1 T1) {
	if err != nil {
		msg := fmt.Sprintf(format, arg1)
		fail("%s: %v", msg, err)
	}
}

// NoError2 is a two-argument specialized version of NoError.
func NoError2[T1, T2 any](err error, format string, arg1 T1, arg2 T2) {
	if err != nil {
		msg := fmt.Sprintf(format, arg1, arg2)
		fail("%s: %v", msg, err)
	}
}
