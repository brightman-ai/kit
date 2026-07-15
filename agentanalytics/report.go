package agentanalytics

import (
	"sort"
	"strings"
	"time"

	"github.com/brightman-ai/kit/transcript"
)

type ActivityWorkItem struct {
	ID           string          `json:"id"`
	Runtime      string          `json:"runtime"`
	Project      string          `json:"project,omitempty"`
	TaskProfile  TaskProfile     `json:"task_profile"`
	SourceRef    string          `json:"source_ref,omitempty"`
	Status       LifecycleStatus `json:"status"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	EndedAt      *time.Time      `json:"ended_at,omitempty"`
	OutputTokens int64           `json:"output_tokens"`
	ToolCalls    int             `json:"tool_calls"`
	Outcome      OutcomeStatus   `json:"outcome"`
}

type EconomicRequest struct {
	ID                              string    `json:"id"`
	WorkItemID                      string    `json:"work_item_id,omitempty"`
	Runtime                         string    `json:"runtime"`
	Model                           string    `json:"model,omitempty"`
	Effort                          string    `json:"effort,omitempty"`
	ServiceTier                     string    `json:"service_tier,omitempty"`
	At                              time.Time `json:"at"`
	OutputTokens                    int64     `json:"output_tokens"`
	TokenComplete                   bool      `json:"token_complete"`
	GenerationRate                  *float64  `json:"generation_tokens_per_second,omitempty"`
	GenerationDurationSeconds       *float64  `json:"generation_duration_seconds,omitempty"`
	ObservedResponseRate            *float64  `json:"observed_response_tokens_per_second,omitempty"`
	ObservedResponseDurationSeconds *float64  `json:"observed_response_duration_seconds,omitempty"`
	TTFTSeconds                     *float64  `json:"ttft_seconds,omitempty"`
	ObservedFirstResponseSeconds    *float64  `json:"observed_first_response_seconds,omitempty"`
	APIEquivalent                   *Money    `json:"api_equivalent,omitempty"`
	CostComplete                    bool      `json:"cost_complete"`
	Credits                         *float64  `json:"credits,omitempty"`
	CreditComplete                  bool      `json:"credit_complete"`
	FastMultiplier                  *float64  `json:"fast_multiplier,omitempty"`
}

// requestTimingAggregate is the one aggregation rule for every request
// dimension (runtime today, exact model below). Keeping the additive
// numerator/denominator here prevents views from averaging request rates.
type requestTimingAggregate struct {
	generationTokens, responseTokens     int64
	generationSeconds, responseSeconds   float64
	generationObserved, responseObserved int
	ttftObserved, firstResponseObserved  int
	ttftSeconds, firstResponseSeconds    []float64
	eligible                             int
}

func (aggregate *requestTimingAggregate) observe(request EconomicRequest) {
	aggregate.eligible++
	if request.GenerationDurationSeconds != nil && *request.GenerationDurationSeconds > 0 {
		aggregate.generationObserved++
		aggregate.generationTokens += request.OutputTokens
		aggregate.generationSeconds += *request.GenerationDurationSeconds
	}
	if request.ObservedResponseDurationSeconds != nil && *request.ObservedResponseDurationSeconds > 0 {
		aggregate.responseObserved++
		aggregate.responseTokens += request.OutputTokens
		aggregate.responseSeconds += *request.ObservedResponseDurationSeconds
	}
	if request.TTFTSeconds != nil && *request.TTFTSeconds >= 0 {
		aggregate.ttftObserved++
		aggregate.ttftSeconds = append(aggregate.ttftSeconds, *request.TTFTSeconds)
	}
	if request.ObservedFirstResponseSeconds != nil && *request.ObservedFirstResponseSeconds >= 0 {
		aggregate.firstResponseObserved++
		aggregate.firstResponseSeconds = append(aggregate.firstResponseSeconds, *request.ObservedFirstResponseSeconds)
	}
}

func (aggregate requestTimingAggregate) generationRate() *float64 {
	if aggregate.generationSeconds <= 0 {
		return nil
	}
	value := float64(aggregate.generationTokens) / aggregate.generationSeconds
	return &value
}

func (aggregate requestTimingAggregate) responseRate() *float64 {
	if aggregate.responseSeconds <= 0 {
		return nil
	}
	value := float64(aggregate.responseTokens) / aggregate.responseSeconds
	return &value
}

func (aggregate requestTimingAggregate) generationCoverage() ReportCoverage {
	return coverage(aggregate.generationObserved, aggregate.eligible, []string{"provider first-token/end timing"}, "generation_timing_missing")
}

func (aggregate requestTimingAggregate) responseCoverage() ReportCoverage {
	return coverage(aggregate.responseObserved, aggregate.eligible, []string{"preceding causal event", "assistant completion"}, "response_timing_missing")
}

func (aggregate requestTimingAggregate) ttftCoverage() ReportCoverage {
	return coverage(aggregate.ttftObserved, aggregate.eligible, []string{"provider request-start/first-token timing"}, "provider_ttft_missing")
}

func (aggregate requestTimingAggregate) firstResponseCoverage() ReportCoverage {
	return coverage(aggregate.firstResponseObserved, aggregate.eligible, []string{"preceding causal event", "first assistant transcript event"}, "first_response_timing_missing")
}

type ActivityDataset struct {
	WorkItems         []ActivityWorkItem
	Assignments       []AgentAssignment
	Instances         []AgentInstance
	Requests          []EconomicRequest
	RequestFacts      []transcript.ModelRequestUsage
	Artifacts         []ArtifactDelta
	Tools             []ToolExecution
	IngestDiagnostics []string
	Projection        ProjectionObservability
}

// ProjectionObservability describes the materialized-view boundary. It keeps
// parser/index freshness separate from metric coverage, so a complete-looking
// chart can never hide a stale or partially parsed projection.
type ProjectionObservability struct {
	State               string     `json:"state"`
	Mode                string     `json:"mode"`
	RefreshedAt         time.Time  `json:"refreshed_at"`
	SourceHighWatermark *time.Time `json:"source_high_watermark,omitempty"`
	SourceFiles         int        `json:"source_files"`
	ChangedFiles        int        `json:"changed_files"`
	IndexSchema         string     `json:"index_schema,omitempty"`
	Diagnostics         []string   `json:"diagnostics,omitempty"`
}

type ReportObservability struct {
	Projection ProjectionObservability   `json:"projection"`
	Stages     map[string]ReportCoverage `json:"stages"`
}

type ReportCoverage struct {
	State       string   `json:"state"`
	ObservedN   int      `json:"observed_n"`
	EligibleN   int      `json:"eligible_n"`
	Ratio       *float64 `json:"ratio,omitempty"`
	Provenance  []string `json:"provenance"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

type ActivitySummary struct {
	WorkItems           int                        `json:"work_items"`
	Submitted           int                        `json:"submitted"`
	Started             int                        `json:"started"`
	Completed           int                        `json:"completed"`
	Interrupted         int                        `json:"interrupted"`
	Errors              int                        `json:"errors"`
	NeverStarted        int                        `json:"never_started"`
	Open                int                        `json:"open"`
	VerifiedPass        int                        `json:"verified_pass"`
	VerifiedFail        int                        `json:"verified_fail"`
	HumanRework         int                        `json:"human_rework"`
	CompletedUnverified int                        `json:"completed_unverified"`
	WallSeconds         float64                    `json:"wall_seconds"`
	CumulativeSeconds   float64                    `json:"cumulative_seconds"`
	AverageConcurrency  *float64                   `json:"average_concurrency,omitempty"`
	AgentInstances      int                        `json:"agent_instances"`
	AgentAssignments    int                        `json:"agent_assignments"`
	ModelRequests       int                        `json:"model_requests"`
	ToolCalls           int                        `json:"tool_calls"`
	AssignmentLifecycle AssignmentLifecycleSummary `json:"assignment_lifecycle"`
	DelegatedLifecycle  AssignmentLifecycleSummary `json:"delegated_lifecycle"`
}

type AssignmentLifecycleSummary struct {
	Submitted    int `json:"submitted"`
	Started      int `json:"started"`
	Completed    int `json:"completed"`
	Interrupted  int `json:"interrupted"`
	Errors       int `json:"errors"`
	NeverStarted int `json:"never_started"`
	Open         int `json:"open"`
}

// RuntimeResourceYield keeps the raw portfolio facts next to each derived ratio.
// It is descriptive for one runtime/window, never a capability score or a causal
// attribution of a request's cost to a particular file.
type RuntimeResourceYield struct {
	FormulaVersion                       string         `json:"formula_version"`
	ReviewableWrittenLines               int64          `json:"reviewable_written_lines"`
	RequestOutputTokens                  int64          `json:"request_output_tokens"`
	APIEquivalent                        *Money         `json:"api_equivalent,omitempty"`
	CostCoverage                         ReportCoverage `json:"cost_coverage"`
	TokenCoverage                        ReportCoverage `json:"token_coverage"`
	WrittenLinesPerThousandOutputTokens  *float64       `json:"written_lines_per_thousand_output_tokens,omitempty"`
	WrittenLinesPerActiveHour            *float64       `json:"written_lines_per_active_hour,omitempty"`
	APIEquivalentPerThousandWrittenLines *Money         `json:"api_equivalent_per_thousand_written_lines,omitempty"`
	Diagnostics                          []string       `json:"diagnostics"`
}

type RuntimeProfile struct {
	Runtime                            string               `json:"runtime"`
	WorkItems                          int                  `json:"work_items"`
	Completed                          int                  `json:"completed"`
	Interrupted                        int                  `json:"interrupted"`
	Errors                             int                  `json:"errors"`
	ActiveSeconds                      float64              `json:"active_seconds"`
	OutputTokens                       int64                `json:"output_tokens"`
	ModelRequests                      int                  `json:"model_requests"`
	AgentInstances                     int                  `json:"agent_instances"`
	ToolCalls                          int                  `json:"tool_calls"`
	GenerationTokensPerSecond          *float64             `json:"generation_tokens_per_second,omitempty"`
	ObservedResponseTokensPerSecond    *float64             `json:"observed_response_tokens_per_second,omitempty"`
	TTFTMedianSeconds                  *float64             `json:"ttft_median_seconds,omitempty"`
	ObservedFirstResponseMedianSeconds *float64             `json:"observed_first_response_median_seconds,omitempty"`
	GenerationSpeedCoverage            ReportCoverage       `json:"generation_speed_coverage"`
	ResponseSpeedCoverage              ReportCoverage       `json:"response_speed_coverage"`
	TTFTCoverage                       ReportCoverage       `json:"ttft_coverage"`
	FirstResponseCoverage              ReportCoverage       `json:"first_response_coverage"`
	SpeedCoverage                      ReportCoverage       `json:"speed_coverage"` // compatibility alias for generation coverage
	Artifacts                          ArtifactTotals       `json:"artifacts"`
	ArtifactCoverage                   ReportCoverage       `json:"artifact_coverage"`
	ResourceYield                      RuntimeResourceYield `json:"resource_yield"`
	Tools                              ToolExecutionSummary `json:"tools"`
}

type ModelCostRow struct {
	Runtime                         string         `json:"runtime"`
	Model                           string         `json:"model"`
	RequestN                        int            `json:"request_n"`
	Efforts                         []string       `json:"efforts,omitempty"`
	ServiceTiers                    []string       `json:"service_tiers,omitempty"`
	Cost                            *Money         `json:"cost,omitempty"`
	CostCoverage                    ReportCoverage `json:"cost_coverage"`
	Credits                         *float64       `json:"credits,omitempty"`
	CreditCoverage                  ReportCoverage `json:"credit_coverage"`
	FastMultipliers                 []float64      `json:"fast_multipliers,omitempty"`
	ObservedResponseTokensPerSecond *float64       `json:"observed_response_tokens_per_second,omitempty"`
	ObservedResponseOutputTokens    int64          `json:"observed_response_output_tokens"`
	ObservedResponseDurationSeconds float64        `json:"observed_response_duration_seconds"`
	ResponseSpeedCoverage           ReportCoverage `json:"response_speed_coverage"`
}

type ActivityReport struct {
	SchemaVersion   string                    `json:"schema_version"`
	Window          string                    `json:"window"`
	Timezone        string                    `json:"timezone"`
	Start           time.Time                 `json:"start"`
	End             time.Time                 `json:"end"`
	GeneratedAt     time.Time                 `json:"generated_at"`
	Summary         ActivitySummary           `json:"summary"`
	RuntimeProfiles []RuntimeProfile          `json:"runtime_profiles"`
	TopCostModels   map[string][]ModelCostRow `json:"top_cost_models"`
	Artifacts       ArtifactTotals            `json:"artifacts"`
	Tools           ToolExecutionSummary      `json:"tools"`
	Health          HealthAssessment          `json:"health"`
	Coverage        map[string]ReportCoverage `json:"coverage"`
	Observability   ReportObservability       `json:"observability"`
	Comparisons     []ComparisonDecision      `json:"comparisons"`
}

func BuildActivityReport(dataset ActivityDataset, window, timezone string, now time.Time) ActivityReport {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
		timezone = "UTC"
	}
	start, end := activityWindow(window, now.In(loc))
	report := ActivityReport{
		SchemaVersion: "agent-report.v1", Window: window, Timezone: timezone,
		Start: start, End: end, GeneratedAt: now, TopCostModels: make(map[string][]ModelCostRow),
		RuntimeProfiles: make([]RuntimeProfile, 0), Comparisons: make([]ComparisonDecision, 0),
		Coverage: make(map[string]ReportCoverage), Observability: ReportObservability{Stages: make(map[string]ReportCoverage)},
	}
	workInWindow := make(map[string]ActivityWorkItem)
	intervalsByRuntime := make(map[string][]Interval)
	profiles := make(map[string]*RuntimeProfile)
	for _, work := range dataset.WorkItems {
		if !workItemOverlaps(work, start, end) {
			continue
		}
		workInWindow[work.ID] = work
		report.Summary.WorkItems++
		report.Summary.Submitted++
		p := profiles[work.Runtime]
		if p == nil {
			p = &RuntimeProfile{Runtime: work.Runtime}
			profiles[work.Runtime] = p
		}
		p.WorkItems++
		p.OutputTokens += work.OutputTokens
		p.ToolCalls += work.ToolCalls
		report.Summary.ToolCalls += work.ToolCalls
		if work.StartedAt != nil {
			report.Summary.Started++
		}
		switch work.Status {
		case LifecycleCompleted:
			report.Summary.Completed++
			p.Completed++
		case LifecycleInterrupted:
			report.Summary.Interrupted++
			p.Interrupted++
		case LifecycleError:
			report.Summary.Errors++
			p.Errors++
		case LifecycleNeverStarted:
			report.Summary.NeverStarted++
		case LifecycleOpen, LifecycleStarted:
			report.Summary.Open++
		}
		switch work.Outcome {
		case OutcomeVerifiedPass, OutcomeHumanAccepted:
			report.Summary.VerifiedPass++
		case OutcomeVerifiedFail:
			report.Summary.VerifiedFail++
		case OutcomeHumanRework:
			report.Summary.HumanRework++
		case OutcomeCompletedUnverified:
			report.Summary.CompletedUnverified++
		}
		if work.StartedAt != nil && work.EndedAt != nil {
			intervalsByRuntime[work.Runtime] = append(intervalsByRuntime[work.Runtime], Interval{Start: *work.StartedAt, End: *work.EndedAt})
		}
	}
	allIntervals := make([]Interval, 0)
	for runtime, intervals := range intervalsByRuntime {
		s := SummarizeIntervals(intervals, Interval{Start: start, End: end})
		profiles[runtime].ActiveSeconds = s.Cumulative.Seconds()
		allIntervals = append(allIntervals, intervals...)
	}
	timeSummary := SummarizeIntervals(allIntervals, Interval{Start: start, End: end})
	report.Summary.WallSeconds = timeSummary.Wall.Seconds()
	report.Summary.CumulativeSeconds = timeSummary.Cumulative.Seconds()
	report.Summary.AverageConcurrency = timeSummary.Concurrency

	instanceRuntime := make(map[string]string, len(dataset.Instances))
	for _, instance := range dataset.Instances {
		instanceRuntime[instance.ID] = instance.Runtime
	}
	windowInstances := make(map[string]struct{})
	for _, assignment := range dataset.Assignments {
		if _, ok := workInWindow[assignment.WorkItemID]; ok {
			report.Summary.AgentAssignments++
			observeAssignmentLifecycle(&report.Summary.AssignmentLifecycle, assignment)
			if !assignment.Root {
				observeAssignmentLifecycle(&report.Summary.DelegatedLifecycle, assignment)
			}
			if assignment.AgentInstanceID != "" {
				windowInstances[assignment.AgentInstanceID] = struct{}{}
			}
		}
	}
	report.Summary.AgentInstances = len(windowInstances)
	for instanceID := range windowInstances {
		if p := profiles[instanceRuntime[instanceID]]; p != nil {
			p.AgentInstances++
		}
	}

	type modelKey struct{ runtime, model string }
	type modelAggregate struct {
		row              ModelCostRow
		knownCost        map[string]float64
		knownCredits     float64
		priced, credited int
		efforts, tiers   map[string]struct{}
		fastMultipliers  map[float64]struct{}
		timing           requestTimingAggregate
	}
	models := make(map[modelKey]*modelAggregate)
	speeds := make(map[string]*requestTimingAggregate)
	requestOutputTokensByRuntime := make(map[string]int64)
	tokenCompleteByRuntime := make(map[string]int)
	for _, request := range dataset.Requests {
		if request.At.Before(start) || !request.At.Before(end) {
			continue
		}
		if request.WorkItemID != "" {
			if _, ok := workInWindow[request.WorkItemID]; !ok {
				continue
			}
		}
		report.Summary.ModelRequests++
		p := profiles[request.Runtime]
		if p == nil {
			p = &RuntimeProfile{Runtime: request.Runtime}
			profiles[request.Runtime] = p
		}
		p.ModelRequests++
		requestOutputTokensByRuntime[request.Runtime] += request.OutputTokens
		if request.TokenComplete {
			tokenCompleteByRuntime[request.Runtime]++
		}
		model := strings.TrimSpace(request.Model)
		k := modelKey{request.Runtime, model}
		agg := models[k]
		if agg == nil {
			agg = &modelAggregate{row: ModelCostRow{Runtime: request.Runtime, Model: model}, knownCost: make(map[string]float64), efforts: make(map[string]struct{}), tiers: make(map[string]struct{}), fastMultipliers: make(map[float64]struct{})}
			models[k] = agg
		}
		agg.row.RequestN++
		if request.Effort != "" {
			agg.efforts[request.Effort] = struct{}{}
		}
		if request.ServiceTier != "" {
			agg.tiers[request.ServiceTier] = struct{}{}
		}
		if request.CostComplete && request.APIEquivalent != nil {
			agg.knownCost[request.APIEquivalent.Currency] += request.APIEquivalent.Amount
			agg.priced++
		}
		if request.CreditComplete && request.Credits != nil {
			agg.knownCredits += *request.Credits
			agg.credited++
		}
		if request.FastMultiplier != nil {
			agg.fastMultipliers[*request.FastMultiplier] = struct{}{}
		}
		agg.timing.observe(request)
		speed := speeds[request.Runtime]
		if speed == nil {
			speed = &requestTimingAggregate{}
			speeds[request.Runtime] = speed
		}
		speed.observe(request)
	}

	toolsByRuntime := make(map[string][]ToolExecution)
	toolsInWindow := make([]ToolExecution, 0, len(dataset.Tools))
	for _, tool := range dataset.Tools {
		at := toolExecutionTime(tool)
		if at.IsZero() || at.Before(start) || !at.Before(end) {
			continue
		}
		if tool.WorkItemID != "" {
			if _, ok := workInWindow[tool.WorkItemID]; !ok {
				continue
			}
		}
		toolsInWindow = append(toolsInWindow, tool)
		toolsByRuntime[tool.Runtime] = append(toolsByRuntime[tool.Runtime], tool)
	}
	report.Tools = AggregateToolExecutions(toolsInWindow)
	if len(dataset.Tools) > 0 {
		report.Summary.ToolCalls = report.Tools.Calls
	}

	for runtime, p := range profiles {
		speed := speeds[runtime]
		if speed == nil {
			speed = &requestTimingAggregate{}
		}
		p.GenerationSpeedCoverage = speed.generationCoverage()
		p.ResponseSpeedCoverage = speed.responseCoverage()
		p.TTFTCoverage = speed.ttftCoverage()
		p.FirstResponseCoverage = speed.firstResponseCoverage()
		p.GenerationTokensPerSecond = speed.generationRate()
		p.ObservedResponseTokensPerSecond = speed.responseRate()
		p.TTFTMedianSeconds = medianFloat64(speed.ttftSeconds)
		p.ObservedFirstResponseMedianSeconds = medianFloat64(speed.firstResponseSeconds)
		p.SpeedCoverage = p.GenerationSpeedCoverage
		p.Tools = AggregateToolExecutions(toolsByRuntime[runtime])
		if len(toolsByRuntime[runtime]) > 0 {
			p.ToolCalls = p.Tools.Calls
		}
		report.RuntimeProfiles = append(report.RuntimeProfiles, *p)
	}
	sort.Slice(report.RuntimeProfiles, func(i, j int) bool { return report.RuntimeProfiles[i].Runtime < report.RuntimeProfiles[j].Runtime })

	rowsByRuntime := make(map[string][]ModelCostRow)
	knownCostByRuntime := make(map[string]map[string]float64)
	pricedByRuntime := make(map[string]int)
	pricedRequests := 0
	for _, agg := range models {
		agg.row.Efforts = sortedSet(agg.efforts)
		agg.row.ServiceTiers = sortedSet(agg.tiers)
		agg.row.CostCoverage = coverage(agg.priced, agg.row.RequestN, []string{"ModelRequestUsage", "PricingCatalog"}, "exact_price_missing")
		agg.row.CreditCoverage = coverage(agg.credited, agg.row.RequestN, []string{"ModelRequestUsage", "PricingCatalog.credit_schedule"}, "official_credit_schedule_or_speed_evidence_missing")
		agg.row.ObservedResponseTokensPerSecond = agg.timing.responseRate()
		agg.row.ObservedResponseOutputTokens = agg.timing.responseTokens
		agg.row.ObservedResponseDurationSeconds = agg.timing.responseSeconds
		agg.row.ResponseSpeedCoverage = agg.timing.responseCoverage()
		if agg.credited > 0 {
			value := agg.knownCredits
			agg.row.Credits = &value
		}
		for multiplier := range agg.fastMultipliers {
			agg.row.FastMultipliers = append(agg.row.FastMultipliers, multiplier)
		}
		sort.Float64s(agg.row.FastMultipliers)
		pricedRequests += agg.priced
		pricedByRuntime[agg.row.Runtime] += agg.priced
		if knownCostByRuntime[agg.row.Runtime] == nil {
			knownCostByRuntime[agg.row.Runtime] = make(map[string]float64)
		}
		for currency, amount := range agg.knownCost {
			knownCostByRuntime[agg.row.Runtime][currency] += amount
		}
		if len(agg.knownCost) == 1 {
			for currency, amount := range agg.knownCost {
				agg.row.Cost = &Money{Amount: amount, Currency: currency}
			}
		}
		rowsByRuntime[agg.row.Runtime] = append(rowsByRuntime[agg.row.Runtime], agg.row)
	}
	for runtime, rows := range rowsByRuntime {
		sort.Slice(rows, func(i, j int) bool { return modelCompositionLess(rows[i], rows[j]) })
		report.TopCostModels[runtime] = selectModelCompositionRows(rows, 3)
	}
	report.Coverage["identity"] = coverage(len(workInWindow), report.Summary.WorkItems, []string{"AgentRun"}, "")
	report.Coverage["outcome"] = coverage(report.Summary.VerifiedPass, report.Summary.WorkItems, []string{"OutcomeEvidence"}, "outcome_oracle_missing")
	responseObserved, responseEligible := 0, 0
	generationObserved, ttftObserved, firstResponseObserved := 0, 0, 0
	for _, speed := range speeds {
		responseObserved += speed.responseObserved
		generationObserved += speed.generationObserved
		ttftObserved += speed.ttftObserved
		firstResponseObserved += speed.firstResponseObserved
		responseEligible += speed.eligible
	}
	report.Coverage["response_speed"] = coverage(responseObserved, responseEligible, []string{"preceding causal event", "assistant completion"}, "response_timing_missing")
	report.Coverage["generation_speed"] = coverage(generationObserved, responseEligible, []string{"provider first-token/end timing"}, "generation_timing_missing")
	report.Coverage["ttft"] = coverage(ttftObserved, responseEligible, []string{"provider request-start/first-token timing"}, "provider_ttft_missing")
	report.Coverage["first_response"] = coverage(firstResponseObserved, responseEligible, []string{"preceding causal event", "first assistant transcript event"}, "first_response_timing_missing")
	report.Coverage["tool_timing"] = report.Tools.TimingCoverage
	artifactWorkItems := make(map[string]struct{})
	artifactWorkItemsByRuntime := make(map[string]map[string]struct{})
	artifactsByRuntime := make(map[string][]ArtifactDelta)
	artifacts := make([]ArtifactDelta, 0, len(dataset.Artifacts))
	for _, artifact := range dataset.Artifacts {
		if artifact.At.Before(start) || !artifact.At.Before(end) {
			continue
		}
		if artifact.WorkItemID != "" {
			work, ok := workInWindow[artifact.WorkItemID]
			if !ok {
				continue
			}
			artifactWorkItems[artifact.WorkItemID] = struct{}{}
			if artifactWorkItemsByRuntime[work.Runtime] == nil {
				artifactWorkItemsByRuntime[work.Runtime] = make(map[string]struct{})
			}
			artifactWorkItemsByRuntime[work.Runtime][artifact.WorkItemID] = struct{}{}
			artifactsByRuntime[work.Runtime] = append(artifactsByRuntime[work.Runtime], artifact)
		}
		artifacts = append(artifacts, artifact)
	}
	report.Artifacts = AggregateArtifacts(artifacts)
	report.Coverage["artifacts"] = coverage(len(artifactWorkItems), report.Summary.WorkItems, []string{"ArtifactDelta"}, "artifact_evidence_partial")
	for i := range report.RuntimeProfiles {
		profile := &report.RuntimeProfiles[i]
		profile.Artifacts = AggregateArtifacts(artifactsByRuntime[profile.Runtime])
		profile.ArtifactCoverage = coverage(len(artifactWorkItemsByRuntime[profile.Runtime]), profile.WorkItems, []string{"ArtifactDelta", "ActivityWorkItem.runtime"}, "artifact_evidence_partial")
		profile.ResourceYield = buildRuntimeResourceYield(*profile, requestOutputTokensByRuntime[profile.Runtime], tokenCompleteByRuntime[profile.Runtime], knownCostByRuntime[profile.Runtime], pricedByRuntime[profile.Runtime])
	}
	report.Coverage["ingest"] = ReportCoverage{State: stateForDiagnostics(dataset.IngestDiagnostics), Provenance: []string{"provider transcripts"}, Diagnostics: dataset.IngestDiagnostics}
	projection := dataset.Projection
	if projection.RefreshedAt.IsZero() {
		projection.RefreshedAt = now
	}
	if projection.State == "" {
		projection.State = stateForDiagnostics(dataset.IngestDiagnostics)
	}
	if projection.Mode == "" {
		projection.Mode = "in_memory"
	}
	report.Observability.Projection = projection
	report.Observability.Stages["ingest"] = report.Coverage["ingest"]
	report.Observability.Stages["identity"] = report.Coverage["identity"]
	report.Observability.Stages["economics"] = coverage(pricedRequests, report.Summary.ModelRequests, []string{"ModelRequestUsage", "PricingCatalog"}, "exact_price_missing")
	report.Observability.Stages["evidence"] = report.Coverage["outcome"]
	comparable, comparisonEvidence := 0, 0
	for _, work := range workInWindow {
		if work.TaskProfile.Comparable() {
			comparable++
			if work.Outcome == OutcomeVerifiedPass || work.Outcome == OutcomeVerifiedFail || work.Outcome == OutcomeHumanAccepted || work.Outcome == OutcomeHumanRework {
				comparisonEvidence++
			}
		}
	}
	report.Observability.Stages["comparison"] = coverage(comparisonEvidence, comparable, []string{"TaskProfile", "OutcomeEvidence"}, "comparable_cohort_or_oracle_missing")
	report.Health = EvaluateHealth(report)
	return report
}

