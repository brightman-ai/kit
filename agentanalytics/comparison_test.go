package agentanalytics

import (
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func TestDifferentTaskProfilesNeverProduceWinner(t *testing.T) {
	a := CohortProfile{CohortKey: "project|bug|s|low|test", Runtime: "codex", EligibleN: 20, VCR: estimateRate(18, 20)}
	b := CohortProfile{CohortKey: "project|architecture|xl|high|review", Runtime: "claude", EligibleN: 20, VCR: estimateRate(18, 20)}
	d := CompareCohorts(a, b, DefaultComparisonPolicy)
	if d.Status != "not_comparable" || d.Recommendation != "" {
		t.Fatalf("selection bias guard failed: %+v", d)
	}
}

func TestQualityGatePreventsFastButWorseRecommendation(t *testing.T) {
	fast, slow := 5*time.Minute, 10*time.Minute
	a := CohortProfile{CohortKey: "same", Runtime: "a", EligibleN: 100, VCR: estimateRate(60, 100), TTAVMedian: &fast}
	b := CohortProfile{CohortKey: "same", Runtime: "b", EligibleN: 100, VCR: estimateRate(90, 100), TTAVMedian: &slow}
	d := CompareCohorts(a, b, DefaultComparisonPolicy)
	if d.Status != "quality_lower" || d.Recommendation != "" {
		t.Fatalf("faster but worse was recommended: %+v", d)
	}
}

func TestNoninferiorFasterCanBeRecommended(t *testing.T) {
	fast, slow := 5*time.Minute, 10*time.Minute
	a := CohortProfile{CohortKey: "same", Runtime: "a", EligibleN: 500, VCR: estimateRate(460, 500), TTAVMedian: &fast}
	b := CohortProfile{CohortKey: "same", Runtime: "b", EligibleN: 500, VCR: estimateRate(450, 500), TTAVMedian: &slow}
	d := CompareCohorts(a, b, DefaultComparisonPolicy)
	if d.Status != "recommended" || d.Recommendation != "faster" {
		t.Fatalf("decision=%+v", d)
	}
}

func TestMixedModelOutcomeIsNotCapabilityEvidence(t *testing.T) {
	if _, ok := ModelCapabilityEligible([]string{"gpt-5.6-sol", "gpt-5.6-luna"}); ok {
		t.Fatal("mixed-model work item credited to a model")
	}
	if model, ok := ModelCapabilityEligible([]string{"gpt-5.6-sol", "gpt-5.6-sol"}); !ok || model != "gpt-5.6-sol" {
		t.Fatalf("single model eligibility=(%q,%v)", model, ok)
	}
}

func TestBuildCohortProfilesKeepsCurrencySeparate(t *testing.T) {
	profile := TaskProfile{Project: "p", TaskClass: "bug", ScopeBand: "s", Risk: "low", Oracle: "test"}
	now := time.Now()
	accepted := now.Add(time.Minute)
	profiles := BuildCohortProfiles([]PerformanceObservation{
		{WorkItemID: "1", Runtime: "codex", Profile: profile, Outcome: OutcomeVerifiedPass, FirstPass: boolPtr(true), SubmittedAt: now, AcceptedAt: &accepted, Cost: &Money{Amount: 2, Currency: "USD"}},
		{WorkItemID: "2", Runtime: "codex", Profile: profile, Outcome: OutcomeHumanAccepted, FirstPass: boolPtr(false), SubmittedAt: now, AcceptedAt: &accepted, Cost: &Money{Amount: 10, Currency: "CNY"}},
	})
	if len(profiles) != 1 || profiles[0].CostPerAccepted["USD"] != 1 || profiles[0].CostPerAccepted["CNY"] != 5 {
		t.Fatalf("currency aggregation=%+v", profiles)
	}
}

func TestPartialTaskProfileIsNotComparable(t *testing.T) {
	profiles := BuildCohortProfiles([]PerformanceObservation{{
		WorkItemID: "w", Runtime: "codex", Profile: TaskProfile{Project: "p", Source: "transcript.cwd"}, Outcome: OutcomeVerifiedPass,
	}})
	if len(profiles) != 0 {
		t.Fatalf("post-hoc incomplete profile entered comparison: %+v", profiles)
	}
}
