package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brightman-ai/kit/contextx"
)

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestLoggerBasic(t *testing.T) {
	var buf bytes.Buffer
	logger := Module("test")
	logger.output = &buf
	logger.level = LevelDebug

	logger.Info("hello world")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.L != "INFO" {
		t.Errorf("level = %q, want INFO", entry.L)
	}
	if entry.Mod != "test" {
		t.Errorf("mod = %q, want test", entry.Mod)
	}
	if entry.Msg != "hello world" {
		t.Errorf("msg = %q, want 'hello world'", entry.Msg)
	}
	if entry.P == 0 {
		t.Error("pid should not be 0")
	}
	if entry.G == 0 {
		t.Error("goroutine ID should not be 0")
	}
	if entry.T == "" {
		t.Error("timestamp should not be empty")
	}
}

func TestLoggerWithContext(t *testing.T) {
	defer contextx.Clear()
	contextx.SetTID("test/req-001")
	contextx.SetStage("http/handler")

	var buf bytes.Buffer
	logger := Module("mymodule")
	logger.output = &buf
	logger.level = LevelDebug

	logger.Info("processing request")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.TID != "test/req-001" {
		t.Errorf("tid = %q, want 'test/req-001'", entry.TID)
	}
	if entry.STG != "http/handler" {
		t.Errorf("stg = %q, want 'http/handler'", entry.STG)
	}
}

func TestLoggerWarnError(t *testing.T) {
	var buf bytes.Buffer
	logger := Module("test")
	logger.output = &buf
	logger.level = LevelDebug

	// WARN should include file location
	logger.Warn("something wrong")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.L != "WARN" {
		t.Errorf("level = %q, want WARN", entry.L)
	}
	if entry.F == "" {
		t.Error("WARN should include file location")
	}
	if !strings.Contains(entry.F, "log_test.go:") {
		t.Errorf("file location = %q, should contain 'log_test.go:'", entry.F)
	}
}

func TestLoggerErrorCode(t *testing.T) {
	var buf bytes.Buffer
	logger := Module("test").WithErrorCode(5001)
	logger.output = &buf
	logger.level = LevelDebug

	logger.Error("request failed")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.EC != 5001 {
		t.Errorf("ec = %d, want 5001", entry.EC)
	}
}

func TestLoggerWithEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := Module("test").WithEvent("user_login")
	logger.output = &buf
	logger.level = LevelDebug

	logger.Info("user logged in")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if entry.EV != "user_login" {
		t.Errorf("ev = %q, want 'user_login'", entry.EV)
	}
}

func TestLoggerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	logger := Module("test")
	logger.output = &buf
	logger.level = LevelWarn // Only WARN and above

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	output := buf.String()
	if strings.Contains(output, "debug message") {
		t.Error("DEBUG should be filtered")
	}
	if strings.Contains(output, "info message") {
		t.Error("INFO should be filtered")
	}
	if !strings.Contains(output, "warn message") {
		t.Error("WARN should be included")
	}
}

func TestFormatMessageKeyValue(t *testing.T) {
	// Test key-value formatting (even number of args)
	// Use intermediate variable to avoid vet warning
	base := "operation completed"
	args := []any{"count", 10, "duration_ms", 150}
	msg := formatMessage(base, args...)
	if !strings.Contains(msg, "count=10") {
		t.Errorf("msg = %q, should contain 'count=10'", msg)
	}
	if !strings.Contains(msg, "duration_ms=150") {
		t.Errorf("msg = %q, should contain 'duration_ms=150'", msg)
	}
}

func TestFormatMessagePrintf(t *testing.T) {
	msg := formatMessage("error: %s, code: %d", "not found", 404)
	if msg != "error: not found, code: 404" {
		t.Errorf("msg = %q, want 'error: not found, code: 404'", msg)
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-abc123xyz789", "sk-abc***"},
		{"short", "***"},
		{"123456", "***"},
		{"1234567", "123456***"},
	}

	for _, tt := range tests {
		if got := MaskAPIKey(tt.input); got != tt.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"13812345678", "138****5678"},
		{"12345", "***"},
	}

	for _, tt := range tests {
		if got := MaskPhone(tt.input); got != tt.want {
			t.Errorf("MaskPhone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGlobalFunctions(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel(LevelDebug)
	defer func() {
		SetOutput(nil)
		SetLevel(LevelInfo)
	}()

	Info("global info")

	output := buf.String()
	if !strings.Contains(output, `"l":"INFO"`) {
		t.Errorf("output = %q, should contain INFO level", output)
	}
	if !strings.Contains(output, "global info") {
		t.Errorf("output = %q, should contain message", output)
	}
}

func BenchmarkLog(b *testing.B) {
	var buf bytes.Buffer
	logger := Module("bench")
	logger.output = &buf
	logger.level = LevelInfo

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "iteration", i)
		buf.Reset()
	}
}
