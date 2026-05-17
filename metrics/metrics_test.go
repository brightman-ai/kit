package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestCounter(t *testing.T) {
	c := NewCounter("test_counter")

	if c.Value() != 0 {
		t.Errorf("initial value = %d, want 0", c.Value())
	}

	c.Inc()
	if c.Value() != 1 {
		t.Errorf("after Inc() = %d, want 1", c.Value())
	}

	c.Add(5)
	if c.Value() != 6 {
		t.Errorf("after Add(5) = %d, want 6", c.Value())
	}

	if c.Name() != "test_counter" {
		t.Errorf("name = %q, want 'test_counter'", c.Name())
	}
}

func TestCounterWithTags(t *testing.T) {
	c := NewCounter("test_counter", "method", "GET", "path", "/api")

	tags := c.Tags()
	if tags["method"] != "GET" {
		t.Errorf("method tag = %q, want 'GET'", tags["method"])
	}
	if tags["path"] != "/api" {
		t.Errorf("path tag = %q, want '/api'", tags["path"])
	}
}

func TestCounterWithLabels(t *testing.T) {
	c := NewCounter("test_counter", "method", "GET")
	c2 := c.WithLabels("status", "200")

	// Original should be unchanged
	if _, ok := c.Tags()["status"]; ok {
		t.Error("original counter should not have status tag")
	}

	// New counter should have both
	if c2.Tags()["method"] != "GET" {
		t.Error("new counter missing original tag")
	}
	if c2.Tags()["status"] != "200" {
		t.Error("new counter missing new tag")
	}
}

func TestCounterConcurrent(t *testing.T) {
	c := NewCounter("concurrent_counter")
	var wg sync.WaitGroup
	n := 1000

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}

	wg.Wait()

	if c.Value() != uint64(n) {
		t.Errorf("value = %d, want %d", c.Value(), n)
	}
}

func TestGauge(t *testing.T) {
	g := NewGauge("test_gauge")

	if g.Value() != 0 {
		t.Errorf("initial value = %d, want 0", g.Value())
	}

	g.Set(100)
	if g.Value() != 100 {
		t.Errorf("after Set(100) = %d, want 100", g.Value())
	}

	g.Inc()
	if g.Value() != 101 {
		t.Errorf("after Inc() = %d, want 101", g.Value())
	}

	g.Dec()
	if g.Value() != 100 {
		t.Errorf("after Dec() = %d, want 100", g.Value())
	}

	g.Add(-50)
	if g.Value() != 50 {
		t.Errorf("after Add(-50) = %d, want 50", g.Value())
	}

	if g.Name() != "test_gauge" {
		t.Errorf("name = %q, want 'test_gauge'", g.Name())
	}
}

func TestGaugeConcurrent(t *testing.T) {
	g := NewGauge("concurrent_gauge")
	var wg sync.WaitGroup
	n := 1000

	// Half increment, half decrement
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g.Inc()
		}()
		go func() {
			defer wg.Done()
			g.Dec()
		}()
	}

	wg.Wait()

	if g.Value() != 0 {
		t.Errorf("value = %d, want 0", g.Value())
	}
}

func TestHistogram(t *testing.T) {
	h := NewHistogram("test_histogram", []float64{0.1, 0.5, 1.0})

	h.Observe(0.05)
	h.Observe(0.3)
	h.Observe(0.8)
	h.Observe(2.0)

	if h.Count() != 4 {
		t.Errorf("count = %d, want 4", h.Count())
	}

	expectedSum := 0.05 + 0.3 + 0.8 + 2.0
	if h.Sum() != expectedSum {
		t.Errorf("sum = %f, want %f", h.Sum(), expectedSum)
	}

	if h.Name() != "test_histogram" {
		t.Errorf("name = %q, want 'test_histogram'", h.Name())
	}
}

func TestHistogramWithTags(t *testing.T) {
	h := NewHistogram("test_histogram", DefaultBuckets(), "method", "POST")

	if h.Tags()["method"] != "POST" {
		t.Errorf("method tag = %q, want 'POST'", h.Tags()["method"])
	}
}

func TestTimer(t *testing.T) {
	h := NewHistogram("timer_test", DefaultBuckets())
	timer := h.NewTimer()

	time.Sleep(10 * time.Millisecond)
	duration := timer.ObserveDuration()

	if duration < 10*time.Millisecond {
		t.Errorf("duration = %v, want >= 10ms", duration)
	}

	if h.Count() != 1 {
		t.Errorf("count = %d, want 1", h.Count())
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	c := NewCounter("reg_counter")
	g := NewGauge("reg_gauge")
	h := NewHistogram("reg_histogram", DefaultBuckets())

	r.RegisterCounter(c)
	r.RegisterGauge(g)
	r.RegisterHistogram(h)

	if r.GetCounter("reg_counter") != c {
		t.Error("counter not found in registry")
	}
	if r.GetGauge("reg_gauge") != g {
		t.Error("gauge not found in registry")
	}
	if r.GetHistogram("reg_histogram") != h {
		t.Error("histogram not found in registry")
	}

	// Non-existent
	if r.GetCounter("nonexistent") != nil {
		t.Error("non-existent counter should be nil")
	}
}

func TestRegistrySnapshot(t *testing.T) {
	r := NewRegistry()

	c := NewCounter("snap_counter")
	g := NewGauge("snap_gauge")
	h := NewHistogram("snap_histogram", DefaultBuckets())

	c.Add(10)
	g.Set(20)
	h.Observe(0.5)

	r.RegisterCounter(c)
	r.RegisterGauge(g)
	r.RegisterHistogram(h)

	snap := r.Snapshot()

	if snap.Counters["snap_counter"] != 10 {
		t.Errorf("counter = %d, want 10", snap.Counters["snap_counter"])
	}
	if snap.Gauges["snap_gauge"] != 20 {
		t.Errorf("gauge = %d, want 20", snap.Gauges["snap_gauge"])
	}
	if snap.Histograms["snap_histogram"].Count != 1 {
		t.Errorf("histogram count = %d, want 1", snap.Histograms["snap_histogram"].Count)
	}
}

func TestDefaultBuckets(t *testing.T) {
	buckets := DefaultBuckets()
	if len(buckets) == 0 {
		t.Error("default buckets should not be empty")
	}

	// Verify sorted
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			t.Errorf("buckets not sorted: %v", buckets)
		}
	}
}

func TestPredefinedMetrics(t *testing.T) {
	// Verify pre-defined metrics are registered
	if DefaultRegistry.GetCounter("http_requests_total") == nil {
		t.Error("http_requests_total not registered")
	}
	if DefaultRegistry.GetHistogram("http_request_duration_seconds") == nil {
		t.Error("http_request_duration_seconds not registered")
	}
	if DefaultRegistry.GetGauge("http_active_connections") == nil {
		t.Error("http_active_connections not registered")
	}
	if DefaultRegistry.GetCounter("llm_requests_total") == nil {
		t.Error("llm_requests_total not registered")
	}
}

// Benchmarks

func BenchmarkCounterInc(b *testing.B) {
	c := NewCounter("bench_counter")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Inc()
	}
}

func BenchmarkGaugeSet(b *testing.B) {
	g := NewGauge("bench_gauge")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Set(int64(i))
	}
}

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewHistogram("bench_histogram", DefaultBuckets())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Observe(float64(i) * 0.001)
	}
}
