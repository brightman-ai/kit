package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

// --- ctx.go tests ---

func TestWithTraceAndRead(t *testing.T) {
	ctx := WithTrace(context.Background(), "abc123")
	if got := Trace(ctx); got != "abc123" {
		t.Fatalf("Trace = %q, want %q", got, "abc123")
	}
}

func TestWithStageAndRead(t *testing.T) {
	ctx := WithStage(context.Background(), "council/round/scatter")
	if got := Stage(ctx); got != "council/round/scatter" {
		t.Fatalf("Stage = %q, want %q", got, "council/round/scatter")
	}
}

func TestTraceEmpty(t *testing.T) {
	if got := Trace(context.Background()); got != "" {
		t.Fatalf("Trace on empty ctx = %q, want empty", got)
	}
}

func TestDropCancelPreservesValues(t *testing.T) {
	ctx := WithTrace(context.Background(), "tid1")
	ctx = WithStage(ctx, "test/phase")
	ctx, cancel := context.WithCancel(ctx)
	cancel() // cancel parent

	dropped := DropCancel(ctx)
	if Trace(dropped) != "tid1" {
		t.Fatal("DropCancel lost TID")
	}
	if Stage(dropped) != "test/phase" {
		t.Fatal("DropCancel lost STG")
	}
	// dropped should NOT be done
	select {
	case <-dropped.Done():
		t.Fatal("DropCancel did not disconnect cancel")
	default:
		// ok
	}
}

func TestGoPropagatesCtx(t *testing.T) {
	ctx := WithTrace(context.Background(), "goroutine-tid")
	var wg sync.WaitGroup
	wg.Add(1)
	var got string
	Go(ctx, func(ctx context.Context) {
		defer wg.Done()
		got = Trace(ctx)
	})
	wg.Wait()
	if got != "goroutine-tid" {
		t.Fatalf("Go did not propagate TID: got %q", got)
	}
}

// --- ensure.go tests ---

func TestTruePass(t *testing.T) {
	True(true, "should not fire")
}

func TestTrueFail(t *testing.T) {
	var collected string
	old := OnFailed
	OnFailed = func(msg string) { collected = msg }
	defer func() { OnFailed = old }()

	True(false, "expected %d", 42)
	if collected != "expected 42" {
		t.Fatalf("OnFailed got %q", collected)
	}
}

func TestNotNilPass(t *testing.T) {
	NotNil(&struct{}{}, "thing")
}

func TestNotNilFail(t *testing.T) {
	var collected string
	old := OnFailed
	OnFailed = func(msg string) { collected = msg }
	defer func() { OnFailed = old }()

	NotNil(nil, "repo")
	if !strings.Contains(collected, "repo") {
		t.Fatalf("NotNil message missing name: %q", collected)
	}
}

// --- log.go tests ---

func TestLogInfoWithCtx(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	ctx := WithTrace(context.Background(), "t1")
	ctx = WithStage(ctx, "test/log")
	logger := Module("mymod")
	logger.Info(ctx, "hello", "key", "value")

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if e.L != "INFO" {
		t.Errorf("L = %q", e.L)
	}
	if e.TID != "t1" {
		t.Errorf("TID = %q", e.TID)
	}
	if e.STG != "test/log" {
		t.Errorf("STG = %q", e.STG)
	}
	if e.Mod != "mymod" {
		t.Errorf("Mod = %q", e.Mod)
	}
	if e.Ext["key"] != "value" {
		t.Errorf("Ext[key] = %v", e.Ext["key"])
	}
}

func TestLogLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	Init(WARN, &buf)
	defer Init(INFO, nil)

	logger := Module("test")
	logger.Info(context.Background(), "should be filtered")
	if buf.Len() > 0 {
		t.Fatal("INFO should be filtered at WARN level")
	}

	logger.Warn(context.Background(), "should appear")
	if buf.Len() == 0 {
		t.Fatal("WARN should not be filtered at WARN level")
	}
}

func TestLogModuleLevel(t *testing.T) {
	var buf bytes.Buffer
	Init(ERROR, &buf)
	SetModuleLevel("verbose", DEBUG)
	defer func() {
		Init(INFO, nil)
		moduleLevelsMu.Lock()
		delete(moduleLevels, "verbose")
		moduleLevelsMu.Unlock()
	}()

	Module("verbose").Debug(context.Background(), "should appear")
	if buf.Len() == 0 {
		t.Fatal("module-level override not working")
	}
}

// --- metrics.go tests ---

func TestCounter(t *testing.T) {
	ResetForTest()
	c := NewCounter("test_total")
	c.Inc()
	c.Add(4)
	if c.Value() != 5 {
		t.Fatalf("Counter = %d, want 5", c.Value())
	}
}

func TestCounterIdempotent(t *testing.T) {
	ResetForTest()
	c1 := NewCounter("same")
	c1.Inc()
	c2 := NewCounter("same")
	if c1 != c2 {
		t.Fatal("NewCounter not idempotent")
	}
	if c2.Value() != 1 {
		t.Fatal("idempotent counter lost value")
	}
}

func TestGauge(t *testing.T) {
	ResetForTest()
	g := NewGauge("test_gauge")
	g.Set(10)
	g.Add(5)
	g.Sub(3)
	if g.Value() != 12 {
		t.Fatalf("Gauge = %d, want 12", g.Value())
	}
}

func TestHistogram(t *testing.T) {
	ResetForTest()
	h := NewHistogram("test_duration", DefaultBuckets())
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(100) // +Inf bucket
	// just verify no panic
}