func buildRuntimeResourceYield(profile RuntimeProfile, requestOutputTokens int64, tokenComplete int, knownCosts map[string]float64, priced int) RuntimeResourceYield {
	writtenLines := profile.Artifacts.ByKind[ArtifactCode].WrittenLines +
		profile.Artifacts.ByKind[ArtifactTest].WrittenLines +
		profile.Artifacts.ByKind[ArtifactDoc].WrittenLines
	yield := RuntimeResourceYield{
		FormulaVersion: "runtime-resource-yield.v1", ReviewableWrittenLines: writtenLines,
		RequestOutputTokens: requestOutputTokens,
		CostCoverage:        coverage(priced, profile.ModelRequests, []string{"ModelRequestUsage", "PricingCatalog"}, "exact_price_missing"),
		TokenCoverage:       coverage(tokenComplete, profile.ModelRequests, []string{"ModelRequestUsage.output_tokens"}, "request_token_evidence_missing"),
		Diagnostics:         []string{"runtime_window_portfolio_ratio_not_causal"},
	}
	if requestOutputTokens > 0 && writtenLines > 0 {
		value := float64(writtenLines) * 1_000 / float64(requestOutputTokens)
		yield.WrittenLinesPerThousandOutputTokens = &value
	}
	if profile.ActiveSeconds > 0 && writtenLines > 0 {
		value := float64(writtenLines) * 3_600 / profile.ActiveSeconds
		yield.WrittenLinesPerActiveHour = &value
	}
	if len(knownCosts) == 1 {
		for currency, amount := range knownCosts {
			yield.APIEquivalent = &Money{Amount: amount, Currency: currency}
			if writtenLines > 0 {
				yield.APIEquivalentPerThousandWrittenLines = &Money{Amount: amount * 1_000 / float64(writtenLines), Currency: currency}
			}
		}
	} else if len(knownCosts) > 1 {
		yield.Diagnostics = append(yield.Diagnostics, "mixed_currency_not_scalar")
	}
	return yield
}

