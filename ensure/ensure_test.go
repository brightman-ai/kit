package ensure

import (
	"strings"
	"testing"
)

func TestTrue_Pass(t *testing.T) {
	// Should not panic
	True(true, "should pass")
	True(1 == 1, "math works")
	True(len("hello") > 0, "string not empty")
}

func TestTrue_Fail(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True(false, "expected failure: %s", "test")

	if !strings.Contains(panicMsg, "ASSERTION FAILED") {
		t.Errorf("expected 'ASSERTION FAILED', got %q", panicMsg)
	}
	if !strings.Contains(panicMsg, "expected failure: test") {
		t.Errorf("expected message in panic, got %q", panicMsg)
	}
}

func TestNotNil_Pass(t *testing.T) {
	obj := &struct{}{}
	NotNil(obj, "should pass")

	str := "hello"
	NotNil(str, "string should pass")
}

func TestNotNil_Fail(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	NotNil(nil, "object is nil")

	if !strings.Contains(panicMsg, "object is nil") {
		t.Errorf("expected message in panic, got %q", panicMsg)
	}
}

func TestTrueDbg_DebugEnabled(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
		SetDebugMode(false)
	}()

	SetDebugMode(true)
	TrueDbg(false, "debug assertion")

	if !strings.Contains(panicMsg, "debug assertion") {
		t.Errorf("expected debug assertion to trigger, got %q", panicMsg)
	}
}

func TestTrueDbg_DebugDisabled(t *testing.T) {
	var called bool
	OnFailed = func(msg string) {
		called = true
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	SetDebugMode(false)
	TrueDbg(false, "should not trigger")

	if called {
		t.Error("TrueDbg should not trigger when debug mode is disabled")
	}
}

func TestTrue1(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True1(false, "value is %d", 42)

	if !strings.Contains(panicMsg, "value is 42") {
		t.Errorf("expected formatted message, got %q", panicMsg)
	}
}

func TestTrue2(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True2(false, "event %s missing field %s", "ProcessCreate", "timestamp")

	if !strings.Contains(panicMsg, "event ProcessCreate missing field timestamp") {
		t.Errorf("expected formatted message, got %q", panicMsg)
	}
}

func TestTrue3(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True3(false, "op=%s id=%d status=%s", "delete", 123, "failed")

	if !strings.Contains(panicMsg, "op=delete id=123 status=failed") {
		t.Errorf("expected formatted message, got %q", panicMsg)
	}
}

func TestNotNil1(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	NotNil1[string](nil, "missing %s", "config")

	if !strings.Contains(panicMsg, "missing config") {
		t.Errorf("expected formatted message, got %q", panicMsg)
	}
}

func TestNotNil2(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	NotNil2[string, int](nil, "%s with id %d not found", "user", 42)

	if !strings.Contains(panicMsg, "user with id 42 not found") {
		t.Errorf("expected formatted message, got %q", panicMsg)
	}
}

func TestUnreachable(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	Unreachable("unknown status: %d", 99)

	if !strings.Contains(panicMsg, "unreachable code") {
		t.Errorf("expected 'unreachable code', got %q", panicMsg)
	}
	if !strings.Contains(panicMsg, "unknown status: 99") {
		t.Errorf("expected status message, got %q", panicMsg)
	}
}

func TestTODO(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	TODO("feature X not implemented")

	if !strings.Contains(panicMsg, "TODO") {
		t.Errorf("expected 'TODO', got %q", panicMsg)
	}
	if !strings.Contains(panicMsg, "feature X not implemented") {
		t.Errorf("expected message, got %q", panicMsg)
	}
}

func TestFailIncludesStackTrace(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True(false, "test failure")

	if !strings.Contains(panicMsg, "goroutine") {
		t.Errorf("expected stack trace with 'goroutine', got %q", panicMsg)
	}
}

func TestFailIncludesLocation(t *testing.T) {
	var panicMsg string
	OnFailed = func(msg string) {
		panicMsg = msg
	}
	defer func() {
		OnFailed = func(msg string) { panic(msg) }
	}()

	True(false, "location test")

	if !strings.Contains(panicMsg, "ensure_test.go:") {
		t.Errorf("expected file location, got %q", panicMsg)
	}
}

// Benchmarks

func BenchmarkTrue_Pass(b *testing.B) {
	for i := 0; i < b.N; i++ {
		True(true, "should pass")
	}
}

func BenchmarkTrue1_Pass(b *testing.B) {
	for i := 0; i < b.N; i++ {
		True1(true, "value is %d", 42)
	}
}

func BenchmarkTrueDbg_Disabled(b *testing.B) {
	SetDebugMode(false)
	for i := 0; i < b.N; i++ {
		TrueDbg(false, "should be no-op")
	}
}

func BenchmarkTrueDbg_Enabled_Pass(b *testing.B) {
	SetDebugMode(true)
	defer SetDebugMode(false)
	for i := 0; i < b.N; i++ {
		TrueDbg(true, "should pass")
	}
}
