// Package metrics provides application metrics collection.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing counter.
type Counter struct {
	value uint64
	name  string
	tags  map[string]string
}

// NewCounter creates a new counter with name and optional tags.
func NewCounter(name string, tags ...string) *Counter {
	c := &Counter{
		name: name,
		tags: make(map[string]string),
	}
	for i := 0; i+1 < len(tags); i += 2 {
		c.tags[tags[i]] = tags[i+1]
	}
	return c
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	atomic.AddUint64(&c.value, 1)
}

// Add adds delta to the counter.
func (c *Counter) Add(delta uint64) {
	atomic.AddUint64(&c.value, delta)
}

// Value returns the current counter value.
func (c *Counter) Value() uint64 {
	return atomic.LoadUint64(&c.value)
}

// Name returns the counter name.
func (c *Counter) Name() string {
	return c.name
}

// Tags returns the counter tags.
func (c *Counter) Tags() map[string]string {
	return c.tags
}

// WithLabels returns a new counter with additional labels.
func (c *Counter) WithLabels(labels ...string) *Counter {
	newTags := make(map[string]string)
	for k, v := range c.tags {
		newTags[k] = v
	}
	for i := 0; i+1 < len(labels); i += 2 {
		newTags[labels[i]] = labels[i+1]
	}
	return &Counter{
		name: c.name,
		tags: newTags,
	}
}

// Gauge is a value that can increase and decrease.
type Gauge struct {
	value int64
	name  string
	tags  map[string]string
}

// NewGauge creates a new gauge with name and optional tags.
func NewGauge(name string, tags ...string) *Gauge {
	g := &Gauge{
		name: name,
		tags: make(map[string]string),
	}
	for i := 0; i+1 < len(tags); i += 2 {
		g.tags[tags[i]] = tags[i+1]
	}
	return g
}

// Set sets the gauge value.
func (g *Gauge) Set(value int64) {
	atomic.StoreInt64(&g.value, value)
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	atomic.AddInt64(&g.value, 1)
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	atomic.AddInt64(&g.value, -1)
}

// Add adds delta to the gauge.
func (g *Gauge) Add(delta int64) {
	atomic.AddInt64(&g.value, delta)
}

// Value returns the current gauge value.
func (g *Gauge) Value() int64 {
	return atomic.LoadInt64(&g.value)
}

// Name returns the gauge name.
func (g *Gauge) Name() string {
	return g.name
}

// Tags returns the gauge tags.
func (g *Gauge) Tags() map[string]string {
	return g.tags
}

// Histogram tracks distribution of values.
type Histogram struct {
	mu      sync.RWMutex
	name    string
	tags    map[string]string
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// NewHistogram creates a new histogram with name and buckets.
func NewHistogram(name string, buckets []float64, tags ...string) *Histogram {
	h := &Histogram{
		name:    name,
		buckets: buckets,
		counts:  make([]uint64, len(buckets)+1), // +1 for +Inf
		tags:    make(map[string]string),
	}
	for i := 0; i+1 < len(tags); i += 2 {
		h.tags[tags[i]] = tags[i+1]
	}
	return h
}

// DefaultBuckets returns default latency buckets in seconds.
func DefaultBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
}

// Observe records a value.
func (h *Histogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sum += value
	h.count++

	for i, boundary := range h.buckets {
		if value <= boundary {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.buckets)]++ // +Inf bucket
}

// Name returns the histogram name.
func (h *Histogram) Name() string {
	return h.name
}

// Tags returns the histogram tags.
func (h *Histogram) Tags() map[string]string {
	return h.tags
}

// Sum returns the sum of all observed values.
func (h *Histogram) Sum() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sum
}

// Count returns the count of observations.
func (h *Histogram) Count() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.count
}

// Timer measures duration and records to histogram.
type Timer struct {
	histogram *Histogram
	start     time.Time
}

// NewTimer starts a new timer for the histogram.
func (h *Histogram) NewTimer() *Timer {
	return &Timer{
		histogram: h,
		start:     time.Now(),
	}
}

