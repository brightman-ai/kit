package agentanalytics

import (
	"strings"
	"testing"
	"time"
)

func TestMetricRegistryIsCompleteAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, metric := range MetricRegistry() {
		if metric.ID == "" || metric.Grain == "" || metric.Formula == "" || metric.Unit == "" || metric.Eligibility == "" || metric.Timezone == "" || metric.Provenance == "" || metric.Coverage == "" || metric.Aggregation == "" || metric.ComparisonPolicy == "" {
			t.Fatalf("incomplete metric: %+v", metric)
		}
		if seen[metric.ID] {
			t.Fatalf("duplicate metric id: %s", metric.ID)
		}
		seen[metric.ID] = true
	}
}

func TestWorkGraphResumeAndCrossPaneCollision(t *testing.T) {
	g := WorkGraph{
		WorkItems: []WorkItem{{ID: "w1"}},
		Instances: []AgentInstance{{ID: "i1", ThreadID: "thread-1", SourceRef: "rollout-1", Depth: 0}},
		Assignments: []AgentAssignment{
			{ID: "a1", WorkItemID: "w1", AgentInstanceID: "i1", Attempt: 1},
			{ID: "a2", WorkItemID: "w1", AgentInstanceID: "i1", Attempt: 2}, // resume: new assignment, same instance
		},
		Requests: []ModelRequestRef{{ID: "r1", WorkItemID: "w1", AssignmentID: "a2", AgentInstanceID: "i1"}},
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("valid resume graph: %v", err)
	}
	g.Instances = append(g.Instances, AgentInstance{ID: "i2", ThreadID: "thread-2", SourceRef: "rollout-1", Depth: 0})
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "cross_root_collision") {
		t.Fatalf("same rollout owned by two root panes must fail, got %v", err)
	}
}

func TestSummarizeIntervalsUnionAndSum(t *testing.T) {
	base := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	s := SummarizeIntervals([]Interval{
		{Start: base.Add(time.Hour), End: base.Add(3 * time.Hour)},
		{Start: base.Add(2 * time.Hour), End: base.Add(4 * time.Hour)},
	}, Interval{Start: base, End: base.Add(24 * time.Hour)})
	if s.Wall != 3*time.Hour || s.Cumulative != 4*time.Hour || s.Concurrency == nil || *s.Concurrency != 4.0/3.0 {
		t.Fatalf("summary=%+v, want wall=3h cumulative=4h concurrency=4/3", s)
	}
}