func TestNewRED(t *testing.T) {
	ResetForTest()
	req, errs, dur := NewRED("council")
	req.Inc()
	errs.Inc()
	dur.Observe(1.5)
	if req.Value() != 1 || errs.Value() != 1 {
		t.Fatal("RED counters wrong")
	}
}

func TestWritePrometheus(t *testing.T) {
	ResetForTest()
	NewCounter("app_requests").Inc()
	NewGauge("app_active").Set(3)

	var buf bytes.Buffer
	WritePrometheus(&buf)
	out := buf.String()
	if !strings.Contains(out, "app_requests 1") {
		t.Errorf("missing counter in prometheus output:\n%s", out)
	}
	if !strings.Contains(out, "app_active 3") {
		t.Errorf("missing gauge in prometheus output:\n%s", out)
	}
}

func TestResetForTest(t *testing.T) {
	NewCounter("to_delete")
	ResetForTest()
	R.mu.RLock()
	count := len(R.counters)
	R.mu.RUnlock()
	if count != 0 {
		t.Fatal("ResetForTest did not clear registry")
	}
}

// --- sanitization tests ---

func TestSanitizeRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	ctx := context.Background()
	Module("sec").Info(ctx, "check", "authorization", "Bearer xxx", "password", "s3cret", "user", "alice")

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Ext["authorization"] != "[redacted]" {
		t.Errorf("authorization not redacted: %v", e.Ext["authorization"])
	}
	if e.Ext["password"] != "[redacted]" {
		t.Errorf("password not redacted: %v", e.Ext["password"])
	}
	if e.Ext["user"] != "alice" {
		t.Errorf("user should not be redacted: %v", e.Ext["user"])
	}
}

func TestSanitizeTruncatesLongStrings(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	long := strings.Repeat("x", 300)
	Module("trunc").Info(context.Background(), "check", "data", long)

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := e.Ext["data"].(string)
	if len(got) > maxStringLen+10 {
		t.Errorf("string not truncated: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("truncated string should end with ...")
	}
}

func TestMarshalFallbackPreservesCore(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	ctx := WithTrace(context.Background(), "fallback-tid")
	ctx = WithStage(ctx, "test/fallback")
	// math.NaN() passes sanitizeVal (float64 primitive) but causes json.Marshal to fail,
	// triggering the real fallback path.
	Module("fb").Info(ctx, "test", "bad", math.NaN())

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if e.TID != "fallback-tid" {
		t.Errorf("TID lost: %q", e.TID)
	}
	if e.STG != "test/fallback" {
		t.Errorf("STG lost: %q", e.STG)
	}
	if e.Mod != "fb" {
		t.Errorf("Mod lost: %q", e.Mod)
	}
	if e.Ext != nil {
		t.Errorf("Ext should be nil in fallback: %v", e.Ext)
	}
	if !strings.Contains(e.Msg, "[obs: ext marshal failed]") {
		t.Errorf("Msg should contain fallback marker: %q", e.Msg)
	}
}

func TestTimestampUTCFormat(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	Module("ts").Info(context.Background(), "check")

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasSuffix(e.T, "Z") {
		t.Errorf("timestamp should end with Z (UTC): %q", e.T)
	}
}

func TestSetLevelConcurrent(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			SetLevel(WARN)
		}()
		go func() {
			defer wg.Done()
			Module("race").Info(context.Background(), "msg")
		}()
	}
	wg.Wait()
	// No race detector failure = pass
}

func TestSanitizeStructuralParity(t *testing.T) {
	// Verify that common Go types are preserved structurally (not stringified),
	// matching TS behavior where objects/arrays keep their structure.
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	Module("parity").Info(context.Background(), "check",
		"tags", []string{"a", "b", "c"},
		"meta", map[string]string{"env": "dev", "ver": "1.0"},
	)

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}

	// []string should be preserved as array, not "[a b c]"
	tags, ok := e.Ext["tags"].([]any)
	if !ok {
		t.Fatalf("tags should be []any, got %T: %v", e.Ext["tags"], e.Ext["tags"])
	}
	if len(tags) != 3 || tags[0] != "a" {
		t.Errorf("tags content wrong: %v", tags)
	}

	// map[string]string should be preserved as object, not "map[env:dev ver:1.0]"
	meta, ok := e.Ext["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta should be map[string]any, got %T: %v", e.Ext["meta"], e.Ext["meta"])
	}
	if meta["env"] != "dev" {
		t.Errorf("meta.env wrong: %v", meta["env"])
	}
}

func TestSanitizeErrorType(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	Module("err").Info(context.Background(), "check",
		"err", errors.New("connection refused"),
	)

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.Ext["err"] != "connection refused" {
		t.Errorf("error not stringified: %v", e.Ext["err"])
	}
}

func TestMalformedArgs(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	// Odd number of args: orphaned value gets !MISSING marker
	Module("mal").Info(context.Background(), "check", "orphan_key")

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if e.Ext["orphan_key"] != "!MISSING" {
		t.Errorf("orphaned key should get !MISSING: %v", e.Ext)
	}
}

func TestFileLineBasename(t *testing.T) {
	var buf bytes.Buffer
	Init(DEBUG, &buf)
	defer Init(INFO, nil)

	Module("fline").Warn(context.Background(), "check")

	var e Entry
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.F == "" {
		t.Fatal("F should be set for WARN")
	}
	// F should be basename:line, not a full path
	if strings.Contains(e.F, "/") {
		t.Errorf("F should be basename only, got: %q", e.F)
	}
}