// ObserveDuration records the elapsed time since timer start.
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	t.histogram.Observe(d.Seconds())
	return d
}

// ========== Registry ==========

// Registry holds all registered metrics.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry creates a new metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// DefaultRegistry is the global default registry.
var DefaultRegistry = NewRegistry()

// RegisterCounter registers a counter in the registry.
func (r *Registry) RegisterCounter(c *Counter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[c.name] = c
}

// RegisterGauge registers a gauge in the registry.
func (r *Registry) RegisterGauge(g *Gauge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[g.name] = g
}

// RegisterHistogram registers a histogram in the registry.
func (r *Registry) RegisterHistogram(h *Histogram) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.histograms[h.name] = h
}

// GetCounter returns a counter by name.
func (r *Registry) GetCounter(name string) *Counter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.counters[name]
}

// GetGauge returns a gauge by name.
func (r *Registry) GetGauge(name string) *Gauge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.gauges[name]
}

// GetHistogram returns a histogram by name.
func (r *Registry) GetHistogram(name string) *Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.histograms[name]
}

// Snapshot returns a snapshot of all metrics.
type Snapshot struct {
	Counters   map[string]uint64
	Gauges     map[string]int64
	Histograms map[string]HistogramSnapshot
}

// HistogramSnapshot contains histogram data.
type HistogramSnapshot struct {
	Count uint64
	Sum   float64
}

// Snapshot returns current values of all metrics.
func (r *Registry) Snapshot() *Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s := &Snapshot{
		Counters:   make(map[string]uint64),
		Gauges:     make(map[string]int64),
		Histograms: make(map[string]HistogramSnapshot),
	}

	for name, c := range r.counters {
		s.Counters[name] = c.Value()
	}
	for name, g := range r.gauges {
		s.Gauges[name] = g.Value()
	}
	for name, h := range r.histograms {
		s.Histograms[name] = HistogramSnapshot{
			Count: h.Count(),
			Sum:   h.Sum(),
		}
	}

	return s
}

// ========== Global helpers ==========

// Counter creates or returns a counter.
func Counter_(name string, tags ...string) *Counter {
	c := NewCounter(name, tags...)
	DefaultRegistry.RegisterCounter(c)
	return c
}

// Gauge creates or returns a gauge.
func Gauge_(name string, tags ...string) *Gauge {
	g := NewGauge(name, tags...)
	DefaultRegistry.RegisterGauge(g)
	return g
}

// Histogram creates or returns a histogram.
func Histogram_(name string, buckets []float64, tags ...string) *Histogram {
	h := NewHistogram(name, buckets, tags...)
	DefaultRegistry.RegisterHistogram(h)
	return h
}

// ========== Pre-defined metrics ==========

var (
	// HTTP metrics
	HTTPRequestsTotal       = NewCounter("http_requests_total")
	HTTPRequestDuration     = NewHistogram("http_request_duration_seconds", DefaultBuckets())
	HTTPActiveConnections   = NewGauge("http_active_connections")

	// LLM metrics
	LLMRequestsTotal    = NewCounter("llm_requests_total")
	LLMRequestDuration  = NewHistogram("llm_request_duration_seconds", DefaultBuckets())
	LLMTokensTotal      = NewCounter("llm_tokens_total")
	LLMSlowRequests     = NewCounter("llm_slow_requests_total")

	// Database metrics
	DBQueriesTotal    = NewCounter("db_queries_total")
	DBQueryDuration   = NewHistogram("db_query_duration_seconds", DefaultBuckets())
	DBConnectionsOpen = NewGauge("db_connections_open")

	// Memory metrics
	MemorySessionsActive  = NewGauge("memory_sessions_active")
	MemoryMessagesTotal   = NewCounter("memory_messages_total")

	// Workforce metrics
	WorkforceActive     = NewGauge("workforce_active")
	WorkforceTasksTotal = NewCounter("workforce_tasks_total")

	// ========== AI Engineering Metrics (Deep-AI-Metrics) ==========

	// Token consumption metrics
	// Labels: model, type (input/output), feature
	AITokensTotal = NewCounter("ai_tokens_total")

	// Cost tracking in cents (for budget monitoring)
	// Labels: model, feature
	AICostCents = NewCounter("ai_cost_cents")

	// Time to first token for streaming responses
	AITimeToFirstToken = NewHistogram("ai_time_to_first_token_seconds",
		[]float64{0.1, 0.25, 0.5, 1, 2, 5, 10})

	// Agent execution metrics
	// Labels: agent_type, status (success/failure)
	AgentToolCallsTotal = NewCounter("agent_tool_calls_total")
	AgentStepsPerTask   = NewHistogram("agent_steps_per_task",
		[]float64{1, 2, 3, 5, 10, 20, 50})

	// User feedback metrics
	// Labels: feedback_type (thumbs_up/thumbs_down/edit)
	AIUserFeedbackTotal = NewCounter("ai_user_feedback_total")

	// ThinkFlow metrics
	ThinkFlowExecutionsTotal = NewCounter("thinkflow_executions_total")
	ThinkFlowDuration        = NewHistogram("thinkflow_duration_seconds", DefaultBuckets())
	ThinkFlowRetriesTotal    = NewCounter("thinkflow_retries_total")

	// Skill execution metrics
	SkillCallsTotal    = NewCounter("skill_calls_total")
	SkillCallDuration  = NewHistogram("skill_call_duration_seconds", DefaultBuckets())
	SkillFailuresTotal = NewCounter("skill_failures_total")
)

