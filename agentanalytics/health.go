package agentanalytics

import "fmt"

type HealthState string

const (
	HealthHealthy      HealthState = "healthy"
	HealthAttention    HealthState = "attention"
	HealthCritical     HealthState = "critical"
	HealthInsufficient HealthState = "insufficient_evidence"
)

const AgentHealthPolicyVersion = "agent-health.v1"

// HealthReason is an inspectable rule evaluation, not a score contribution.
// Threshold/minimum sample and coverage are carried with the conclusion so the
// frontend never has to recreate policy logic.
type HealthReason struct {
	Code        string      `json:"code"`
	State       HealthState `json:"state"`
	Message     string      `json:"message"`
	Observed    *float64    `json:"observed,omitempty"`
	Unit        string      `json:"unit,omitempty"`
	Comparator  string      `json:"comparator,omitempty"`
	Threshold   *float64    `json:"threshold,omitempty"`
	MinimumN    *int        `json:"minimum_n,omitempty"`
	EvidenceRef string      `json:"evidence_ref"`
	CoverageRef string      `json:"coverage_ref,omitempty"`
}

type HealthAxis struct {
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	State    HealthState    `json:"state"`
	Headline string         `json:"headline"`
	Reasons  []HealthReason `json:"reasons"`
}

type HealthAssessment struct {
	PolicyVersion string         `json:"policy_version"`
	State         HealthState    `json:"state"`
	Label         string         `json:"label"`
	Headline      string         `json:"headline"`
	Axes          []HealthAxis   `json:"axes"`
	Reasons       []HealthReason `json:"reasons"`
}

// EvaluateHealth applies categorical hard gates to normalized facts. A
// failure cannot be averaged away by tokens, duration, calls or LOC, and an
// incomplete projection can never produce a false "healthy" verdict.
func EvaluateHealth(report ActivityReport) HealthAssessment {
	axes := []HealthAxis{
		evaluateExecutionHealth(report),
		evaluateDeliveryHealth(report),
		evaluateEfficiencyHealth(report),
	}
	integrity := evaluateIntegrityHealth(report)
	state := HealthHealthy
	for _, axis := range axes {
		if healthRank(axis.State) > healthRank(state) {
			state = axis.State
		}
	}
	for _, reason := range integrity {
		if healthRank(reason.State) > healthRank(state) {
			state = reason.State
		}
	}

	// Integrity comes first among equal-severity insufficient reasons because it
	// qualifies every downstream axis. Critical/attention facts still lead.
	candidates := append([]HealthReason(nil), integrity...)
	for _, axis := range axes {
		candidates = append(candidates, axis.Reasons...)
	}
	reasons := make([]HealthReason, 0, 3)
	for _, desired := range []HealthState{HealthCritical, HealthAttention, HealthInsufficient} {
		for _, reason := range candidates {
			if reason.State == desired && len(reasons) < 3 {
				reasons = append(reasons, reason)
			}
		}
	}
	headline := "未发现需要处理的执行、交付或效率信号"
	if len(reasons) > 0 {
		headline = reasons[0].Message
	}
	return HealthAssessment{PolicyVersion: AgentHealthPolicyVersion, State: state, Label: healthLabel(state), Headline: headline, Axes: axes, Reasons: reasons}
}

