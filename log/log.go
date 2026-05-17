// Package log provides structured JSON logging with trace context support.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/brightman-ai/kit/contextx"
)

// Level represents log level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Entry represents a log entry in JSON format.
// Schema: l/t/p/g/tid/span/pspan/stg/mod/ev/f/ec/msg/dur/event/duration_ms/session_id/flow_id/extra
type Entry struct {
	L          string                 `json:"l"`                    // 级别
	T          string                 `json:"t"`                    // 时间戳 YYYYMMDDHHmmss.SSS
	P          int                    `json:"p"`                    // 进程ID
	G          int64                  `json:"g"`                    // 协程ID
	TID        string                 `json:"tid,omitempty"`        // 业务流ID
	Span       string                 `json:"span,omitempty"`       // 当前 Span ID
	PSpan      string                 `json:"pspan,omitempty"`      // 父 Span ID
	STG        string                 `json:"stg,omitempty"`        // 阶段 (业务流优先)
	Mod        string                 `json:"mod,omitempty"`        // 模块
	EV         string                 `json:"ev,omitempty"`         // 事件上下文
	F          string                 `json:"f,omitempty"`          // 源码位置
	EC         int                    `json:"ec,omitempty"`         // 错误码
	Msg        string                 `json:"msg"`                  // 消息
	Dur        float64                `json:"dur,omitempty"`        // 耗时(毫秒)
	Event      string                 `json:"event,omitempty"`      // 事件类型 (stage_enter/exit, llm_call, memory_enrich)
	DurationMs float64                `json:"duration_ms,omitempty"` // 事件耗时(毫秒)
	SessionID  string                 `json:"session_id,omitempty"` // 会话ID
	FlowID     string                 `json:"flow_id,omitempty"`    // 流程ID
	Extra      map[string]interface{} `json:"extra,omitempty"`      // 额外字段 (event-specific)
}

// Logger provides structured logging.
type Logger struct {
	mod       string
	event     string
	errorCode int
	level     Level
	output    io.Writer
	mu        sync.Mutex
}

var (
	// globalLogger is the default logger
	globalLogger = &Logger{
		level:  LevelInfo,
		output: os.Stdout,
	}
	pid = os.Getpid()
)

// SetLevel sets the global log level.
func SetLevel(level Level) {
	globalLogger.mu.Lock()
	globalLogger.level = level
	globalLogger.mu.Unlock()
}

// SetOutput sets the global log output.
func SetOutput(w io.Writer) {
	globalLogger.mu.Lock()
	globalLogger.output = w
	globalLogger.mu.Unlock()
}

// Module returns a logger with specified module name.
func Module(name string) *Logger {
	return &Logger{
		mod:    name,
		level:  globalLogger.level,
		output: globalLogger.output,
	}
}

// WithModule returns a new logger with specified module name.
func (l *Logger) WithModule(mod string) *Logger {
	return &Logger{
		mod:       mod,
		event:     l.event,
		errorCode: l.errorCode,
		level:     l.level,
		output:    l.output,
	}
}

// WithEvent returns a new logger with event context (one-time).
func (l *Logger) WithEvent(ev string) *Logger {
	return &Logger{
		mod:       l.mod,
		event:     ev,
		errorCode: l.errorCode,
		level:     l.level,
		output:    l.output,
	}
}

// WithErrorCode returns a new logger with error code.
func (l *Logger) WithErrorCode(ec int) *Logger {
	return &Logger{
		mod:       l.mod,
		event:     l.event,
		errorCode: ec,
		level:     l.level,
		output:    l.output,
	}
}

// Debug logs at DEBUG level.
func (l *Logger) Debug(msg string, args ...any) {
	l.log(LevelDebug, msg, args...)
}

// Info logs at INFO level.
func (l *Logger) Info(msg string, args ...any) {
	l.log(LevelInfo, msg, args...)
}

