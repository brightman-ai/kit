package obs

import "fmt"

// OnFailed is the failure handler for contract assertions.
// Default: panic. Override in tests to collect failures without crashing.
//
//	obs.OnFailed = func(msg string) { t.Fatal(msg) }
var OnFailed = func(msg string) {
	panic(msg)
}

// True asserts a condition. Panics with formatted message if false.
// Use for internal invariants, never for external input validation.
func True(condition bool, format string, args ...any) {
	if !condition {
		OnFailed(fmt.Sprintf(format, args...))
	}
}

// NotNil asserts that v is not nil. Panics with descriptive message.
// Primary use: ValidateDeps in observe.go files.
func NotNil(v any, name string) {
	if v == nil {
		OnFailed(fmt.Sprintf("%s must not be nil", name))
	}
}

// NoError asserts that err is nil. Panics with formatted message + error.
// Use when an error indicates a bug, not an expected failure.
func NoError(err error, format string, args ...any) {
	if err != nil {
		OnFailed(fmt.Sprintf(format+": %v", append(args, err)...))
	}
}

// Unreachable marks code paths that should never execute.
// If reached, it indicates a logic error.
func Unreachable(format string, args ...any) {
	OnFailed("unreachable: " + fmt.Sprintf(format, args...))
}

// True1 is a generic specialization of True for single-arg formatting.
// Avoids interface{} boxing overhead on hot paths.
func True1[T1 any](condition bool, format string, arg1 T1) {
	if !condition {
		OnFailed(fmt.Sprintf(format, arg1))
	}
}

// True2 is a generic specialization of True for two-arg formatting.
func True2[T1, T2 any](condition bool, format string, arg1 T1, arg2 T2) {
	if !condition {
		OnFailed(fmt.Sprintf(format, arg1, arg2))
	}
}