func evaluateExecutionHealth(report ActivityReport) HealthAxis {
	reasons := make([]HealthReason, 0, 4)
	if report.Summary.Errors > 0 {
		reasons = append(reasons, positiveCountReason("task_errors", HealthCritical,
			fmt.Sprintf("发现 %d 个任务错误", report.Summary.Errors), report.Summary.Errors, "个", "summary.errors"))
	}
	// Root assignments mirror WorkItem lifecycle and are deliberately excluded;
	// only delegated work is an independent scheduling signal.
	if report.Summary.DelegatedLifecycle.Errors > 0 {
		reasons = append(reasons, positiveCountReason("delegated_assignment_errors", HealthCritical,
			fmt.Sprintf("发现 %d 次子 Agent 调度错误", report.Summary.DelegatedLifecycle.Errors), report.Summary.DelegatedLifecycle.Errors, "次", "summary.delegated_lifecycle.errors"))
	}
	if report.Summary.Interrupted > 0 {
		reasons = append(reasons, positiveCountReason("task_interrupted", HealthAttention,
			fmt.Sprintf("有 %d 个任务中断，已从完成耗时中排除", report.Summary.Interrupted), report.Summary.Interrupted, "个", "summary.interrupted"))
	}
	if report.Summary.DelegatedLifecycle.Interrupted > 0 {
		reasons = append(reasons, positiveCountReason("delegated_assignment_interrupted", HealthAttention,
			fmt.Sprintf("有 %d 次子 Agent 调度中断", report.Summary.DelegatedLifecycle.Interrupted), report.Summary.DelegatedLifecycle.Interrupted, "次", "summary.delegated_lifecycle.interrupted"))
	}
	if report.Summary.NeverStarted > 0 {
		reasons = append(reasons, positiveCountReason("task_never_started", HealthAttention,
			fmt.Sprintf("有 %d 个任务未启动", report.Summary.NeverStarted), report.Summary.NeverStarted, "个", "summary.never_started"))
	}
	if report.Summary.DelegatedLifecycle.NeverStarted > 0 {
		reasons = append(reasons, positiveCountReason("delegated_assignment_never_started", HealthAttention,
			fmt.Sprintf("有 %d 次子 Agent 调度未启动", report.Summary.DelegatedLifecycle.NeverStarted), report.Summary.DelegatedLifecycle.NeverStarted, "次", "summary.delegated_lifecycle.never_started"))
	}
	if report.Tools.Calls >= 5 {
		ratio := float64(report.Tools.Errors) / float64(report.Tools.Calls)
		threshold, minimumN := .10, 5
		if ratio >= threshold {
			reasons = append(reasons, HealthReason{Code: "tool_error_ratio", State: HealthAttention,
				Message: fmt.Sprintf("工具错误率 %.0f%%，建议检查失败调用", ratio*100), Observed: &ratio,
				Unit: "ratio", Comparator: ">=", Threshold: &threshold, MinimumN: &minimumN, EvidenceRef: "tools"})
		}
	}
	if report.Summary.WorkItems == 0 {
		reasons = append(reasons, missingReason("execution_facts_missing", "当前窗口没有可判定的任务事实", "summary.work_items", "coverage.identity"))
	}
	return makeHealthAxis("execution", "执行", reasons, "执行链路未发现错误或中断")
}

func evaluateDeliveryHealth(report ActivityReport) HealthAxis {
	reasons := make([]HealthReason, 0, 3)
	if report.Summary.VerifiedFail > 0 {
		reasons = append(reasons, positiveCountReason("verified_failure", HealthCritical,
			fmt.Sprintf("%d 项验证失败", report.Summary.VerifiedFail), report.Summary.VerifiedFail, "项", "summary.verified_fail"))
	}
	if report.Summary.HumanRework > 0 {
		reasons = append(reasons, positiveCountReason("human_rework", HealthCritical,
			fmt.Sprintf("%d 项被要求返工", report.Summary.HumanRework), report.Summary.HumanRework, "项", "summary.human_rework"))
	}
	if report.Summary.CompletedUnverified > 0 {
		observed, threshold := float64(report.Summary.CompletedUnverified), float64(0)
		reasons = append(reasons, HealthReason{Code: "completed_unverified", State: HealthInsufficient,
			Message:  fmt.Sprintf("%d 项完成但没有验收证据，不能判断交付健康", report.Summary.CompletedUnverified),
			Observed: &observed, Unit: "项", Comparator: ">", Threshold: &threshold,
			EvidenceRef: "summary.completed_unverified", CoverageRef: "coverage.outcome"})
	}
	if report.Summary.Completed == 0 {
		reasons = append(reasons, missingReason("delivery_facts_missing", "当前窗口没有已完成任务可评估交付", "summary.completed", "coverage.outcome"))
	}
	return makeHealthAxis("delivery", "交付", reasons, "已完成任务均有通过或验收证据")
}