// selectModelCompositionRows keeps the cost-ranked known models compact while
// retaining the unknown-model evidence bucket even when it falls outside the
// ranking. Unknown is a data-quality fact, not a low-cost model that Top-N may
// silently discard.
func selectModelCompositionRows(rows []ModelCostRow, knownLimit int) []ModelCostRow {
	selected := make([]ModelCostRow, 0, knownLimit+1)
	var unknown *ModelCostRow
	for i := range rows {
		if strings.TrimSpace(rows[i].Model) == "" {
			if unknown == nil {
				row := rows[i]
				unknown = &row
			}
			continue
		}
		if len(selected) < knownLimit {
			selected = append(selected, rows[i])
		}
	}
	if unknown != nil {
		selected = append(selected, *unknown)
	}
	return selected
}

func modelCompositionLess(left, right ModelCostRow) bool {
	if left.Cost != nil && right.Cost == nil {
		return true
	}
	if left.Cost == nil && right.Cost != nil {
		return false
	}
	if left.Cost != nil && right.Cost != nil && left.Cost.Currency == right.Cost.Currency && left.Cost.Amount != right.Cost.Amount {
		return left.Cost.Amount > right.Cost.Amount
	}
	if left.RequestN != right.RequestN {
		return left.RequestN > right.RequestN
	}
	return left.Model < right.Model
}