func init() {
	// Register pre-defined metrics
	DefaultRegistry.RegisterCounter(HTTPRequestsTotal)
	DefaultRegistry.RegisterHistogram(HTTPRequestDuration)
	DefaultRegistry.RegisterGauge(HTTPActiveConnections)

	DefaultRegistry.RegisterCounter(LLMRequestsTotal)
	DefaultRegistry.RegisterHistogram(LLMRequestDuration)
	DefaultRegistry.RegisterCounter(LLMTokensTotal)
	DefaultRegistry.RegisterCounter(LLMSlowRequests)

	DefaultRegistry.RegisterCounter(DBQueriesTotal)
	DefaultRegistry.RegisterHistogram(DBQueryDuration)
	DefaultRegistry.RegisterGauge(DBConnectionsOpen)

	DefaultRegistry.RegisterGauge(MemorySessionsActive)
	DefaultRegistry.RegisterCounter(MemoryMessagesTotal)

	DefaultRegistry.RegisterGauge(WorkforceActive)
	DefaultRegistry.RegisterCounter(WorkforceTasksTotal)

	// AI Engineering Metrics
	DefaultRegistry.RegisterCounter(AITokensTotal)
	DefaultRegistry.RegisterCounter(AICostCents)
	DefaultRegistry.RegisterHistogram(AITimeToFirstToken)
	DefaultRegistry.RegisterCounter(AgentToolCallsTotal)
	DefaultRegistry.RegisterHistogram(AgentStepsPerTask)
	DefaultRegistry.RegisterCounter(AIUserFeedbackTotal)
	DefaultRegistry.RegisterCounter(ThinkFlowExecutionsTotal)
	DefaultRegistry.RegisterHistogram(ThinkFlowDuration)
	DefaultRegistry.RegisterCounter(ThinkFlowRetriesTotal)
	DefaultRegistry.RegisterCounter(SkillCallsTotal)
	DefaultRegistry.RegisterHistogram(SkillCallDuration)
	DefaultRegistry.RegisterCounter(SkillFailuresTotal)
}

// ========== Prometheus text format exporter ==========

