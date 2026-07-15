package agentanalytics

import "time"

type ToolExecutionStatus string

const (
	ToolExecutionOpen        ToolExecutionStatus = "open"
	ToolExecutionCompleted   ToolExecutionStatus = "completed"
	ToolExecutionError       ToolExecutionStatus = "error"
	ToolExecutionInterrupted ToolExecutionStatus = "interrupted"
	ToolExecutionUnknown     ToolExecutionStatus = "unknown"
)

// ToolExecution is a provider-neutral, immutable tool_use→tool_result fact.
// Open is a live append state; Unknown is a terminal run whose result evidence
// is missing. Neither may be mislabeled interrupted or enter latency averages.
type ToolExecution struct {
	ID              string              `json:"id"`
	WorkItemID      string              `json:"work_item_id,omitempty"`
	Runtime         string              `json:"runtime"`
	Name            string              `json:"name"`
	Status          ToolExecutionStatus `json:"status"`
	StartedAt       *time.Time          `json:"started_at,omitempty"`
	EndedAt         *time.Time          `json:"ended_at,omitempty"`
	DurationSeconds *float64            `json:"duration_seconds,omitempty"`
	SourceRef       string              `json:"source_ref,omitempty"`
}

type ToolExecutionSummary struct {
	Calls                  int            `json:"calls"`
	Completed              int            `json:"completed"`
	Errors                 int            `json:"errors"`
	Interrupted            int            `json:"interrupted"`
	Open                   int            `json:"open"`
	Unknown                int            `json:"unknown"`
	TotalDurationSeconds   float64        `json:"total_duration_seconds"`
	AverageDurationSeconds *float64       `json:"average_duration_seconds,omitempty"`
	TimingCoverage         ReportCoverage `json:"timing_coverage"`
}

func AggregateToolExecutions(values []ToolExecution) ToolExecutionSummary {
	out := ToolExecutionSummary{}
	timed := 0
	// Provider transcripts may repeat/split one tool_use across rows. Stable
	// identity is the aggregate boundary; terminal/richer facts replace open
	// observations instead of multiplying calls.
	byID := make(map[string]ToolExecution, len(values))
	withoutID := make([]ToolExecution, 0)
	for _, value := range values {
		if value.ID == "" {
			withoutID = append(withoutID, value)
			continue
		}
		if prior, ok := byID[value.ID]; !ok || preferToolFact(value, prior) {
			byID[value.ID] = value
		}
	}
	deduped := make([]ToolExecution, 0, len(byID)+len(withoutID))
	for _, value := range byID {
		deduped = append(deduped, value)
	}
	deduped = append(deduped, withoutID...)
	for _, value := range deduped {
		out.Calls++
		switch value.Status {
		case ToolExecutionCompleted:
			out.Completed++
		case ToolExecutionError:
			out.Errors++
		case ToolExecutionInterrupted:
			out.Interrupted++
		case ToolExecutionOpen:
			out.Open++
		case ToolExecutionUnknown:
			out.Unknown++
		}
		// Errors with a result are completed observations too. Interrupted
		// calls have no result and are deliberately excluded.
		if (value.Status == ToolExecutionCompleted || value.Status == ToolExecutionError) && value.DurationSeconds != nil && *value.DurationSeconds >= 0 {
			out.TotalDurationSeconds += *value.DurationSeconds
			timed++
		}
	}
	if timed > 0 {
		average := out.TotalDurationSeconds / float64(timed)
		out.AverageDurationSeconds = &average
	}
	eligible := out.Completed + out.Errors
	out.TimingCoverage = coverage(timed, eligible, []string{"provider tool_use/tool_result timestamps"}, "tool_timing_missing")
	return out
}

func preferToolFact(candidate, prior ToolExecution) bool {
	if toolStatusRank(candidate.Status) != toolStatusRank(prior.Status) {
		return toolStatusRank(candidate.Status) > toolStatusRank(prior.Status)
	}
	if candidate.DurationSeconds != nil && prior.DurationSeconds == nil {
		return true
	}
	return candidate.EndedAt != nil && prior.EndedAt == nil
}

func toolStatusRank(status ToolExecutionStatus) int {
	switch status {
	case ToolExecutionCompleted, ToolExecutionError:
		return 4
	case ToolExecutionInterrupted:
		return 3
	case ToolExecutionUnknown:
		return 2
	case ToolExecutionOpen:
		return 1
	default:
		return 0
	}
}