func toolExecutionTime(tool ToolExecution) time.Time {
	if tool.EndedAt != nil {
		return *tool.EndedAt
	}
	if tool.StartedAt != nil {
		return *tool.StartedAt
	}
	return time.Time{}
}

func observeAssignmentLifecycle(summary *AssignmentLifecycleSummary, assignment AgentAssignment) {
	summary.Submitted++
	if assignment.StartedAt != nil {
		summary.Started++
	}
	switch assignment.Status {
	case LifecycleCompleted:
		summary.Completed++
	case LifecycleInterrupted:
		summary.Interrupted++
	case LifecycleError:
		summary.Errors++
	case LifecycleNeverStarted:
		summary.NeverStarted++
	default:
		summary.Open++
	}
}

func medianFloat64(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	value := ordered[middle]
	if len(ordered)%2 == 0 {
		value = (ordered[middle-1] + ordered[middle]) / 2
	}
	return &value
}

func activityWindow(window string, now time.Time) (time.Time, time.Time) {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	days := 1
	switch window {
	case "7d":
		days = 7
	case "14d":
		days = 14
	case "30d":
		days = 30
	}
	return today.AddDate(0, 0, -(days - 1)), today.AddDate(0, 0, 1)
}

func workItemOverlaps(work ActivityWorkItem, start, end time.Time) bool {
	at := work.StartedAt
	if at == nil {
		at = work.EndedAt
	}
	if at == nil {
		return false
	}
	workEnd := end
	if work.EndedAt != nil {
		workEnd = *work.EndedAt
	}
	return workEnd.After(start) && at.Before(end)
}

func coverage(observed, eligible int, provenance []string, missingReason string) ReportCoverage {
	c := ReportCoverage{ObservedN: observed, EligibleN: eligible, Provenance: provenance}
	if eligible == 0 {
		c.State = "missing"
		if missingReason != "" {
			c.Diagnostics = []string{missingReason}
		}
		return c
	}
	ratio := float64(observed) / float64(eligible)
	c.Ratio = &ratio
	switch {
	case observed == eligible:
		c.State = "complete"
	case observed == 0:
		c.State = "missing"
	default:
		c.State = "partial"
	}
	if observed < eligible && missingReason != "" {
		c.Diagnostics = []string{missingReason}
	}
	return c
}

func stateForDiagnostics(diagnostics []string) string {
	if len(diagnostics) > 0 {
		return "partial"
	}
	return "complete"
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
