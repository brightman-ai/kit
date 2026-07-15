package agentanalytics

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DetailFilter struct {
	Project   string `json:"project,omitempty"`
	TaskClass string `json:"task_class,omitempty"`
	Risk      string `json:"risk,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type DetailFilterOptions struct {
	Projects    []string `json:"projects"`
	TaskClasses []string `json:"task_classes"`
	Risks       []string `json:"risks"`
	Outcomes    []string `json:"outcomes"`
	Runtimes    []string `json:"runtimes"`
}

type DetailAssignment struct {
	ID              string          `json:"id"`
	AgentInstanceID string          `json:"agent_instance_id"`
	Attempt         int             `json:"attempt"`
	Status          LifecycleStatus `json:"status"`
}

type DetailRequest struct {
	ID            string `json:"id"`
	Model         string `json:"model,omitempty"`
	Effort        string `json:"effort,omitempty"`
	ServiceTier   string `json:"service_tier,omitempty"`
	OutputTokens  int64  `json:"output_tokens"`
	APIEquivalent *Money `json:"api_equivalent,omitempty"`
	CostComplete  bool   `json:"cost_complete"`
}

type DetailTask struct {
	ActivityWorkItem
	AgentInstances int                `json:"agent_instances"`
	Assignments    []DetailAssignment `json:"assignments"`
	Requests       []DetailRequest    `json:"requests"`
	Artifacts      []ArtifactDelta    `json:"artifacts"`
	Diagnostics    []string           `json:"diagnostics,omitempty"`
}

type ActivityDetailReport struct {
	SchemaVersion string              `json:"schema_version"`
	Report        ActivityReport      `json:"report"`
	Filters       DetailFilterOptions `json:"filters"`
	Tasks         []DetailTask        `json:"tasks"`
	NextCursor    string              `json:"next_cursor,omitempty"`
	Metrics       []MetricDefinition  `json:"metrics"`
}

// BuildActivityDetail applies one filter at the WorkItem grain, rebuilds every
// dependent section from that filtered set, and pages only the trace rows. This
// prevents a stale comparison/winner from surviving a cohort filter change.
func BuildActivityDetail(dataset ActivityDataset, window, timezone string, now time.Time, filter DetailFilter) ActivityDetailReport {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc, timezone = time.UTC, "UTC"
	}
	start, end := activityWindow(window, now.In(loc))
	options := detailOptions(dataset.WorkItems, start, end)
	selected := make(map[string]ActivityWorkItem)
	for _, work := range dataset.WorkItems {
		if !workItemOverlaps(work, start, end) || !matchesDetailFilter(work, filter) {
			continue
		}
		selected[work.ID] = work
	}
	filtered := ActivityDataset{IngestDiagnostics: append([]string(nil), dataset.IngestDiagnostics...), Projection: dataset.Projection}
	for _, work := range selected {
		filtered.WorkItems = append(filtered.WorkItems, work)
	}
	instanceIDs := make(map[string]struct{})
	for _, assignment := range dataset.Assignments {
		if _, ok := selected[assignment.WorkItemID]; !ok {
			continue
		}
		filtered.Assignments = append(filtered.Assignments, assignment)
		instanceIDs[assignment.AgentInstanceID] = struct{}{}
	}
	for _, instance := range dataset.Instances {
		if _, ok := instanceIDs[instance.ID]; ok {
			filtered.Instances = append(filtered.Instances, instance)
		}
	}
	for _, request := range dataset.Requests {
		if request.WorkItemID != "" {
			if _, ok := selected[request.WorkItemID]; !ok {
				continue
			}
		} else if filter.Project != "" || filter.TaskClass != "" || filter.Risk != "" || filter.Outcome != "" || (filter.Runtime != "" && request.Runtime != filter.Runtime) {
			continue
		}
		filtered.Requests = append(filtered.Requests, request)
	}
	for _, artifact := range dataset.Artifacts {
		if _, ok := selected[artifact.WorkItemID]; ok {
			filtered.Artifacts = append(filtered.Artifacts, artifact)
		}
	}
	for _, tool := range dataset.Tools {
		if tool.WorkItemID != "" {
			if _, ok := selected[tool.WorkItemID]; !ok {
				continue
			}
		} else if filter.Project != "" || filter.TaskClass != "" || filter.Risk != "" || filter.Outcome != "" || (filter.Runtime != "" && tool.Runtime != filter.Runtime) {
			continue
		}
		filtered.Tools = append(filtered.Tools, tool)
	}

	report := BuildActivityReport(filtered, window, timezone, now)
	tasks := buildDetailTasks(filtered)
	offset := decodeDetailCursor(filter.Cursor)
	if offset > len(tasks) {
		offset = len(tasks)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	pageEnd := offset + limit
	if pageEnd > len(tasks) {
		pageEnd = len(tasks)
	}
	detail := ActivityDetailReport{
		SchemaVersion: "agent-detail.v1", Report: report, Filters: options,
		Tasks: tasks[offset:pageEnd], Metrics: MetricRegistry(),
	}
	if pageEnd < len(tasks) {
		detail.NextCursor = encodeDetailCursor(pageEnd)
	}
	return detail
}

func matchesDetailFilter(work ActivityWorkItem, filter DetailFilter) bool {
	return (filter.Project == "" || work.Project == filter.Project) &&
		(filter.TaskClass == "" || work.TaskProfile.TaskClass == filter.TaskClass) &&
		(filter.Risk == "" || work.TaskProfile.Risk == filter.Risk) &&
		(filter.Outcome == "" || string(work.Outcome) == filter.Outcome) &&
		(filter.Runtime == "" || work.Runtime == filter.Runtime)
}

func detailOptions(items []ActivityWorkItem, start, end time.Time) DetailFilterOptions {
	projects, classes, risks, outcomes, runtimes := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, work := range items {
		if !workItemOverlaps(work, start, end) {
			continue
		}
		addDetailOption(projects, work.Project)
		addDetailOption(classes, work.TaskProfile.TaskClass)
		addDetailOption(risks, work.TaskProfile.Risk)
		addDetailOption(outcomes, string(work.Outcome))
		addDetailOption(runtimes, work.Runtime)
	}
	return DetailFilterOptions{
		Projects: sortedDetailOptions(projects), TaskClasses: sortedDetailOptions(classes),
		Risks: sortedDetailOptions(risks), Outcomes: sortedDetailOptions(outcomes), Runtimes: sortedDetailOptions(runtimes),
	}
}

func addDetailOption(values map[string]struct{}, value string) {
	if strings.TrimSpace(value) != "" {
		values[value] = struct{}{}
	}
}

func sortedDetailOptions(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func buildDetailTasks(dataset ActivityDataset) []DetailTask {
	byID := make(map[string]*DetailTask, len(dataset.WorkItems))
	for _, work := range dataset.WorkItems {
		// Collections are part of the JSON contract, not an implementation detail.
		// Keep them as [] instead of null so every consumer has one stable shape.
		copy := DetailTask{
			ActivityWorkItem: work,
			Assignments:      make([]DetailAssignment, 0),
			Requests:         make([]DetailRequest, 0),
			Artifacts:        make([]ArtifactDelta, 0),
		}
		if work.Outcome == OutcomeCompletedUnverified {
			copy.Diagnostics = []string{"applicable_outcome_evidence_missing"}
		}
		byID[work.ID] = &copy
	}
	instanceSets := make(map[string]map[string]struct{})
	for _, assignment := range dataset.Assignments {
		task := byID[assignment.WorkItemID]
		if task == nil {
			continue
		}
		task.Assignments = append(task.Assignments, DetailAssignment{ID: assignment.ID, AgentInstanceID: assignment.AgentInstanceID, Attempt: assignment.Attempt, Status: assignment.Status})
		if instanceSets[assignment.WorkItemID] == nil {
			instanceSets[assignment.WorkItemID] = make(map[string]struct{})
		}
		instanceSets[assignment.WorkItemID][assignment.AgentInstanceID] = struct{}{}
	}
	for _, request := range dataset.Requests {
		task := byID[request.WorkItemID]
		if task == nil {
			continue
		}
		task.Requests = append(task.Requests, DetailRequest{ID: request.ID, Model: request.Model, Effort: request.Effort, ServiceTier: request.ServiceTier, OutputTokens: request.OutputTokens, APIEquivalent: request.APIEquivalent, CostComplete: request.CostComplete})
	}
	for _, artifact := range dataset.Artifacts {
		if task := byID[artifact.WorkItemID]; task != nil {
			task.Artifacts = append(task.Artifacts, artifact)
		}
	}
	out := make([]DetailTask, 0, len(byID))
	for id, task := range byID {
		task.AgentInstances = len(instanceSets[id])
		out = append(out, *task)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := activityDetailTime(out[i].ActivityWorkItem), activityDetailTime(out[j].ActivityWorkItem)
		if ai.Equal(aj) {
			return out[i].ID > out[j].ID
		}
		return ai.After(aj)
	})
	return out
}

func activityDetailTime(work ActivityWorkItem) time.Time {
	if work.EndedAt != nil {
		return *work.EndedAt
	}
	if work.StartedAt != nil {
		return *work.StartedAt
	}
	return time.Time{}
}

func encodeDetailCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeDetailCursor(cursor string) int {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	offset, err := strconv.Atoi(string(decoded))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}