// WritePrometheus writes metrics in Prometheus text format.
func (r *Registry) WritePrometheus(w io.Writer) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Sort names for stable output
	counterNames := make([]string, 0, len(r.counters))
	for name := range r.counters {
		counterNames = append(counterNames, name)
	}
	sort.Strings(counterNames)

	gaugeNames := make([]string, 0, len(r.gauges))
	for name := range r.gauges {
		gaugeNames = append(gaugeNames, name)
	}
	sort.Strings(gaugeNames)

	histogramNames := make([]string, 0, len(r.histograms))
	for name := range r.histograms {
		histogramNames = append(histogramNames, name)
	}
	sort.Strings(histogramNames)

	// Write counters
	for _, name := range counterNames {
		c := r.counters[name]
		fmt.Fprintf(w, "# TYPE %s counter\n", name)
		fmt.Fprintf(w, "%s%s %d\n", name, formatTags(c.tags), c.Value())
	}

	// Write gauges
	for _, name := range gaugeNames {
		g := r.gauges[name]
		fmt.Fprintf(w, "# TYPE %s gauge\n", name)
		fmt.Fprintf(w, "%s%s %d\n", name, formatTags(g.tags), g.Value())
	}

	// Write histograms
	for _, name := range histogramNames {
		h := r.histograms[name]
		h.mu.RLock()
		fmt.Fprintf(w, "# TYPE %s histogram\n", name)

		// Cumulative bucket counts
		var cumulative uint64
		for i, boundary := range h.buckets {
			cumulative += h.counts[i]
			fmt.Fprintf(w, "%s_bucket{le=\"%.3f\"%s} %d\n",
				name, boundary, formatTagsSuffix(h.tags), cumulative)
		}
		cumulative += h.counts[len(h.buckets)]
		fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"%s} %d\n", name, formatTagsSuffix(h.tags), cumulative)

		fmt.Fprintf(w, "%s_sum%s %.6f\n", name, formatTags(h.tags), h.sum)
		fmt.Fprintf(w, "%s_count%s %d\n", name, formatTags(h.tags), h.count)
		h.mu.RUnlock()
	}

	return nil
}

// formatTags formats tags as Prometheus labels.
func formatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, fmt.Sprintf("%s=\"%s\"", k, v))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

// formatTagsSuffix formats tags as suffix for bucket labels.
func formatTagsSuffix(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, fmt.Sprintf("%s=\"%s\"", k, v))
	}
	sort.Strings(parts)
	return "," + strings.Join(parts, ",")
}

// PrometheusHandler returns a http.HandlerFunc for /metrics endpoint.
func PrometheusHandler() func(w io.Writer) {
	return func(w io.Writer) {
		DefaultRegistry.WritePrometheus(w)
	}
}

// ========== AI Metrics Helper Functions ==========

// RecordTokens records token consumption for an AI request.
func RecordTokens(model, tokenType, feature string, count uint64) {
	AITokensTotal.WithLabels("model", model, "type", tokenType, "feature", feature).Add(count)
}

// RecordCost records cost in cents for an AI request.
func RecordCost(model, feature string, cents uint64) {
	AICostCents.WithLabels("model", model, "feature", feature).Add(cents)
}

// RecordTTFT records time to first token for a streaming response.
func RecordTTFT(seconds float64) {
	AITimeToFirstToken.Observe(seconds)
}

// RecordToolCall records a tool/skill call by an agent.
func RecordToolCall(agentType, status string) {
	AgentToolCallsTotal.WithLabels("agent_type", agentType, "status", status).Inc()
}

// RecordAgentSteps records the number of steps taken to complete a task.
func RecordAgentSteps(steps float64) {
	AgentStepsPerTask.Observe(steps)
}

// RecordUserFeedback records user feedback on AI response.
func RecordUserFeedback(feedbackType string) {
	AIUserFeedbackTotal.WithLabels("feedback_type", feedbackType).Inc()
}

// RecordThinkFlowExecution records a ThinkFlow execution.
func RecordThinkFlowExecution(durationSec float64) {
	ThinkFlowExecutionsTotal.Inc()
	ThinkFlowDuration.Observe(durationSec)
}

// RecordThinkFlowRetry records a ThinkFlow retry.
func RecordThinkFlowRetry() {
	ThinkFlowRetriesTotal.Inc()
}

// RecordSkillCall records a skill invocation.
func RecordSkillCall(skillName string, durationSec float64, success bool) {
	SkillCallsTotal.WithLabels("skill", skillName).Inc()
	SkillCallDuration.Observe(durationSec)
	if !success {
		SkillFailuresTotal.WithLabels("skill", skillName).Inc()
	}
}
