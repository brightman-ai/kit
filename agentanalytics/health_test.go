package agentanalytics

import (
	"testing"
	"time"
)

func TestActivityReportWeightsResponseThroughputByDuration(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(-time.Minute)
	two, eight, ttftOne, ttftThree, firstTwo, firstFour := 2.0, 8.0, 1.0, 3.0, 2.0, 4.0
	dataset := ActivityDataset{
		WorkItems:   []ActivityWorkItem{{ID: "w", Runtime: "claude", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end, Outcome: OutcomeVerifiedPass}},
		Instances:   []AgentInstance{{ID: "i", Runtime: "claude", ThreadID: "t"}},
		Assignments: []AgentAssignment{{ID: "a", WorkItemID: "w", AgentInstanceID: "i", Status: LifecycleCompleted}},
		Requests: []EconomicRequest{
			{ID: "r1", WorkItemID: "w", Runtime: "claude", At: start, OutputTokens: 20, ObservedResponseDurationSeconds: &two, TTFTSeconds: &ttftOne, ObservedFirstResponseSeconds: &firstTwo},
			{ID: "r2", WorkItemID: "w", Runtime: "claude", At: start.Add(time.Minute), OutputTokens: 40, ObservedResponseDurationSeconds: &eight, TTFTSeconds: &ttftThree, ObservedFirstResponseSeconds: &firstFour},
		},
	}
	report := BuildActivityReport(dataset, "24h", "UTC", now)
	if len(report.RuntimeProfiles) != 1 || report.RuntimeProfiles[0].ObservedResponseTokensPerSecond == nil || *report.RuntimeProfiles[0].ObservedResponseTokensPerSecond != 6 {
		t.Fatalf("weighted response throughput=%+v", report.RuntimeProfiles)
	}
	if report.RuntimeProfiles[0].TTFTMedianSeconds == nil || *report.RuntimeProfiles[0].TTFTMedianSeconds != 2 || report.RuntimeProfiles[0].ObservedFirstResponseMedianSeconds == nil || *report.RuntimeProfiles[0].ObservedFirstResponseMedianSeconds != 3 {
		t.Fatalf("latency medians=%+v", report.RuntimeProfiles[0])
	}
	if report.Health.State != HealthInsufficient || report.Health.PolicyVersion != AgentHealthPolicyVersion {
		t.Fatalf("health=%+v", report.Health)
	}
}

func TestHealthDoesNotCountRootAssignmentLifecycleTwice(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(-time.Minute)
	dataset := ActivityDataset{
		WorkItems:   []ActivityWorkItem{{ID: "w", Runtime: "claude", Status: LifecycleError, StartedAt: &start, EndedAt: &end, Outcome: OutcomeError}},
		Instances:   []AgentInstance{{ID: "i", Runtime: "claude", ThreadID: "t"}},
		Assignments: []AgentAssignment{{ID: "a", WorkItemID: "w", AgentInstanceID: "i", Root: true, Status: LifecycleError}},
	}
	report := BuildActivityReport(dataset, "24h", "UTC", now)
	if report.Summary.AssignmentLifecycle.Errors != 1 || report.Summary.DelegatedLifecycle.Errors != 0 {
		t.Fatalf("assignment summaries=%+v", report.Summary)
	}
	for _, reason := range report.Health.Reasons {
		if reason.Code == "delegated_assignment_errors" {
			t.Fatalf("root lifecycle was counted twice: %+v", report.Health)
		}
	}
}

func TestHealthFailureCannotBeOffsetByHighProduction(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(-time.Minute)
	second := 1.0
	dataset := ActivityDataset{
		WorkItems:   []ActivityWorkItem{{ID: "w", Runtime: "claude", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end, Outcome: OutcomeVerifiedFail, OutputTokens: 9_000_000}},
		Instances:   []AgentInstance{{ID: "i", Runtime: "claude", ThreadID: "t"}},
		Assignments: []AgentAssignment{{ID: "a", WorkItemID: "w", AgentInstanceID: "i", Status: LifecycleCompleted}},
		Requests:    []EconomicRequest{{ID: "r", WorkItemID: "w", Runtime: "claude", At: start, OutputTokens: 9_000_000, ObservedResponseDurationSeconds: &second}},
		Artifacts:   []ArtifactDelta{{WorkItemID: "w", Path: "huge.go", Kind: ArtifactCode, Additions: 1_000_000, At: end, Attribution: AttributionProviderPatch}},
	}
	report := BuildActivityReport(dataset, "24h", "UTC", now)
	if report.Health.State != HealthCritical || report.Health.Label != "异常" || len(report.Health.Reasons) == 0 || report.Health.Reasons[0].Code != "verified_failure" {
		t.Fatalf("production offset hard failure: %+v", report.Health)
	}
}
