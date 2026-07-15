package agentanalytics

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestBuildActivityReportUsesStableJSONCollections(t *testing.T) {
	report := BuildActivityReport(ActivityDataset{}, "24h", "Asia/Shanghai", time.Now())
	if report.RuntimeProfiles == nil || report.Comparisons == nil || report.TopCostModels == nil {
		t.Fatalf("report collections must be empty, not nil: %+v", report)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["runtime_profiles"]) != "[]" || string(payload["comparisons"]) != "[]" || string(payload["top_cost_models"]) != "{}" {
		t.Fatalf("unstable JSON collection shape: %s", body)
	}
}

func TestModelCompositionKeepsTopThreeKnownAndUnknown(t *testing.T) {
	rows := []ModelCostRow{
		{Model: "opus"}, {Model: "sonnet"}, {Model: "fable"},
		{Model: "haiku"}, {Model: ""},
	}
	selected := selectModelCompositionRows(rows, 3)
	if len(selected) != 4 || selected[0].Model != "opus" || selected[1].Model != "sonnet" || selected[2].Model != "fable" || selected[3].Model != "" {
		t.Fatalf("top-three known + unknown bucket lost: %+v", selected)
	}
}

func TestModelCompositionAggregatesObservedThroughputByExactModel(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(-time.Minute)
	two, eight := 2.0, 8.0
	dataset := ActivityDataset{
		WorkItems: []ActivityWorkItem{{ID: "w", Runtime: "claude", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end}},
		Requests: []EconomicRequest{
			{ID: "o1", WorkItemID: "w", Runtime: "claude", Model: "opus", At: start, OutputTokens: 20, ObservedResponseDurationSeconds: &two},
			{ID: "o2", WorkItemID: "w", Runtime: "claude", Model: "opus", At: start.Add(time.Minute), OutputTokens: 40, ObservedResponseDurationSeconds: &eight},
			{ID: "s1", WorkItemID: "w", Runtime: "claude", Model: "sonnet", At: start.Add(2 * time.Minute), OutputTokens: 50, ObservedResponseDurationSeconds: &two},
			{ID: "s2", WorkItemID: "w", Runtime: "claude", Model: "sonnet", At: start.Add(3 * time.Minute), OutputTokens: 10},
		},
	}

	report := BuildActivityReport(dataset, "24h", "UTC", now)
	rows := make(map[string]ModelCostRow)
	for _, row := range report.TopCostModels["claude"] {
		rows[row.Model] = row
	}
	opus, sonnet := rows["opus"], rows["sonnet"]
	if opus.ObservedResponseTokensPerSecond == nil || *opus.ObservedResponseTokensPerSecond != 6 || opus.ObservedResponseOutputTokens != 60 || opus.ObservedResponseDurationSeconds != 10 {
		t.Fatalf("opus weighted throughput=%+v", opus)
	}
	if opus.ResponseSpeedCoverage.ObservedN != 2 || opus.ResponseSpeedCoverage.EligibleN != 2 || opus.ResponseSpeedCoverage.State != "complete" {
		t.Fatalf("opus response coverage=%+v", opus.ResponseSpeedCoverage)
	}
	if sonnet.ObservedResponseTokensPerSecond == nil || *sonnet.ObservedResponseTokensPerSecond != 25 || sonnet.ObservedResponseOutputTokens != 50 || sonnet.ObservedResponseDurationSeconds != 2 {
		t.Fatalf("sonnet weighted throughput=%+v", sonnet)
	}
	if sonnet.ResponseSpeedCoverage.ObservedN != 1 || sonnet.ResponseSpeedCoverage.EligibleN != 2 || sonnet.ResponseSpeedCoverage.State != "partial" {
		t.Fatalf("sonnet response coverage=%+v", sonnet.ResponseSpeedCoverage)
	}
	runtimeRate := report.RuntimeProfiles[0].ObservedResponseTokensPerSecond
	if runtimeRate == nil || math.Abs(*runtimeRate-(110.0/12.0)) > 1e-9 {
		t.Fatalf("runtime throughput must share the same additive rule, got %v", runtimeRate)
	}
}

func TestModelCompositionCanonicalizesUnknownAndUsesStableTieBreak(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(-time.Minute)
	dataset := ActivityDataset{
		WorkItems: []ActivityWorkItem{{ID: "w", Runtime: "codex", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end}},
		Requests: []EconomicRequest{
			{ID: "b", WorkItemID: "w", Runtime: "codex", Model: "beta", At: start},
			{ID: "a", WorkItemID: "w", Runtime: "codex", Model: "alpha", At: start},
			{ID: "u1", WorkItemID: "w", Runtime: "codex", Model: "", At: start},
			{ID: "u2", WorkItemID: "w", Runtime: "codex", Model: "   ", At: start},
		},
	}

	rows := BuildActivityReport(dataset, "24h", "UTC", now).TopCostModels["codex"]
	if len(rows) != 3 || rows[0].Model != "alpha" || rows[1].Model != "beta" || rows[2].Model != "" || rows[2].RequestN != 2 {
		t.Fatalf("model composition must be deterministic with one unknown bucket: %+v", rows)
	}
}

