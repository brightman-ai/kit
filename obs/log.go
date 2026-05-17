package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Level represents log severity.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

var levelNames = [...]string{"DEBUG", "INFO", "WARN", "ERROR"}

func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("LEVEL(%d)", l)
}

// Logger is a module-scoped logger. Create with Module().
// All log methods require context.Context to extract TID/STG.
type Logger struct {
	mod string
}

// Module creates a Logger for the named module.
// Convention: mod must match the package directory name.
func Module(mod string) Logger {
	return Logger{mod: mod}
}

// Entry is the fixed-schema log record. 7 core fields + ext map.
// Reserved keys (l/t/tid/stg/mod/msg/f) must not appear in Ext.
type Entry struct {
	L   string         `json:"l"`
	T   string         `json:"t"`
	TID string         `json:"tid,omitempty"`
	STG string         `json:"stg,omitempty"`
	Mod string         `json:"mod"`
	Msg string         `json:"msg"`
	F   string         `json:"f,omitempty"`
	Ext map[string]any `json:"ext,omitempty"`
}

// --- Ext sanitization (security parity with frontend obs.ts) ---

// sensitiveKeyRE redacts keys that may contain secrets.
// Matches: authorization, cookie, token, secret, password, passwd, api_key, session_id, etc.
var sensitiveKeyRE = regexp.MustCompile(`(?i)(authorization|cookie|token|secret|password|passwd|api[-_]?key|session[-_]?id)`)

const (
	maxExtKeys   = 20
	maxStringLen = 256
	maxExtDepth  = 2
)

func sanitizeExt(ext map[string]any) map[string]any {
	if len(ext) == 0 {
		return nil
	}
	// Sort keys for deterministic truncation when exceeding maxExtKeys
	keys := make([]string, 0, len(ext))
	for k := range ext {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]any, min(len(ext), maxExtKeys))
	for i, k := range keys {
		if i >= maxExtKeys {
			break
		}
		if sensitiveKeyRE.MatchString(k) {
			out[k] = "[redacted]"
		} else {
			out[k] = sanitizeVal(ext[k], 0)
		}
	}
	return out
}

func sanitizeVal(v any, depth int) any {
	if v == nil {
		return nil
	}
	if depth > maxExtDepth {
		return "[truncated]"
	}
	switch val := v.(type) {
	case string:
		if len(val) > maxStringLen {
			return val[:maxStringLen] + "..."
		}
		return val
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return val
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, min(len(val), maxExtKeys))
		for i, k := range keys {
			if i >= maxExtKeys {
				break
			}
			if sensitiveKeyRE.MatchString(k) {
				out[k] = "[redacted]"
			} else {
				out[k] = sanitizeVal(val[k], depth+1)
			}
		}
		return out
	case []any:
		n := len(val)
		if n > maxExtKeys {
			n = maxExtKeys
		}
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = sanitizeVal(val[i], depth+1)
		}
		return out
	case error:
		s := val.Error()
		if len(s) > maxStringLen {
			return s[:maxStringLen] + "..."
		}
		return s
	default:
		// JSON roundtrip: preserves structure for []string, map[string]string,
		// structs, time.Time, etc. — maintains cross-language parity with TS.
		if data, err := json.Marshal(val); err == nil {
			var safe any
			if json.Unmarshal(data, &safe) == nil {
				return sanitizeVal(safe, depth)
			}
		}
		s := fmt.Sprintf("%v", val)
		if len(s) > maxStringLen {
			return s[:maxStringLen] + "..."
		}
		return s
	}
}

// --- Logger methods ---

// Info logs at INFO level. Use for key success milestones and deliberate skip events.
func (l Logger) Info(ctx context.Context, msg string, args ...any) {
	l.emit(ctx, INFO, msg, args)
}

// Warn logs at WARN level with automatic file:line capture.
// Use for degraded-but-not-crashed scenarios.
func (l Logger) Warn(ctx context.Context, msg string, args ...any) {
	l.emit(ctx, WARN, msg, args)
}

// Error logs at ERROR level with automatic file:line capture.
// Use for failures that break core functionality.
func (l Logger) Error(ctx context.Context, msg string, args ...any) {
	l.emit(ctx, ERROR, msg, args)
}

// Debug logs at DEBUG level. Use for non-error branch diagnostics.
func (l Logger) Debug(ctx context.Context, msg string, args ...any) {
	l.emit(ctx, DEBUG, msg, args)
}

func (l Logger) emit(ctx context.Context, level Level, msg string, args []any) {
	if !l.enabled(level) {
		return
	}

	e := Entry{
		L:   level.String(),
		T:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		TID: Trace(ctx),
		STG: Stage(ctx),
		Mod: l.mod,
		Msg: msg,
	}

	// WARN/ERROR: auto-capture basename:line (no full path exposure)
	if level >= WARN {
		if _, file, line, ok := runtime.Caller(2); ok {
			for i := len(file) - 1; i >= 0; i-- {
				if file[i] == '/' {
					file = file[i+1:]
					break
				}
			}
			e.F = fmt.Sprintf("%s:%d", file, line)
		}
	}

	// key-value args → Ext map (with sanitization)
	// Malformed args (odd count, non-string key) are captured, never silently lost.
	if len(args) > 0 {
		raw := make(map[string]any, (len(args)+1)/2)
		for i := 0; i < len(args); i += 2 {
			key, ok := args[i].(string)
			if !ok {
				key = "!BADKEY"
			}
			if i+1 < len(args) {
				raw[key] = args[i+1]
			} else {
				raw[key] = "!MISSING"
			}
		}
		e.Ext = sanitizeExt(raw)
	}

	// 所有 INFO 及以上级别写入全局 ring buffer，供 /api/debug/obs/recent 查询
	if level >= INFO {
		globalRing.Push(e)
	}

	data, err := json.Marshal(e)
	if err != nil {
		// Fallback: preserve core fields, drop ext that caused the failure
		e.Ext = nil
		e.Msg = msg + " [obs: ext marshal failed]"
		if fb, err2 := json.Marshal(e); err2 == nil {
			logMu.Lock()
			_, _ = output.Write(append(fb, '\n'))
			logMu.Unlock()
		}
		return
	}

	logMu.Lock()
	_, _ = output.Write(append(data, '\n'))
	logMu.Unlock()
}

func (l Logger) enabled(level Level) bool {
	moduleLevelsMu.RLock()
	if ml, ok := moduleLevels[l.mod]; ok {
		moduleLevelsMu.RUnlock()
		return level >= ml
	}
	moduleLevelsMu.RUnlock()
	return level >= Level(globalLvl.Load())
}

// --- global log state ---

var (
	// globalLvl stores Level as int32; thread-safe via atomic.
	// Default: INFO (set in init).
	globalLvl atomic.Int32

	output io.Writer = os.Stderr
	logMu  sync.Mutex

	moduleLevels   = make(map[string]Level)
	moduleLevelsMu sync.RWMutex
)

func init() {
	globalLvl.Store(int32(INFO))
}

// SetLevel sets the global minimum log level. Thread-safe.
func SetLevel(l Level) {
	globalLvl.Store(int32(l))
}

// SetOutput sets the log output destination.
func SetOutput(w io.Writer) {
	logMu.Lock()
	output = w
	logMu.Unlock()
}

// SetModuleLevel sets a module-specific level that overrides the global level.
func SetModuleLevel(mod string, l Level) {
	moduleLevelsMu.Lock()
	moduleLevels[mod] = l
	moduleLevelsMu.Unlock()
}
