package log

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPrettyFormatter_Format(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)
	f.SetColorEnabled(false) // Disable colors for testing

	entry := Entry{
		L:   "INFO",
		T:   "20260131120000.000",
		P:   1234,
		G:   1,
		Mod: "test",
		Msg: "test message",
	}

	output := f.Format(entry)

	if !strings.Contains(output, "INFO") {
		t.Errorf("output should contain INFO: %s", output)
	}
	if !strings.Contains(output, "[test]") {
		t.Errorf("output should contain module: %s", output)
	}
	if !strings.Contains(output, "test message") {
		t.Errorf("output should contain message: %s", output)
	}
}

func TestPrettyFormatter_ColorizeLevel(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)

	tests := []struct {
		level string
	}{
		{"ERROR"},
		{"WARN"},
		{"INFO"},
		{"DEBUG"},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			// With colors
			f.SetColorEnabled(true)
			colored := f.colorizeLevel(tt.level)
			if !strings.Contains(colored, tt.level) {
				t.Errorf("colorized output should contain level: %s", colored)
			}
			// Has ANSI codes
			if !strings.Contains(colored, "\033[") {
				t.Errorf("colorized output should contain ANSI codes: %s", colored)
			}

			// Without colors
			f.SetColorEnabled(false)
			plain := f.colorizeLevel(tt.level)
			if strings.Contains(plain, "\033[") {
				t.Errorf("plain output should not contain ANSI codes: %s", plain)
			}
		})
	}
}

func TestPrettyFormatter_SlowThreshold(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)
	f.SetColorEnabled(false)
	f.SetSlowThreshold(1 * time.Second)

	// Fast operation
	entry := Entry{
		L:   "INFO",
		T:   "20260131120000.000",
		Msg: "fast",
		Dur: 100, // 100ms
	}
	output := f.Format(entry)
	if strings.Contains(output, "⚠️") {
		t.Errorf("fast operation should not have warning: %s", output)
	}

	// Slow operation
	entry.Dur = 5000 // 5s > 1s threshold
	output = f.Format(entry)
	if !strings.Contains(output, "⚠️") {
		t.Errorf("slow operation should have warning: %s", output)
	}
}

func TestPrettyFormatter_TraceContext(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)
	f.SetColorEnabled(false)

	entry := Entry{
		L:    "INFO",
		T:    "20260131120000.000",
		Msg:  "with trace",
		TID:  "req-123",
		Span: "span-456",
	}

	output := f.Format(entry)

	if !strings.Contains(output, "├─ tid: req-123") {
		t.Errorf("output should contain tid: %s", output)
	}
	if !strings.Contains(output, "└─ span: span-456") {
		t.Errorf("output should contain span: %s", output)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   float64
		want string
	}{
		{0.5, "0.50ms"},
		{100, "100ms"},
		{1500, "1.5s"},
		{90000, "1.5m"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("formatDuration(%f) = %s, want %s", tt.ms, got, tt.want)
		}
	}
}

func TestSetFormat(t *testing.T) {
	// Save original
	original := currentFormat

	t.Run("set JSON", func(t *testing.T) {
		SetFormat(FormatJSON)
		if GetFormat() != FormatJSON {
			t.Errorf("format = %s, want json", GetFormat())
		}
	})

	t.Run("set Pretty", func(t *testing.T) {
		SetFormat(FormatPretty)
		if GetFormat() != FormatPretty {
			t.Errorf("format = %s, want pretty", GetFormat())
		}
		if prettyFmt == nil {
			t.Error("prettyFmt should be initialized")
		}
	})

	// Restore
	SetFormat(original)
}

func TestInitFromEnv(t *testing.T) {
	original := os.Getenv("LOG_FORMAT")
	defer os.Setenv("LOG_FORMAT", original)

	tests := []struct {
		envVal string
		want   Format
	}{
		{"json", FormatJSON},
		{"JSON", FormatJSON},
		{"pretty", FormatPretty},
		{"PRETTY", FormatPretty},
		{"dev", FormatPretty},
		{"human", FormatPretty},
		{"", FormatJSON},
		{"unknown", FormatJSON},
	}

	for _, tt := range tests {
		t.Run(tt.envVal, func(t *testing.T) {
			os.Setenv("LOG_FORMAT", tt.envVal)
			InitFromEnv()
			if GetFormat() != tt.want {
				t.Errorf("LOG_FORMAT=%s: format = %s, want %s", tt.envVal, GetFormat(), tt.want)
			}
		})
	}

	// Restore to JSON for other tests
	SetFormat(FormatJSON)
}

func TestPrettyFormatter_ErrorCode(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)
	f.SetColorEnabled(false)

	entry := Entry{
		L:   "ERROR",
		T:   "20260131120000.000",
		Msg: "error occurred",
		EC:  1001,
	}

	output := f.Format(entry)
	if !strings.Contains(output, "[EC:1001]") {
		t.Errorf("output should contain error code: %s", output)
	}
}

func TestPrettyFormatter_SourceFile(t *testing.T) {
	var buf bytes.Buffer
	f := NewPrettyFormatter(&buf)
	f.SetColorEnabled(false)

	entry := Entry{
		L:   "ERROR",
		T:   "20260131120000.000",
		Msg: "error",
		F:   "main.go:42",
	}

	output := f.Format(entry)
	if !strings.Contains(output, "@main.go:42") {
		t.Errorf("output should contain source file: %s", output)
	}
}