func evaluateEfficiencyHealth(report ActivityReport) HealthAxis {
	reasons := make([]HealthReason, 0, 2)
	responseCoverage, ok := report.Coverage["response_speed"]
	if !ok || responseCoverage.EligibleN == 0 || responseCoverage.ObservedN == 0 {
		reasons = append(reasons, missingReason("response_timing_missing", "缺少可用响应区间，暂不能判断效率", "runtime_profiles.observed_response_tokens_per_second", "coverage.response_speed"))
		return makeHealthAxis("efficiency", "效率", reasons, "")
	}
	minimumCoverage := .80
	if responseCoverage.Ratio == nil || *responseCoverage.Ratio < minimumCoverage {
		ratio := float64(responseCoverage.ObservedN) / float64(responseCoverage.EligibleN)
		reasons = append(reasons, HealthReason{Code: "response_timing_partial", State: HealthInsufficient,
			Message: fmt.Sprintf("响应吞吐仅覆盖 %.0f%% 请求，证据不足", ratio*100), Observed: &ratio,
			Unit: "ratio", Comparator: ">=", Threshold: &minimumCoverage,
			EvidenceRef: "runtime_profiles.observed_response_tokens_per_second", CoverageRef: "coverage.response_speed"})
		return makeHealthAxis("efficiency", "效率", reasons, "")
	}

	minimumN := DefaultComparisonPolicy.MinimumEligibleN
	observed := float64(responseCoverage.ObservedN)
	reasons = append(reasons, HealthReason{Code: "cohort_baseline_missing", State: HealthInsufficient,
		Message: "响应吞吐已采集；缺少同任务 cohort 基线，暂不判断快慢", Observed: &observed, Unit: "请求",
		Comparator: ">=", MinimumN: &minimumN, EvidenceRef: "comparisons", CoverageRef: "observability.stages.comparison"})
	return makeHealthAxis("efficiency", "效率", reasons, "")
}

func evaluateIntegrityHealth(report ActivityReport) []HealthReason {
	projection := report.Observability.Projection
	if projection.State != "complete" {
		observed, threshold := float64(projection.ChangedFiles), float64(0)
		return []HealthReason{{Code: "projection_integrity_incomplete", State: HealthInsufficient,
			Message: "数据投影不完整，健康结论已降级", Observed: &observed, Unit: "变更文件",
			Comparator: "state ==", Threshold: &threshold, EvidenceRef: "observability.projection", CoverageRef: "coverage.ingest"}}
	}
	if ingest, ok := report.Coverage["ingest"]; ok && ingest.State != "complete" {
		return []HealthReason{missingReason("ingest_integrity_incomplete", "转录采集不完整，健康结论已降级", "observability.stages.ingest", "coverage.ingest")}
	}
	return nil
}

func makeHealthAxis(key, label string, reasons []HealthReason, healthyHeadline string) HealthAxis {
	state, headline := HealthHealthy, healthyHeadline
	for _, reason := range reasons {
		if healthRank(reason.State) > healthRank(state) {
			state, headline = reason.State, reason.Message
		}
	}
	if reasons == nil {
		reasons = make([]HealthReason, 0)
	}
	return HealthAxis{Key: key, Label: label, State: state, Headline: headline, Reasons: reasons}
}

func positiveCountReason(code string, state HealthState, message string, observed int, unit, ref string) HealthReason {
	value, threshold := float64(observed), float64(0)
	return HealthReason{Code: code, State: state, Message: message, Observed: &value, Unit: unit, Comparator: ">", Threshold: &threshold, EvidenceRef: ref}
}

func missingReason(code, message, evidenceRef, coverageRef string) HealthReason {
	value, threshold, minimumN := float64(0), float64(1), 1
	return HealthReason{Code: code, State: HealthInsufficient, Message: message, Observed: &value, Unit: "facts",
		Comparator: ">=", Threshold: &threshold, MinimumN: &minimumN, EvidenceRef: evidenceRef, CoverageRef: coverageRef}
}

func healthRank(state HealthState) int {
	switch state {
	case HealthCritical:
		return 3
	case HealthAttention:
		return 2
	case HealthInsufficient:
		return 1
	default:
		return 0
	}
}

func healthLabel(state HealthState) string {
	switch state {
	case HealthCritical:
		return "异常"
	case HealthAttention:
		return "需关注"
	case HealthInsufficient:
		return "证据不足"
	default:
		return "健康"
	}
}