// Warn logs at WARN level.
func (l *Logger) Warn(msg string, args ...any) {
	l.log(LevelWarn, msg, args...)
}

// Error logs at ERROR level.
func (l *Logger) Error(msg string, args ...any) {
	l.log(LevelError, msg, args...)
}

// log writes a log entry.
func (l *Logger) log(level Level, msg string, args ...any) {
	if level < l.level {
		return
	}

	// Format message with args if provided
	if len(args) > 0 {
		msg = formatMessage(msg, args...)
	}

	// Get context
	ctx := contextx.Current()

	entry := Entry{
		L:     level.String(),
		T:     formatTime(time.Now()),
		P:     pid,
		G:     contextx.GoroutineID(),
		TID:   ctx.TID,
		Span:  ctx.SpanID,
		PSpan: ctx.ParentSpan,
		STG:   ctx.Stage,
		Mod:   l.mod,
		Msg:   msg,
	}

	// Event context (one-time)
	if l.event != "" {
		entry.EV = l.event
	}

	// Source location for WARN/ERROR
	if level >= LevelWarn {
		entry.F = getCallerLocation(3) // skip getCallerLocation, log, Warn/Error
	}

	// Error code for ERROR
	if level == LevelError && l.errorCode != 0 {
		entry.EC = l.errorCode
	}

	// Format and write based on current format (ADR-029)
	l.mu.Lock()
	defer l.mu.Unlock()

	if currentFormat == FormatPretty && prettyFmt != nil {
		prettyFmt.Write(entry)
		return
	}

	// Default: JSON format
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log marshal error: %v\n", err)
		return
	}
	l.output.Write(data)
	l.output.Write([]byte("\n"))
}

// formatTime formats time as YYYYMMDDHHmmss.SSS
func formatTime(t time.Time) string {
	return t.Format("20060102150405.000")
}

// formatMessage formats message with key-value pairs or printf style.
// If message contains % and args provided, uses printf style.
// Otherwise, treats args as key-value pairs.
func formatMessage(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}

	// Check if message contains format verbs (printf style)
	hasFmtVerb := false
	for i := 0; i < len(msg)-1; i++ {
		if msg[i] == '%' && msg[i+1] != '%' {
			hasFmtVerb = true
			break
		}
	}

	if hasFmtVerb {
		return fmt.Sprintf(msg, args...)
	}

	// Key-value pairs
	result := msg
	for i := 0; i < len(args); i += 2 {
		key := fmt.Sprint(args[i])
		if i+1 < len(args) {
			val := args[i+1]
			result += fmt.Sprintf(", %s=%v", key, val)
		} else {
			result += fmt.Sprintf(", %s=<missing>", key)
		}
	}
	return result
}

// getCallerLocation returns "file:line" for the caller.
func getCallerLocation(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// Global logging functions

// Debug logs at DEBUG level.
func Debug(msg string, args ...any) {
	globalLogger.log(LevelDebug, msg, args...)
}

// Info logs at INFO level.
func Info(msg string, args ...any) {
	globalLogger.log(LevelInfo, msg, args...)
}

// Warn logs at WARN level.
func Warn(msg string, args ...any) {
	globalLogger.log(LevelWarn, msg, args...)
}

// Error logs at ERROR level.
func Error(msg string, args ...any) {
	globalLogger.log(LevelError, msg, args...)
}

// InfoStructured logs an INFO level structured event.
func InfoStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	globalLogger.InfoStructured(msg, event, durationMs, sessionID, flowID, extra)
}

// WarnStructured logs a WARN level structured event.
func WarnStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	globalLogger.WarnStructured(msg, event, durationMs, sessionID, flowID, extra)
}

// ErrorStructured logs an ERROR level structured event.
func ErrorStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	globalLogger.ErrorStructured(msg, event, durationMs, sessionID, flowID, extra)
}

