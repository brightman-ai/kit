package agentanalytics

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TaskProfile struct {
	Project    string  `json:"project"`
	TaskClass  string  `json:"task_class"`
	ScopeBand  string  `json:"scope_band"`
	Risk       string  `json:"risk"`
	Oracle     string  `json:"oracle"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

// ProfileOpeningIntent is a deliberately small pre-outcome classifier. It uses
// only the project and the human's opening intent; runtime duration, tokens and
// produced files can never leak into difficulty/cohort labels.
func ProfileOpeningIntent(project, intent string) TaskProfile {
	p := TaskProfile{Project: project, Source: "opening_intent.v1"}
	lower := strings.ToLower(strings.TrimSpace(intent))
	switch {
	case containsAny(lower, "bug", "fix", "修复", "报错", "故障"):
		p.TaskClass, p.Oracle, p.Confidence = "bug", "test", .75
	case containsAny(lower, "design", "architecture", "设计", "架构"):
		p.TaskClass, p.Oracle, p.Confidence = "design", "review", .7
	case containsAny(lower, "research", "investigate", "调研", "研究"):
		p.TaskClass, p.Oracle, p.Confidence = "research", "human_acceptance", .7
	case containsAny(lower, "doc", "readme", "文档"):
		p.TaskClass, p.Oracle, p.Confidence = "docs", "human_acceptance", .75
	default:
		p.TaskClass, p.Confidence = "general", .35
	}
	return p
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func (p TaskProfile) CohortKey() string {
	return strings.Join([]string{p.Project, p.TaskClass, p.ScopeBand, p.Risk, p.Oracle}, "|")
}

func (p TaskProfile) Comparable() bool {
	return strings.TrimSpace(p.Project) != "" && strings.TrimSpace(p.TaskClass) != "" &&
		strings.TrimSpace(p.ScopeBand) != "" && strings.TrimSpace(p.Risk) != "" && strings.TrimSpace(p.Oracle) != ""
}

type OutcomeEvidence struct {
	WorkItemID string        `json:"work_item_id"`
	Source     string        `json:"source"`
	OracleKind string        `json:"oracle_kind"`
	Status     OutcomeStatus `json:"status"`
	At         time.Time     `json:"at"`
	Ref        string        `json:"ref,omitempty"`
	Confidence float64       `json:"confidence"`
}

type OutcomeResolution struct {
	Status   OutcomeStatus    `json:"status"`
	Evidence *OutcomeEvidence `json:"evidence,omitempty"`
	Eligible bool             `json:"eligible"`
	Reason   string           `json:"reason,omitempty"`
}

// ResolveOutcome applies an explicit evidence precedence. Runtime completion is
// only completed_unverified; a failing applicable oracle always closes the VCR
// gate regardless of the runtime's optimistic lifecycle status.
func ResolveOutcome(lifecycle LifecycleStatus, profile TaskProfile, evidence []OutcomeEvidence) OutcomeResolution {
	ordered := append([]OutcomeEvidence(nil), evidence...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.After(ordered[j].At) })
	for _, desired := range []OutcomeStatus{OutcomeHumanRework, OutcomeVerifiedFail, OutcomeHumanAccepted, OutcomeVerifiedPass} {
		for i := range ordered {
			e := &ordered[i]
			if e.Status != desired || (profile.Oracle != "" && e.OracleKind != profile.Oracle && e.Status != OutcomeHumanAccepted && e.Status != OutcomeHumanRework) {
				continue
			}
			return OutcomeResolution{Status: e.Status, Evidence: e, Eligible: profile.Oracle != "" || e.Status == OutcomeHumanAccepted || e.Status == OutcomeHumanRework}
		}
	}
	switch lifecycle {
	case LifecycleCompleted:
		return OutcomeResolution{Status: OutcomeCompletedUnverified, Reason: "applicable_outcome_evidence_missing"}
	case LifecycleInterrupted:
		return OutcomeResolution{Status: OutcomeInterrupted, Reason: "runtime_interrupted"}
	case LifecycleError:
		return OutcomeResolution{Status: OutcomeError, Reason: "runtime_error"}
	default:
		return OutcomeResolution{Status: OutcomeOpen, Reason: "work_item_open"}
	}
}

type ArtifactKind string

const (
	ArtifactCode   ArtifactKind = "code"
	ArtifactTest   ArtifactKind = "test"
	ArtifactDoc    ArtifactKind = "doc"
	ArtifactConfig ArtifactKind = "config"
	ArtifactAsset  ArtifactKind = "asset"
	ArtifactOther  ArtifactKind = "other"
)

type AttributionState string

const (
	AttributionProviderPatch AttributionState = "provider_patch"
	AttributionExclusiveBase AttributionState = "exclusive_baseline"
	AttributionUnknown       AttributionState = "unattributed"
)

type ArtifactDelta struct {
	ID             string           `json:"id,omitempty"`
	SourceToolID   string           `json:"source_tool_id,omitempty"`
	WorkItemID     string           `json:"work_item_id,omitempty"`
	Path           string           `json:"path"`
	Kind           ArtifactKind     `json:"kind"`
	Operation      string           `json:"operation,omitempty"`
	Additions      int64            `json:"additions"`
	Deletions      int64            `json:"deletions"`
	WrittenLines   int64            `json:"written_lines"`
	ChangeCoverage string           `json:"change_coverage,omitempty"`
	Accepted       *bool            `json:"accepted,omitempty"`
	Attribution    AttributionState `json:"attribution"`
	At             time.Time        `json:"at"`
	Excluded       bool             `json:"excluded"`
	ExcludeReason  string           `json:"exclude_reason,omitempty"`
	Diagnostics    []string         `json:"diagnostics,omitempty"`
}

type ArtifactTotals struct {
	ByKind        map[ArtifactKind]LineTotals `json:"by_kind"`
	Unattributed  LineTotals                  `json:"unattributed"`
	ExcludedFiles int64                       `json:"excluded_files"`
	Events        int64                       `json:"events"`
}

type LineTotals struct {
	Additions     int64 `json:"additions"`
	Deletions     int64 `json:"deletions"`
	WrittenLines  int64 `json:"written_lines"`
	Files         int64 `json:"files"`
	CreatedFiles  int64 `json:"created_files"`
	ModifiedFiles int64 `json:"modified_files"`
}

func AggregateArtifacts(deltas []ArtifactDelta) ArtifactTotals {
	out := ArtifactTotals{ByKind: make(map[ArtifactKind]LineTotals)}
	filesByKind := make(map[ArtifactKind]map[string]struct{})
	createdByKind := make(map[ArtifactKind]map[string]struct{})
	modifiedByKind := make(map[ArtifactKind]map[string]struct{})
	unattributedFiles := make(map[string]struct{})
	excludedFiles := make(map[string]struct{})
	seenEvents := make(map[string]struct{})
	for _, d := range deltas {
		dedupID := d.ID
		if dedupID == "" {
			dedupID = d.SourceToolID // compatibility with pre-v9 single-artifact tool rows
		}
		if dedupID != "" {
			if _, duplicate := seenEvents[dedupID]; duplicate {
				continue
			}
			seenEvents[dedupID] = struct{}{}
		}
		out.Events++
		if d.Excluded {
			excludedFiles[d.Path] = struct{}{}
			continue
		}
		t := out.ByKind[d.Kind]
		t.Additions += d.Additions
		t.Deletions += d.Deletions
		t.WrittenLines += d.WrittenLines
		if filesByKind[d.Kind] == nil {
			filesByKind[d.Kind] = make(map[string]struct{})
			createdByKind[d.Kind] = make(map[string]struct{})
			modifiedByKind[d.Kind] = make(map[string]struct{})
		}
		filesByKind[d.Kind][d.Path] = struct{}{}
		switch d.Operation {
		case "create":
			createdByKind[d.Kind][d.Path] = struct{}{}
		case "modify":
			modifiedByKind[d.Kind][d.Path] = struct{}{}
		}
		out.ByKind[d.Kind] = t
		if d.Attribution == AttributionUnknown {
			out.Unattributed.Additions += d.Additions
			out.Unattributed.Deletions += d.Deletions
			out.Unattributed.WrittenLines += d.WrittenLines
			unattributedFiles[d.Path] = struct{}{}
		}
	}
	for kind, paths := range filesByKind {
		t := out.ByKind[kind]
		t.Files = int64(len(paths))
		t.CreatedFiles = int64(len(createdByKind[kind]))
		t.ModifiedFiles = int64(len(modifiedByKind[kind]))
		out.ByKind[kind] = t
	}
	out.Unattributed.Files = int64(len(unattributedFiles))
	out.ExcludedFiles = int64(len(excludedFiles))
	return out
}

// ChangedLineCounts returns deterministic gross line additions/deletions for a
// replacement fragment. Equal prefix/suffix lines are removed so a one-line
// value edit reports +1/-1, while unchanged context is not counted.
func ChangedLineCounts(oldText, newText string) (additions, deletions int64) {
	oldLines, newLines := logicalLines(oldText), logicalLines(newText)
	for len(oldLines) > 0 && len(newLines) > 0 && oldLines[0] == newLines[0] {
		oldLines, newLines = oldLines[1:], newLines[1:]
	}
	for len(oldLines) > 0 && len(newLines) > 0 && oldLines[len(oldLines)-1] == newLines[len(newLines)-1] {
		oldLines, newLines = oldLines[:len(oldLines)-1], newLines[:len(newLines)-1]
	}
	return int64(len(newLines)), int64(len(oldLines))
}

func ContentLineCount(content string) int64 { return int64(len(logicalLines(content))) }

func logicalLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ClassifyArtifact is intentionally conservative and deterministic. Generated,
// vendor and build output are excluded before any productivity projection.
func ClassifyArtifact(path string) (ArtifactKind, bool, string) {
	clean := filepath.ToSlash(strings.TrimSpace(path))
	lower := strings.ToLower(clean)
	for _, segment := range []string{"/vendor/", "/node_modules/", "/dist/", "/build/", "/.generated/", "/generated/"} {
		if strings.Contains("/"+lower, segment) {
			return ArtifactOther, true, "generated_or_vendor"
		}
	}
	base := strings.ToLower(filepath.Base(lower))
	ext := strings.ToLower(filepath.Ext(base))
	if strings.Contains(base, "_test.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return ArtifactTest, false, ""
	}
	switch ext {
	case ".md", ".mdx", ".rst", ".txt":
		return ArtifactDoc, false, ""
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".rs", ".py", ".java", ".kt", ".c", ".cc", ".cpp", ".h", ".sh", ".bash", ".zsh", ".css", ".scss", ".html", ".vue", ".svelte", ".sql", ".swift":
		return ArtifactCode, false, ""
	case ".json", ".yaml", ".yml", ".toml", ".ini":
		return ArtifactConfig, false, ""
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp":
		return ArtifactAsset, false, ""
	default:
		return ArtifactOther, false, ""
	}
}

type InteractionKind string

const (
	InteractionAck           InteractionKind = "ack"
	InteractionProgress      InteractionKind = "progress"
	InteractionNeedsYou      InteractionKind = "needs_you"
	InteractionPermission    InteractionKind = "permission"
	InteractionDecision      InteractionKind = "decision"
	InteractionClarification InteractionKind = "clarification"
	InteractionSteer         InteractionKind = "steer"
	InteractionCorrection    InteractionKind = "correction"
	InteractionRetry         InteractionKind = "retry"
	InteractionNotification  InteractionKind = "notification"
	InteractionResume        InteractionKind = "resume"
	InteractionRecovery      InteractionKind = "error_recovery"
)

type AttentionClass string

const (
	AttentionRequired  AttentionClass = "required_gate"
	AttentionNeutral   AttentionClass = "neutral_steer"
	AttentionAvoidable AttentionClass = "avoidable_correction"
)

type InteractionEvent struct {
	WorkItemID string          `json:"work_item_id"`
	Kind       InteractionKind `json:"kind"`
	Attention  AttentionClass  `json:"attention"`
	At         time.Time       `json:"at"`
	Ref        string          `json:"ref,omitempty"`
}

type ExperienceSummary struct {
	SubmitToStart        *time.Duration `json:"submit_to_start,omitempty"`
	FirstVisibleProgress *time.Duration `json:"first_visible_progress,omitempty"`
	GrossTTAV            *time.Duration `json:"gross_ttav,omitempty"`
	LongestSilent        *time.Duration `json:"longest_silent,omitempty"`
	RequiredAttention    int            `json:"required_attention"`
	NeutralAttention     int            `json:"neutral_attention"`
	AvoidableAttention   int            `json:"avoidable_attention"`
}

func SummarizeExperience(submitted time.Time, started, accepted *time.Time, events []InteractionEvent) ExperienceSummary {
	out := ExperienceSummary{}
	if started != nil && !submitted.IsZero() && !started.Before(submitted) {
		d := started.Sub(submitted)
		out.SubmitToStart = &d
	}
	if accepted != nil && !submitted.IsZero() && !accepted.Before(submitted) {
		d := accepted.Sub(submitted)
		out.GrossTTAV = &d
	}
	ordered := append([]InteractionEvent(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	var visible []time.Time
	for _, event := range ordered {
		switch event.Attention {
		case AttentionRequired:
			out.RequiredAttention++
		case AttentionNeutral:
			out.NeutralAttention++
		case AttentionAvoidable:
			out.AvoidableAttention++
		}
		if event.Kind == InteractionAck || event.Kind == InteractionProgress || event.Kind == InteractionNotification {
			visible = append(visible, event.At)
		}
	}
	if len(visible) > 0 && !submitted.IsZero() && !visible[0].Before(submitted) {
		d := visible[0].Sub(submitted)
		out.FirstVisibleProgress = &d
	}
	points := append([]time.Time(nil), visible...)
	if !submitted.IsZero() {
		points = append(points, submitted)
	}
	if accepted != nil {
		points = append(points, *accepted)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
	var longest time.Duration
	for i := 1; i < len(points); i++ {
		if gap := points[i].Sub(points[i-1]); gap > longest {
			longest = gap
		}
	}
	if len(points) >= 2 {
		out.LongestSilent = &longest
	}
	return out
}

func GenerationTokensPerSecond(outputTokens int64, firstTokenAt, endedAt *time.Time) *float64 {
	rate, _ := TokensPerSecond(outputTokens, firstTokenAt, endedAt)
	return rate
}

// TokensPerSecond returns both the rate and its additive denominator. Reports
// aggregate Σtokens/Σseconds; they must never average per-request rates.
func TokensPerSecond(outputTokens int64, startedAt, endedAt *time.Time) (rate, durationSeconds *float64) {
	if outputTokens < 0 || startedAt == nil || endedAt == nil || !endedAt.After(*startedAt) {
		return nil, nil
	}
	seconds := endedAt.Sub(*startedAt).Seconds()
	value := float64(outputTokens) / seconds
	return &value, &seconds
}

// ElapsedSeconds projects a latency interval without pretending it is token
// generation. It is shared by exact provider TTFT and transcript-observed first
// response, whose evidence levels remain distinct in the report contract.
func ElapsedSeconds(startedAt, endedAt *time.Time) *float64 {
	if startedAt == nil || endedAt == nil || endedAt.Before(*startedAt) {
		return nil
	}
	seconds := endedAt.Sub(*startedAt).Seconds()
	return &seconds
}