func TestBuildActivityReportLocalCalendarAndDistinctCounts(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, loc)
	a1, a2, a3 := now.Add(-2*time.Hour), now.Add(-time.Hour), now
	dataset := ActivityDataset{
		WorkItems: []ActivityWorkItem{
			{ID: "w1", Runtime: "codex", Status: LifecycleCompleted, StartedAt: &a1, EndedAt: &a2, ToolCalls: 12, Outcome: OutcomeCompletedUnverified},
			{ID: "w2", Runtime: "claude", Status: LifecycleInterrupted, StartedAt: &a2, EndedAt: &a3, ToolCalls: 3, Outcome: OutcomeInterrupted},
		},
		Instances:   []AgentInstance{{ID: "i1", Runtime: "codex", ThreadID: "t1"}, {ID: "i2", Runtime: "codex", ThreadID: "t2"}, {ID: "i3", Runtime: "claude", ThreadID: "t3"}},
		Assignments: []AgentAssignment{{ID: "a1", WorkItemID: "w1", AgentInstanceID: "i1"}, {ID: "a2", WorkItemID: "w1", AgentInstanceID: "i2"}, {ID: "a3", WorkItemID: "w2", AgentInstanceID: "i3"}},
		Requests: []EconomicRequest{
			{ID: "r1", WorkItemID: "w1", Runtime: "codex", Model: "gpt-5.6-sol", At: a1},
			{ID: "r2", WorkItemID: "w1", Runtime: "codex", Model: "gpt-5.6-sol", At: a2},
		},
		Artifacts: []ArtifactDelta{
			{WorkItemID: "w1", Path: "a.go", Kind: ArtifactCode, Additions: 10, At: a1},
			{WorkItemID: "w1", Path: "b.go", Kind: ArtifactCode, Additions: 20, At: a2},
		},
	}
	r := BuildActivityReport(dataset, "24h", "Asia/Shanghai", now)
	if r.Start.Hour() != 0 || r.Start.Location().String() != "Asia/Shanghai" {
		t.Fatalf("window start=%v", r.Start)
	}
	if r.Summary.WorkItems != 2 || r.Summary.AgentAssignments != 3 || r.Summary.AgentInstances != 3 || r.Summary.ModelRequests != 2 || r.Summary.ToolCalls != 15 {
		t.Fatalf("distinct grains collapsed: %+v", r.Summary)
	}
	if r.Summary.WallSeconds != 2*time.Hour.Seconds() || r.Summary.CumulativeSeconds != 2*time.Hour.Seconds() {
		t.Fatalf("time summary=%+v", r.Summary)
	}
	if got := r.Coverage["artifacts"]; got.ObservedN != 1 || got.EligibleN != 2 || got.State != "partial" {
		t.Fatalf("artifact coverage must count distinct work items, got %+v", got)
	}
	if r.Artifacts.ByKind[ArtifactCode].Files != 2 {
		t.Fatalf("artifact totals=%+v", r.Artifacts)
	}
	profiles := make(map[string]RuntimeProfile)
	for _, profile := range r.RuntimeProfiles {
		profiles[profile.Runtime] = profile
	}
	if got := profiles["codex"].Artifacts.ByKind[ArtifactCode]; got.Files != 2 || got.Additions != 30 {
		t.Fatalf("codex runtime artifacts=%+v", profiles["codex"].Artifacts)
	}
	if got := profiles["codex"].ArtifactCoverage; got.ObservedN != 1 || got.EligibleN != 1 || got.State != "complete" {
		t.Fatalf("codex artifact coverage=%+v", got)
	}
	if got := profiles["claude"].ArtifactCoverage; got.ObservedN != 0 || got.EligibleN != 1 || got.State != "missing" {
		t.Fatalf("claude artifact coverage=%+v", got)
	}
}

