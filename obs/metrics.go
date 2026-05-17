package obs

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing counter. Thread-safe via atomic.
type Counter struct {
	value atomic.Uint64
	name  string
}

func (c *Counter) Inc()            { c.value.Add(1) }
func (c *Counter) Add(n uint64)    { c.value.Add(n) }
func (c *Counter) Value() uint64   { return c.value.Load() }
func (c *Counter) Name() string    { return c.name }

// Gauge is a value that can go up and down. Thread-safe via atomic.
type Gauge struct {
	value atomic.Int64
	name  string
}

func (g *Gauge) Set(v int64)  { g.value.Store(v) }
func (g *Gauge) Add(n int64)  { g.value.Add(n) }
func (g *Gauge) Sub(n int64)  { g.value.Add(-n) }
func (g *Gauge) Value() int64 { return g.value.Load() }
func (g *Gauge) Name() string { return g.name }

// Histogram records observations into pre-defined buckets.
// Thread-safe via mutex. Acceptable for desktop app concurrency levels.
// Performance flip trigger: profiler confirms >1% CPU or observable mutex contention.
type Histogram struct {
	mu      sync.Mutex
	name    string
	buckets []float64
	counts  []uint64 // len = len(buckets) + 1 (+Inf bucket)
	sum     float64
	count   uint64
}

func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
			h.mu.Unlock()
			return
		}
	}
	h.counts[len(h.buckets)]++ // +Inf
	h.mu.Unlock()
}

func (h *Histogram) Name() string { return h.name }

// DefaultBuckets returns the default histogram bucket boundaries.
func DefaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// --- Registry ---

// Registry is the global metric registration point.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

var R = &Registry{
	counters:   make(map[string]*Counter),
	gauges:     make(map[string]*Gauge),
	histograms: make(map[string]*Histogram),
}

// NewCounter creates and registers a Counter. Idempotent: returns existing if name already registered.
func NewCounter(name string) *Counter {
	R.mu.Lock()
	defer R.mu.Unlock()
	if c, ok := R.counters[name]; ok {
		return c
	}
	c := &Counter{name: name}
	R.counters[name] = c
	return c
}

// NewHistogram creates and registers a Histogram. Idempotent.
func NewHistogram(name string, buckets []float64) *Histogram {
	R.mu.Lock()
	defer R.mu.Unlock()
	if h, ok := R.histograms[name]; ok {
		return h
	}
	h := &Histogram{
		name:    name,
		buckets: buckets,
		counts:  make([]uint64, len(buckets)+1),
	}
	R.histograms[name] = h
	return h
}

// NewGauge creates and registers a Gauge. Idempotent.
func NewGauge(name string) *Gauge {
	R.mu.Lock()
	defer R.mu.Unlock()
	if g, ok := R.gauges[name]; ok {
		return g
	}
	g := &Gauge{name: name}
	R.gauges[name] = g
	return g
}

// NewRED creates the RED (Rate/Error/Duration) metric triple for a namespace.
func NewRED(ns string) (requests, errors *Counter, duration *Histogram) {
	return NewCounter(ns + "_requests_total"),
		NewCounter(ns + "_errors_total"),
		NewHistogram(ns + "_duration_seconds", DefaultBuckets())
}

// WritePrometheus writes all registered metrics in Prometheus text exposition format.
func WritePrometheus(w io.Writer) {
	R.mu.RLock()
	defer R.mu.RUnlock()

	// sorted output for deterministic ordering
	names := make([]string, 0, len(R.counters)+len(R.gauges)+len(R.histograms))

	for name := range R.counters {
		names = append(names, "c:"+name)
	}
	for name := range R.gauges {
		names = append(names, "g:"+name)
	}
	for name := range R.histograms {
		names = append(names, "h:"+name)
	}
	sort.Strings(names)

	for _, key := range names {
		kind := key[:2]
		name := key[2:]
		switch kind {
		case "c:":
			c := R.counters[name]
			fmt.Fprintf(w, "# TYPE %s counter\n%s %d\n", name, name, c.Value())
		case "g:":
			g := R.gauges[name]
			fmt.Fprintf(w, "# TYPE %s gauge\n%s %d\n", name, name, g.Value())
		case "h:":
			h := R.histograms[name]
			h.mu.Lock()
			var cumulative uint64
			for i, b := range h.buckets {
				cumulative += h.counts[i]
				fmt.Fprintf(w, "%s_bucket{le=\"%.3f\"} %d\n", name, b, cumulative)
			}
			cumulative += h.counts[len(h.buckets)]
			fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
			fmt.Fprintf(w, "%s_sum %.6f\n", name, h.sum)
			fmt.Fprintf(w, "%s_count %d\n", name, h.count)
			h.mu.Unlock()
		}
	}
}

// ResetForTest clears all registered metrics. Only for use in tests.
func ResetForTest() {
	R.mu.Lock()
	defer R.mu.Unlock()
	R.counters = make(map[string]*Counter)
	R.gauges = make(map[string]*Gauge)
	R.histograms = make(map[string]*Histogram)
}
