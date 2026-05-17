// Package log provides structured JSON logging with trace context support.
// formatter.go: Pretty format output for development (ADR-029).
package log

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Format represents log output format.
type Format string

const (
	// FormatJSON outputs structured JSON (default, production).
	FormatJSON Format = "json"
	// FormatPretty outputs human-readable colored text (development).
	FormatPretty Format = "pretty"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorGray   = "\033[90m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// PrettyFormatter formats log entries for human readability (ADR-029).
type PrettyFormatter struct {
	output        io.Writer
	colorEnabled  bool
	slowThreshold time.Duration // Highlight slow operations (default 10s)
}

// NewPrettyFormatter creates a new PrettyFormatter.
func NewPrettyFormatter(output io.Writer) *PrettyFormatter {
	return &PrettyFormatter{
		output:        output,
		colorEnabled:  isTerminal(output),
		slowThreshold: 10 * time.Second,
	}
}

// SetColorEnabled enables or disables color output.
func (f *PrettyFormatter) SetColorEnabled(enabled bool) {
	f.colorEnabled = enabled
}

// SetSlowThreshold sets the threshold for slow operation highlighting.
func (f *PrettyFormatter) SetSlowThreshold(d time.Duration) {
	f.slowThreshold = d
}

// Format formats a log entry in pretty format.
func (f *PrettyFormatter) Format(entry Entry) string {
	var b strings.Builder

	// Time (short format)
	ts, _ := time.Parse("20060102150405.000", entry.T)
	timeStr := ts.Format("15:04:05.000")

	// Level with color
	levelStr := f.colorizeLevel(entry.L)

	// Module/Stage indicator
	moduleStr := ""
	if entry.Mod != "" {
		moduleStr = f.colorize(colorCyan, "["+entry.Mod+"]")
	}
	if entry.STG != "" {
		moduleStr += f.colorize(colorGray, " "+entry.STG)
	}

	// Main line
	b.WriteString(fmt.Sprintf("%s %s%s %s",
		f.colorize(colorGray, timeStr),
		levelStr,
		moduleStr,
		entry.Msg,
	))

	// Source file for WARN/ERROR
	if entry.F != "" {
		b.WriteString(f.colorize(colorGray, " @"+entry.F))
	}

	// Duration highlighting
	if entry.Dur > 0 {
		durStr := formatDuration(entry.Dur)
		if entry.Dur > float64(f.slowThreshold.Milliseconds()) {
			b.WriteString(f.colorize(colorYellow, " ⚠️ "+durStr))
		} else {
			b.WriteString(f.colorize(colorGray, " ("+durStr+")"))
		}
	}

	// Error code
	if entry.EC != 0 {
		b.WriteString(f.colorize(colorRed, fmt.Sprintf(" [EC:%d]", entry.EC)))
	}

	// Trace context (tree structure)
	if entry.TID != "" {
		b.WriteString("\n")
		b.WriteString(f.colorize(colorGray, "    ├─ tid: "+entry.TID))
		if entry.Span != "" {
			b.WriteString("\n")
			b.WriteString(f.colorize(colorGray, "    └─ span: "+entry.Span))
		}
	}

	b.WriteString("\n")
	return b.String()
}

// Write writes a formatted entry to the output.
func (f *PrettyFormatter) Write(entry Entry) {
	output := f.Format(entry)
	f.output.Write([]byte(output))
}

// colorizeLevel returns level string with appropriate color.
func (f *PrettyFormatter) colorizeLevel(level string) string {
	if !f.colorEnabled {
		return fmt.Sprintf("[%-5s]", level)
	}

	var color string
	switch level {
	case "ERROR":
		color = colorRed + colorBold
	case "WARN":
		color = colorYellow
	case "INFO":
		color = colorBlue
	case "DEBUG":
		color = colorGray
	default:
		color = colorReset
	}

	return color + fmt.Sprintf("[%-5s]", level) + colorReset
}

// colorize wraps text with ANSI color codes.
func (f *PrettyFormatter) colorize(color, text string) string {
	if !f.colorEnabled {
		return text
	}
	return color + text + colorReset
}

// formatDuration formats duration in human-readable form.
func formatDuration(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.2fms", ms)
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.1fm", ms/60000)
}

// isTerminal checks if output is a terminal (supports colors).
func isTerminal(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		stat, err := f.Stat()
		if err != nil {
			return false
		}
		return (stat.Mode() & os.ModeCharDevice) != 0
	}
	return false
}

// Global format setting
var (
	currentFormat Format = FormatJSON
	prettyFmt     *PrettyFormatter
)

// SetFormat sets the global log format.
// Called once at startup based on LOG_FORMAT environment variable.
func SetFormat(format Format) {
	currentFormat = format
	if format == FormatPretty {
		prettyFmt = NewPrettyFormatter(os.Stdout)
	}
}

// GetFormat returns the current log format.
func GetFormat() Format {
	return currentFormat
}

// InitFromEnv initializes log format from LOG_FORMAT environment variable (ADR-029).
func InitFromEnv() {
	format := os.Getenv("LOG_FORMAT")
	switch strings.ToLower(format) {
	case "pretty", "dev", "human":
		SetFormat(FormatPretty)
		SetLevel(LevelDebug) // Pretty mode typically wants debug
	case "json", "prod", "":
		SetFormat(FormatJSON)
	default:
		SetFormat(FormatJSON)
	}
}

// FormatEntry formats an entry based on current format setting.
func FormatEntry(entry Entry) []byte {
	if currentFormat == FormatPretty && prettyFmt != nil {
		return []byte(prettyFmt.Format(entry))
	}
	// Default: JSON
	return nil // Let caller handle JSON
}