// MaskAPIKey masks API key for logging (shows first 6 chars).
func MaskAPIKey(key string) string {
	if len(key) <= 6 {
		return "***"
	}
	return key[:6] + "***"
}

// MaskPassword masks password for logging.
func MaskPassword(_ string) string {
	return "***"
}

// MaskEmail masks email for logging (u***@example.com).
func MaskEmail(email string) string {
	at := -1
	for i, c := range email {
		if c == '@' {
			at = i
			break
		}
	}
	if at <= 0 {
		return "***"
	}
	return string(email[0]) + "***" + email[at:]
}

// MaskPath masks username in file path (/home/***/).
func MaskPath(path string) string {
	// Common patterns: /home/username/, /Users/username/, C:\Users\username\
	patterns := []string{"/home/", "/Users/", "\\Users\\"}
	for _, p := range patterns {
		idx := -1
		for i := 0; i <= len(path)-len(p); i++ {
			if path[i:i+len(p)] == p {
				idx = i
				break
			}
		}
		if idx >= 0 {
			start := idx + len(p)
			end := start
			for end < len(path) && path[end] != '/' && path[end] != '\\' {
				end++
			}
			if end > start {
				return path[:start] + "***" + path[end:]
			}
		}
	}
	return path
}

// MaskPhone masks phone number for logging.
func MaskPhone(phone string) string {
	if len(phone) < 7 {
		return "***"
	}
	return phone[:3] + "****" + phone[len(phone)-4:]
}

// ========== Convenience methods for log three elements ==========

// InfoEvent logs an event with category=event.
func (l *Logger) InfoEvent(msg string, args ...any) {
	l.WithEvent("event").Info(msg, args...)
}

// WarnPerf logs a performance warning with category=perf.
func (l *Logger) WarnPerf(msg string, args ...any) {
	l.WithEvent("perf").Warn(msg, args...)
}

// InfoState logs a state change with category=state.
func (l *Logger) InfoState(msg string, args ...any) {
	l.WithEvent("state").Info(msg, args...)
}

// InfoAudit logs an audit event with category=audit.
func (l *Logger) InfoAudit(msg string, args ...any) {
	l.WithEvent("audit").Info(msg, args...)
}

// LogStructured logs a structured event with flat fields for jq queryability.
// All numeric fields are preserved as JSON numbers (not strings).
func (l *Logger) LogStructured(level Level, msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	if level < l.level {
		return
	}

	ctx := contextx.Current()

	entry := Entry{
		L:          level.String(),
		T:          formatTime(time.Now()),
		P:          pid,
		G:          contextx.GoroutineID(),
		TID:        ctx.TID,
		Span:       ctx.SpanID,
		PSpan:      ctx.ParentSpan,
		STG:        ctx.Stage,
		Mod:        l.mod,
		Msg:        msg,
		Event:      event,
		DurationMs: durationMs,
		SessionID:  sessionID,
		FlowID:     flowID,
		Extra:      extra,
	}

	// Event context (one-time)
	if l.event != "" {
		entry.EV = l.event
	}

	// Error code for ERROR
	if level == LevelError && l.errorCode != 0 {
		entry.EC = l.errorCode
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log marshal error: %v\n", err)
		return
	}
	l.output.Write(data)
	l.output.Write([]byte("\n"))
}

// InfoStructured logs an INFO level structured event.
func (l *Logger) InfoStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	l.LogStructured(LevelInfo, msg, event, durationMs, sessionID, flowID, extra)
}

// WarnStructured logs a WARN level structured event.
func (l *Logger) WarnStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	l.LogStructured(LevelWarn, msg, event, durationMs, sessionID, flowID, extra)
}

// ErrorStructured logs an ERROR level structured event.
func (l *Logger) ErrorStructured(msg string, event string, durationMs float64, sessionID, flowID string, extra map[string]interface{}) {
	l.LogStructured(LevelError, msg, event, durationMs, sessionID, flowID, extra)
}
