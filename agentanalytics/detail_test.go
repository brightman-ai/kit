package agentanalytics

import (
	"testing"
	"time"
)

func TestBuildActivityDetailFiltersEverySectionAndPagesTrace(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, loc)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	dataset := ActivityDataset{
		WorkItems: []ActivityWorkItem{
			{ID: "w1", Runtime: "codex", Project: "/p", TaskProfile: TaskProfile{TaskClass: "bug", Risk: "low"}, Status: LifecycleCompleted, Outcome: OutcomeCompletedUnverified, StartedAt: &start, EndedAt: &end},
			{ID: "w2", Runtime: "claude", Project: "/p", TaskProfile: TaskProfile{TaskClass: "design", Risk: "high"}, Status: LifecycleCompleted, Outcome: OutcomeHumanAccepted, StartedAt: &start, EndedAt: &end},
		},
		Instances:   []AgentInstance{{ID: "i1", Runtime: "codex"}, {ID: "i2", Runtime: "claude"}},
		Assignments: []AgentAssignment{{ID: "a1", WorkItemID: "w1", AgentInstanceID: "i1", Attempt: 1}, {ID: "a2", WorkItemID: "w2", AgentInstanceID: "i2", Attempt: 1}},
		Requests:    []EconomicRequest{{ID: "r1", WorkItemID: "w1", Runtime: "codex", Model: "gpt-5.6-sol", At: start}, {ID: "r2", WorkItemID: "w2", Runtime: "claude", Model: "claude-sonnet-5", At: start}},
		Tools: []ToolExecution{
			{ID: "t1", WorkItemID: "w1", Runtime: "codex", Name: "exec", Status: ToolExecutionCompleted, StartedAt: &start, EndedAt: &end},
			{ID: "t2", WorkItemID: "w2", Runtime: "claude", Name: "read", Status: ToolExecutionCompleted, StartedAt: &start, EndedAt: &end},
		},
	}
	detail := BuildActivityDetail(dataset, "24h", "Asia/Shanghai", now, DetailFilter{Runtime: "codex", Limit: 1})
	if detail.SchemaVersion != "agent-detail.v1" || detail.Report.Summary.WorkItems != 1 || detail.Report.Summary.ModelRequests != 1 || detail.Report.Tools.Calls != 1 || detail.Report.RuntimeProfiles[0].Tools.Calls != 1 || len(detail.Tasks) != 1 || detail.Tasks[0].ID != "w1" {
		t.Fatalf("filtered detail=%+v", detail)
	}
	if len(detail.Filters.Runtimes) != 2 || len(detail.Metrics) == 0 {
		t.Fatalf("filter options/metric dictionary missing: %+v", detail)
	}
	if detail.Tasks[0].Diagnostics[0] != "applicable_outcome_evidence_missing" {
		t.Fatalf("trace lost evidence diagnostic: %+v", detail.Tasks[0])
	}
	if detail.Tasks[0].Assignments == nil || detail.Tasks[0].Requests == nil || detail.Tasks[0].Artifacts == nil {
		t.Fatalf("detail task collections must be empty arrays, not null: %+v", detail.Tasks[0])
	}

	all := BuildActivityDetail(dataset, "24h", "Asia/Shanghai", now, DetailFilter{Limit: 1})
	if len(all.Tasks) != 1 || all.NextCursor == "" {
		t.Fatalf("first page=%+v", all)
	}
	next := BuildActivityDetail(dataset, "24h", "Asia/Shanghai", now, DetailFilter{Limit: 1, Cursor: all.NextCursor})
	if len(next.Tasks) != 1 || next.Tasks[0].ID == all.Tasks[0].ID || next.NextCursor != "" {
		t.Fatalf("second page=%+v", next)
	}
}
