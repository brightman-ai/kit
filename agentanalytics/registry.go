// Package agentanalytics owns provider-neutral evidence and rebuildable
// projections for agent execution. Provider transcripts remain the fact source;
// this package never writes conclusions back into them.
package agentanalytics

// MetricDefinition is the machine-readable metric dictionary consumed by API
// field documentation and UI explanations. Formula is descriptive; executable
// aggregation lives beside the domain types and is covered by parity tests.
type MetricDefinition struct {
	ID               string `json:"id"`
	Grain            string `json:"grain"`
	Formula          string `json:"formula"`
	Unit             string `json:"unit"`
	Eligibility      string `json:"eligibility"`
	Timezone         string `json:"timezone"`
	Provenance       string `json:"provenance"`
	Coverage         string `json:"coverage"`
	Aggregation      string `json:"aggregation"`
	ComparisonPolicy string `json:"comparison_policy"`
}

var metricRegistry = []MetricDefinition{
	{"work_items", "work_item", "count(distinct work_item_id)", "tasks", "identity observed", "local calendar window by interval overlap", "AgentRun", "identified / observed", "count", "activity only"},
	{"agent_assignments", "assignment", "count(distinct assignment_id)", "submissions", "work_item join observed", "event timestamp", "spawn/resume events", "joined / observed", "count", "activity only"},
	{"agent_instances", "agent_instance", "count(distinct agent_instance_id)", "agents", "stable thread identity observed", "instance interval", "root/session and spawn metadata", "identified / observed", "count", "activity only"},
	{"model_requests", "model_request", "count(distinct request_id)", "requests", "request identity observed", "request timestamp", "provider usage event", "identified / observed", "count", "resource only"},
	{"wall_active_seconds", "window", "duration(union(active intervals ∩ window))", "seconds", "interval timing observed", "requested IANA timezone", "assignment/instance intervals", "timed / observed", "interval union", "same cohort only"},
	{"cumulative_active_seconds", "window", "sum(duration(active interval ∩ window))", "agent-seconds", "interval timing observed", "requested IANA timezone", "assignment/instance intervals", "timed / observed", "sum", "same cohort only"},
	{"average_concurrency", "window", "cumulative_active_seconds / wall_active_seconds", "ratio", "wall_active_seconds > 0", "requested IANA timezone", "derived from active intervals", "timed / observed", "ratio of sums", "not a speedup claim"},
	{"generation_tokens_per_second", "model_request", "output_tokens / generation_seconds", "tokens/second", "first_token_at and ended_at observed; duration > 0", "request timestamps", "provider timing", "timed requests / output requests", "weighted by generation duration", "request/model performance, not capability"},
	{"verified_completion_rate", "eligible work_item cohort", "verified_pass / eligible", "ratio", "task profile + applicable outcome oracle", "completion timestamp", "OutcomeEvidence", "oracle-observed / eligible", "ratio with interval", "same project/task/scope/risk cohort"},
	{"first_pass_rate", "eligible work_item cohort", "accepted_without_rework / eligible", "ratio", "acceptance and rework evidence observed", "acceptance timestamp", "OutcomeEvidence + InteractionEvent", "decision-observed / eligible", "ratio with interval", "same cohort only"},
	{"ttav_median_seconds", "accepted work_item cohort", "median(accepted_at - submitted_at)", "seconds", "accepted_at and submitted_at observed", "requested IANA timezone", "lifecycle + outcome evidence", "timed / accepted", "median", "same cohort only"},
	{"ttav_p75_seconds", "accepted work_item cohort", "p75(accepted_at - submitted_at)", "seconds", "accepted_at and submitted_at observed", "requested IANA timezone", "lifecycle + outcome evidence", "timed / accepted", "nearest-rank p75", "same cohort only"},
	{"cost_per_accepted", "accepted work_item cohort", "sum(Money by currency) / accepted", "currency/task", "exact price and accepted outcome", "request time + acceptance window", "PricedRequest + OutcomeEvidence", "priced accepted / accepted", "ratio by currency", "same cohort; currencies never mixed"},
	{"attention_per_accepted", "accepted work_item cohort", "avoidable_attention_events / accepted", "events/task", "interaction classification observed", "event timestamp", "InteractionEvent", "classified / observed", "ratio", "required gates excluded"},
	{"artifact_additions", "artifact_delta", "sum(additions) by kind and attribution", "lines", "artifact evidence observed", "artifact timestamp", "provider patch or exclusive baseline", "attributed / observed", "sum additions separately", "activity only; never a capability score"},
	{"artifact_deletions", "artifact_delta", "sum(deletions) by kind and attribution", "lines", "artifact evidence observed", "artifact timestamp", "provider patch or exclusive baseline", "attributed / observed", "sum deletions separately", "activity only; do not net against additions"},
}

// MetricRegistry returns a defensive copy so callers cannot mutate the SSOT.
func MetricRegistry() []MetricDefinition {
	out := make([]MetricDefinition, len(metricRegistry))
	copy(out, metricRegistry)
	return out
}
