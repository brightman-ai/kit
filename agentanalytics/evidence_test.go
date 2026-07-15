package agentanalytics

import (
	"testing"
	"time"
)

func TestRuntimeCompleteDoesNotOverrideFailingOracle(t *testing.T) {
	now := time.Now()
	profile := TaskProfile{Oracle: "test"}
	got := ResolveOutcome(LifecycleCompleted, profile, []OutcomeEvidence{{OracleKind: "test", Status: OutcomeVerifiedFail, At: now}})
	if got.Status != OutcomeVerifiedFail || !got.Eligible {
		t.Fatalf("resolution=%+v, want eligible verified_fail", got)
	}
	got = ResolveOutcome(LifecycleCompleted, profile, nil)
	if got.Status != OutcomeCompletedUnverified || got.Eligible {
		t.Fatalf("runtime complete without evidence=%+v", got)
	}
}

func TestArtifactClassificationAndSeparateLineTotals(t *testing.T) {
	kind, excluded, _ := ClassifyArtifact("frontend/src/view.test.ts")
	if kind != ArtifactTest || excluded {
		t.Fatalf("test artifact classified as %s excluded=%v", kind, excluded)
	}
	_, excluded, reason := ClassifyArtifact("frontend/dist/app.js")
	if !excluded || reason != "generated_or_vendor" {
		t.Fatalf("dist must be excluded: excluded=%v reason=%q", excluded, reason)
	}
	totals := AggregateArtifacts([]ArtifactDelta{
		{Kind: ArtifactCode, Additions: 100, Deletions: 80, Attribution: AttributionProviderPatch},
		{Kind: ArtifactDoc, Additions: 20, Deletions: 3, Attribution: AttributionUnknown},
	})
	if totals.ByKind[ArtifactCode].Additions != 100 || totals.ByKind[ArtifactCode].Deletions != 80 {
		t.Fatalf("code additions/deletions were netted: %+v", totals.ByKind[ArtifactCode])
	}
	if totals.Unattributed.Files != 1 || totals.Unattributed.Additions != 20 {
		t.Fatalf("unattributed=%+v", totals.Unattributed)
	}
}

func TestArtifactTotalsUseUniqueFilesAndKeepWrittenLinesSeparate(t *testing.T) {
	totals := AggregateArtifacts([]ArtifactDelta{
		{Path: "src/a.go", Kind: ArtifactCode, Operation: "modify", Additions: 2, Deletions: 1, WrittenLines: 2, Attribution: AttributionProviderPatch},
		{Path: "src/a.go", Kind: ArtifactCode, Operation: "modify", Additions: 3, Deletions: 3, WrittenLines: 3, Attribution: AttributionProviderPatch},
	})
	code := totals.ByKind[ArtifactCode]
	if code.Files != 1 || code.ModifiedFiles != 1 || code.Additions != 5 || code.Deletions != 4 || code.WrittenLines != 5 || totals.Events != 2 {
		t.Fatalf("artifact totals=%+v all=%+v", code, totals)
	}
	adds, deletes := ChangedLineCounts("keep\nold\ntail\n", "keep\nnew\nextra\ntail\n")
	if adds != 2 || deletes != 1 {
		t.Fatalf("changed lines add=%d delete=%d", adds, deletes)
	}
}

func TestToolExecutionAggregationExcludesInterruptedLatency(t *testing.T) {
	two, four, fake := 2.0, 4.0, 999.0
	summary := AggregateToolExecutions([]ToolExecution{
		{Status: ToolExecutionCompleted, DurationSeconds: &two},
		{Status: ToolExecutionError, DurationSeconds: &four},
		{Status: ToolExecutionInterrupted, DurationSeconds: &fake},
	})
	if summary.Calls != 3 || summary.Completed != 1 || summary.Errors != 1 || summary.Interrupted != 1 || summary.TotalDurationSeconds != 6 || summary.AverageDurationSeconds == nil || *summary.AverageDurationSeconds != 3 {
		t.Fatalf("tool summary=%+v", summary)
	}
}

func TestToolAndArtifactAggregatesUpsertStableToolIdentity(t *testing.T) {
	two := 2.0
	summary := AggregateToolExecutions([]ToolExecution{
		{ID: "tool-1", Status: ToolExecutionOpen},
		{ID: "tool-1", Status: ToolExecutionCompleted, DurationSeconds: &two},
	})
	if summary.Calls != 1 || summary.Completed != 1 || summary.Open != 0 || summary.TotalDurationSeconds != 2 {
		t.Fatalf("split tool rows were multiplied: %+v", summary)
	}
	totals := AggregateArtifacts([]ArtifactDelta{
		{SourceToolID: "tool-1", Path: "a.go", Kind: ArtifactCode, Additions: 1, Attribution: AttributionProviderPatch},
		{SourceToolID: "tool-1", Path: "a.go", Kind: ArtifactCode, Additions: 1, Attribution: AttributionProviderPatch},
	})
	if totals.Events != 1 || totals.ByKind[ArtifactCode].Additions != 1 {
		t.Fatalf("split artifact rows were multiplied: %+v", totals)
	}
	multi := AggregateArtifacts([]ArtifactDelta{
		{ID: "tool-1:artifact:0", SourceToolID: "tool-1", Path: "a.go", Kind: ArtifactCode, Additions: 2, Attribution: AttributionProviderPatch},
		{ID: "tool-1:artifact:1", SourceToolID: "tool-1", Path: "b.go", Kind: ArtifactCode, Additions: 3, Attribution: AttributionProviderPatch},
	})
	if multi.Events != 2 || multi.ByKind[ArtifactCode].Files != 2 || multi.ByKind[ArtifactCode].Additions != 5 {
		t.Fatalf("multi-file tool artifacts collapsed: %+v", multi)
	}
}

func TestAttentionSeparatesRequiredGateFromRework(t *testing.T) {
	base := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	started, accepted := base.Add(time.Second), base.Add(10*time.Minute)
	s := SummarizeExperience(base, &started, &accepted, []InteractionEvent{
		{Kind: InteractionPermission, Attention: AttentionRequired, At: base.Add(time.Minute)},
		{Kind: InteractionCorrection, Attention: AttentionAvoidable, At: base.Add(3 * time.Minute)},
		{Kind: InteractionProgress, Attention: AttentionNeutral, At: base.Add(4 * time.Minute)},
	})
	if s.RequiredAttention != 1 || s.AvoidableAttention != 1 || s.NeutralAttention != 1 {
		t.Fatalf("attention=%+v", s)
	}
	if s.FirstVisibleProgress == nil || *s.FirstVisibleProgress != 4*time.Minute {
		t.Fatalf("first visible progress=%v", s.FirstVisibleProgress)
	}
}

func TestGenerationRateUnavailableWithoutTiming(t *testing.T) {
	if got := GenerationTokensPerSecond(100, nil, nil); got != nil {
		t.Fatalf("missing timing became a fake speed: %v", *got)
	}
}

func TestTaskProfileUsesOpeningIntentOnlyAndLeavesUnknownRiskUnknown(t *testing.T) {
	p := ProfileOpeningIntent("/repo", "修复复制截图报错")
	if p.TaskClass != "bug" || p.Oracle != "test" || p.Project != "/repo" || p.Risk != "" || p.ScopeBand != "" {
		t.Fatalf("profile=%+v", p)
	}
	if p.Comparable() {
		t.Fatal("incomplete risk/scope was made comparison-eligible")
	}
}
