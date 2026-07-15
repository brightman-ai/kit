package agentanalytics

import (
	"fmt"
	"sort"
	"time"
)

type LifecycleStatus string

const (
	LifecycleSubmitted    LifecycleStatus = "submitted"
	LifecycleStarted      LifecycleStatus = "started"
	LifecycleCompleted    LifecycleStatus = "completed"
	LifecycleInterrupted  LifecycleStatus = "interrupted"
	LifecycleError        LifecycleStatus = "error"
	LifecycleNeverStarted LifecycleStatus = "never_started"
	LifecycleOpen         LifecycleStatus = "open"
)

// OutcomeStatus is deliberately separate from runtime lifecycle. Completed is
// not success until an applicable oracle supplies evidence.
type OutcomeStatus string

const (
	OutcomeOpen                OutcomeStatus = "open"
	OutcomeCompletedUnverified OutcomeStatus = "completed_unverified"
	OutcomeVerifiedPass        OutcomeStatus = "verified_pass"
	OutcomeVerifiedFail        OutcomeStatus = "verified_fail"
	OutcomeHumanAccepted       OutcomeStatus = "human_accepted"
	OutcomeHumanRework         OutcomeStatus = "human_rework"
	OutcomeInterrupted         OutcomeStatus = "interrupted"
	OutcomeError               OutcomeStatus = "error"
)

type WorkItem struct {
	ID        string
	Runtime   string
	SessionID string
	Status    LifecycleStatus
	StartedAt *time.Time
	EndedAt   *time.Time
}

type AgentAssignment struct {
	ID              string
	WorkItemID      string
	AgentInstanceID string
	// Root marks the lifecycle mirror of the WorkItem. Health only evaluates
	// delegated assignments so one root failure is never counted twice.
	Root        bool
	Attempt     int
	Status      LifecycleStatus
	SubmittedAt time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
}

type AgentInstance struct {
	ID               string
	Runtime          string
	ThreadID         string
	ParentInstanceID string
	Depth            int
	SourceRef        string
}

type ModelRequestRef struct {
	ID              string
	WorkItemID      string
	AssignmentID    string
	AgentInstanceID string
}

type WorkGraph struct {
	WorkItems   []WorkItem
	Assignments []AgentAssignment
	Instances   []AgentInstance
	Requests    []ModelRequestRef
}

// Validate enforces cross-context identity invariants without provider fields.
func (g WorkGraph) Validate() error {
	work := make(map[string]struct{}, len(g.WorkItems))
	instances := make(map[string]AgentInstance, len(g.Instances))
	assignments := make(map[string]AgentAssignment, len(g.Assignments))
	rootSourceOwner := make(map[string]string)
	for _, w := range g.WorkItems {
		if w.ID == "" {
			return fmt.Errorf("work_item.id_missing")
		}
		if _, duplicate := work[w.ID]; duplicate {
			return fmt.Errorf("work_item.duplicate:%s", w.ID)
		}
		work[w.ID] = struct{}{}
	}
	for _, instance := range g.Instances {
		if instance.ID == "" || instance.ThreadID == "" {
			return fmt.Errorf("agent_instance.identity_missing")
		}
		if _, duplicate := instances[instance.ID]; duplicate {
			return fmt.Errorf("agent_instance.duplicate:%s", instance.ID)
		}
		instances[instance.ID] = instance
		if instance.Depth == 0 && instance.SourceRef != "" {
			if owner, exists := rootSourceOwner[instance.SourceRef]; exists && owner != instance.ID {
				return fmt.Errorf("identity.cross_root_collision:%s", instance.SourceRef)
			}
			rootSourceOwner[instance.SourceRef] = instance.ID
		}
	}
	for _, instance := range g.Instances {
		if instance.ParentInstanceID != "" {
			if _, ok := instances[instance.ParentInstanceID]; !ok {
				return fmt.Errorf("agent_instance.parent_missing:%s", instance.ID)
			}
		}
	}
	for _, assignment := range g.Assignments {
		if assignment.ID == "" {
			return fmt.Errorf("assignment.id_missing")
		}
		if _, duplicate := assignments[assignment.ID]; duplicate {
			return fmt.Errorf("assignment.duplicate:%s", assignment.ID)
		}
		if _, ok := work[assignment.WorkItemID]; !ok {
			return fmt.Errorf("assignment.work_item_missing:%s", assignment.ID)
		}
		if _, ok := instances[assignment.AgentInstanceID]; !ok {
			return fmt.Errorf("assignment.instance_missing:%s", assignment.ID)
		}
		assignments[assignment.ID] = assignment
	}
	requests := make(map[string]struct{}, len(g.Requests))
	for _, request := range g.Requests {
		if request.ID == "" {
			return fmt.Errorf("request.id_missing")
		}
		if _, duplicate := requests[request.ID]; duplicate {
			return fmt.Errorf("request.duplicate:%s", request.ID)
		}
		if _, ok := work[request.WorkItemID]; !ok {
			return fmt.Errorf("request.work_item_missing:%s", request.ID)
		}
		assignment, ok := assignments[request.AssignmentID]
		if !ok || assignment.AgentInstanceID != request.AgentInstanceID {
			return fmt.Errorf("request.assignment_mismatch:%s", request.ID)
		}
		requests[request.ID] = struct{}{}
	}
	return nil
}

type Interval struct{ Start, End time.Time }

type IntervalSummary struct {
	Wall        time.Duration
	Cumulative  time.Duration
	Concurrency *float64
}

// SummarizeIntervals computes union wall time and sum agent time. Open or
// inverted intervals are excluded instead of becoming negative/zero evidence.
func SummarizeIntervals(intervals []Interval, window Interval) IntervalSummary {
	clipped := make([]Interval, 0, len(intervals))
	for _, iv := range intervals {
		start, end := iv.Start, iv.End
		if start.Before(window.Start) {
			start = window.Start
		}
		if end.After(window.End) {
			end = window.End
		}
		if start.IsZero() || end.IsZero() || !end.After(start) {
			continue
		}
		clipped = append(clipped, Interval{Start: start, End: end})
	}
	if len(clipped) == 0 {
		return IntervalSummary{}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].Start.Before(clipped[j].Start) })
	var cumulative time.Duration
	for _, iv := range clipped {
		cumulative += iv.End.Sub(iv.Start)
	}
	start, end := clipped[0].Start, clipped[0].End
	var wall time.Duration
	for _, iv := range clipped[1:] {
		if !iv.Start.After(end) {
			if iv.End.After(end) {
				end = iv.End
			}
			continue
		}
		wall += end.Sub(start)
		start, end = iv.Start, iv.End
	}
	wall += end.Sub(start)
	summary := IntervalSummary{Wall: wall, Cumulative: cumulative}
	if wall > 0 {
		value := float64(cumulative) / float64(wall)
		summary.Concurrency = &value
	}
	return summary
}