func TestBuildActivityReportPartitionsArtifactsByRuntime(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	report := BuildActivityReport(ActivityDataset{
		WorkItems: []ActivityWorkItem{
			{ID: "claude-work", Runtime: "claude", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end},
			{ID: "codex-work", Runtime: "codex", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end},
		},
		Artifacts: []ArtifactDelta{
			{WorkItemID: "claude-work", SourceToolID: "claude-edit", Path: "docs/design.md", Kind: ArtifactDoc, Operation: "modify", Additions: 12, Deletions: 3, WrittenLines: 15, At: start},
			{WorkItemID: "codex-work", SourceToolID: "codex-write", Path: "src/main.go", Kind: ArtifactCode, Operation: "create", Additions: 40, WrittenLines: 40, At: start},
		},
	}, "24h", "UTC", now)

	profiles := make(map[string]RuntimeProfile)
	for _, profile := range report.RuntimeProfiles {
		profiles[profile.Runtime] = profile
	}
	claudeDoc := profiles["claude"].Artifacts.ByKind[ArtifactDoc]
	codexCode := profiles["codex"].Artifacts.ByKind[ArtifactCode]
	if claudeDoc.Additions != 12 || claudeDoc.Deletions != 3 || claudeDoc.WrittenLines != 15 || claudeDoc.ModifiedFiles != 1 {
		t.Fatalf("claude doc output=%+v", claudeDoc)
	}
	if codexCode.Additions != 40 || codexCode.WrittenLines != 40 || codexCode.CreatedFiles != 1 {
		t.Fatalf("codex code output=%+v", codexCode)
	}
	if report.Artifacts.ByKind[ArtifactDoc].Additions != claudeDoc.Additions || report.Artifacts.ByKind[ArtifactCode].Additions != codexCode.Additions {
		t.Fatalf("global and runtime artifact projections diverged: global=%+v profiles=%+v", report.Artifacts, profiles)
	}
}

func TestRuntimeResourceYieldUsesAuditablePortfolioFacts(t *testing.T) {
	profile := RuntimeProfile{
		ModelRequests: 4, ActiveSeconds: 2 * time.Hour.Seconds(),
		Artifacts: ArtifactTotals{ByKind: map[ArtifactKind]LineTotals{
			ArtifactCode: {WrittenLines: 120}, ArtifactTest: {WrittenLines: 30}, ArtifactDoc: {WrittenLines: 50},
		}},
	}
	yield := buildRuntimeResourceYield(profile, 2_000, 4, map[string]float64{"USD": 10}, 3)
	if yield.ReviewableWrittenLines != 200 || yield.RequestOutputTokens != 2_000 {
		t.Fatalf("raw yield facts=%+v", yield)
	}
	if yield.WrittenLinesPerThousandOutputTokens == nil || *yield.WrittenLinesPerThousandOutputTokens != 100 {
		t.Fatalf("lines/1K output tokens=%v", yield.WrittenLinesPerThousandOutputTokens)
	}
	if yield.WrittenLinesPerActiveHour == nil || *yield.WrittenLinesPerActiveHour != 100 {
		t.Fatalf("lines/active hour=%v", yield.WrittenLinesPerActiveHour)
	}
	if yield.APIEquivalentPerThousandWrittenLines == nil || yield.APIEquivalentPerThousandWrittenLines.Amount != 50 || yield.APIEquivalentPerThousandWrittenLines.Currency != "USD" {
		t.Fatalf("API equivalent/1K lines=%+v", yield.APIEquivalentPerThousandWrittenLines)
	}
	if yield.CostCoverage.State != "partial" || yield.CostCoverage.ObservedN != 3 || yield.CostCoverage.EligibleN != 4 || yield.TokenCoverage.State != "complete" {
		t.Fatalf("resource coverage=%+v", yield)
	}
}

func TestAssignmentLifecycleKeepsScreenshotGrainsDistinct(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, loc)
	start, end := now.Add(-time.Hour), now
	dataset := ActivityDataset{WorkItems: []ActivityWorkItem{{ID: "w", Runtime: "codex", Status: LifecycleCompleted, StartedAt: &start, EndedAt: &end}}}
	for i := 0; i < 29; i++ {
		dataset.Instances = append(dataset.Instances, AgentInstance{ID: "i" + string(rune('a'+i)), Runtime: "codex"})
	}
	for i := 0; i < 60; i++ {
		status := LifecycleCompleted
		var started *time.Time = &start
		switch {
		case i >= 56:
			status, started = LifecycleNeverStarted, nil
		case i >= 54:
			status = LifecycleInterrupted
		}
		dataset.Assignments = append(dataset.Assignments, AgentAssignment{ID: "a" + strconv.Itoa(i), WorkItemID: "w", AgentInstanceID: dataset.Instances[i%29].ID, Status: status, StartedAt: started})
	}
	report := BuildActivityReport(dataset, "24h", "Asia/Shanghai", now)
	a := report.Summary.AssignmentLifecycle
	if report.Summary.AgentInstances != 29 || report.Summary.AgentAssignments != 60 || a.Submitted != 60 || a.Started != 56 || a.Completed != 54 || a.Interrupted != 2 || a.NeverStarted != 4 {
		t.Fatalf("four grains collapsed: summary=%+v", report.Summary)
	}
}
