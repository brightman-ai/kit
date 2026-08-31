// Package workstream defines the user-visible event stream for a Deepwork work
// turn. It is intentionally above provider wire events and below portal domain
// state: LLM chunks, tool calls, artifact generation, context capture and
// projection progress can all become workstream events when they are useful in
// a timeline UI.
package workstream

import (
	"encoding/json"
	"time"
)

// Kind classifies the semantic content of a workstream event.
type Kind string

const (
	Status Kind = "status"
	Text   Kind = "text"

	Thinking Kind = "thinking"

	SkillStart  Kind = "skill_start"
	SkillResult Kind = "skill_result"

	ToolStart  Kind = "tool_start"
	ToolResult Kind = "tool_result"

	TaskUpdate Kind = "task_update"

	ArtifactStart Kind = "artifact_start"
	ArtifactDelta Kind = "artifact_delta"
	ArtifactDone  Kind = "artifact_done"

	ContextStart Kind = "context_start"
	ContextDone  Kind = "context_done"

	ProjectionStart Kind = "projection_start"
	ProjectionDone  Kind = "projection_done"

	PermissionRequest  Kind = "permission_request"
	PermissionResolved Kind = "permission_resolved"

	Usage Kind = "usage"
	Done  Kind = "done"
	Error Kind = "error"
	Raw   Kind = "raw"

	// Meta carries out-of-band session metadata discovered MID-turn that is not part
	// of the answer content — e.g. a session title captured from the agent CLI's
	// title-generation call. It is intentionally distinct from Status (which drives
	// the waiting timeline / progress) and Text (the answer): a Meta event must never
	// advance progress or render as a chunk. The payload travels on Meta[…] (e.g.
	// Meta["title"]). 用于 L8 title fast-path: 上层即时拿到标题，不再事后从 transcript 恢复。
	Meta Kind = "meta"
)

// Event is the stable timeline event consumed by Portal session panes.
type Event struct {
	Kind       Kind            `json:"kind"`
	Status     string          `json:"status,omitempty"`
	Content    string          `json:"content,omitempty"`
	Tool       *ToolData       `json:"tool,omitempty"`
	Skill      *SkillData      `json:"skill,omitempty"`
	Task       *TaskData       `json:"task,omitempty"`
	Artifact   *ArtifactData   `json:"artifact,omitempty"`
	Context    *ContextData    `json:"context,omitempty"`
	Projection *ProjectionData `json:"projection,omitempty"`
	Permission *PermissionData `json:"permission,omitempty"`
	Usage      *UsageData      `json:"usage,omitempty"`
	DoneInfo   *DoneData       `json:"done,omitempty"`
	Source     string          `json:"source,omitempty"`
	RawData    json.RawMessage `json:"raw,omitempty"`
	Meta       map[string]any  `json:"meta,omitempty"`
	At         string          `json:"at,omitempty"`
}

type ToolData struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	DurationMs int             `json:"duration_ms,omitempty"`
}

type SkillData struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	DurationMs int             `json:"duration_ms,omitempty"`
}

type TaskData struct {
	ID     string     `json:"id,omitempty"`
	Title  string     `json:"title,omitempty"`
	Items  []TaskItem `json:"items,omitempty"`
	Status string     `json:"status,omitempty"`
}

// TaskItem is one step of a plan the user watches progress through.
//
// Status 取值见下方 TaskStatus* 常量（登记表 AllTaskStatuses()）。
//
// 字段类型保持 string 而非具名类型。**不是**因为字面量赋不进具名类型 —— 无类型字符串
// 字面量赋给具名 string 类型完全合法（`Item{Status: "pending"}` 编译得过）。真正会断的是
// 生产方那些把 `status string` **变量**在函数间传递的地方：变量赋给具名类型需要显式转换，
// 一改就编译不过。为一个枚举去打断下游的构建，不划算。
//
// 常量是**无类型**字符串常量，所以既能赋给本字段，也能逐步替换掉散落各处的字面量。
type TaskItem struct {
	ID      string `json:"id,omitempty"`
	Content string `json:"content"`
	Status  string `json:"status"` // 见 TaskStatus* / AllTaskStatuses()
}

// 计划步骤的状态取值。此前这个枚举只活在 TaskItem.Status 后面的一行注释里 —— 也就是说
// 它**不存在于任何机器可读的地方**：这一侧是裸 string，而消费端（如 TypeScript 前端）
// 往往把它写成一个真联合类型，两边没有任何东西把它们绑住。加一档而消费端没跟上，前端的
// 兜底分支会把它画成"还没开始"，不报错、不留痕、屏幕上说假话 —— 与 Kind 当初要解决的
// 漂移是同一种病。
//
// 生命周期顺序：Pending → InProgress → 三种终态之一。
const (
	TaskStatusPending    = "pending"
	TaskStatusInProgress = "in_progress"

	// TaskStatusCompleted 这一步做成了。
	TaskStatusCompleted = "completed"
	// TaskStatusFailed 这一步跑了但没成。补这一档之前，生产方遇到步骤失败只有两条路：
	// 让它停在 in_progress（前端那一行永远转圈 = 撒谎说还在跑），或者标成 completed
	// （撒谎说成了）。已有消费者正因此受阻。
	TaskStatusFailed = "failed"
	// TaskStatusCancelled 这一步没跑（整轮被中断/上游取消）。与 Failed 分开：没跑不等于
	// 跑砸了，把两者塞进同一个值，用户就无从判断该重试还是该改方案。
	TaskStatusCancelled = "cancelled"
)

type ArtifactData struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Delta       string `json:"delta,omitempty"`
	Complete    bool   `json:"complete,omitempty"`
}

type ContextData struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Title   string `json:"title,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ProjectionData struct {
	ID     string `json:"id,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Target string `json:"target,omitempty"`
	Status string `json:"status,omitempty"`
}

type PermissionData struct {
	ID           string   `json:"id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	Status       string   `json:"status,omitempty"`
}

type UsageData struct {
	InputTokens              int  `json:"input_tokens,omitempty"`
	ThinkingTokens           int  `json:"thinking_tokens,omitempty"`
	OutputTokens             int  `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens,omitempty"`
	TotalTokens              int  `json:"total_tokens,omitempty"`
	Estimated                bool `json:"estimated,omitempty"`
}

type DoneData struct {
	Reason       string `json:"reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	// TTFTMs is the turn's time-to-first-content (the user-visible "first response"
	// latency: first Text/Thinking/ToolStart). Carried on the terminal Done event so
	// the GUI footer shows the REAL value instead of「—」(it was computed for the DB
	// but never reached the front end on CLI turns). nil = not observed → footer「—」.
	TTFTMs *int `json:"ttft_ms,omitempty"`
}

type Emitter func(Event) bool

func StatusEvent(status string) Event {
	return stamp(Event{Kind: Status, Status: status})
}

func TextEvent(content string) Event {
	return stamp(Event{Kind: Text, Content: content})
}

func ThinkingEvent(content string) Event {
	return stamp(Event{Kind: Thinking, Content: content})
}

func SkillStartEvent(id, name string, input json.RawMessage) Event {
	return stamp(Event{Kind: SkillStart, Skill: &SkillData{ID: id, Name: name, Input: input}})
}

func SkillResultEvent(id, name, output string, isErr bool, durationMs int) Event {
	return stamp(Event{Kind: SkillResult, Skill: &SkillData{ID: id, Name: name, Output: output, IsError: isErr, DurationMs: durationMs}})
}

func ToolStartEvent(id, name string, input json.RawMessage) Event {
	return stamp(Event{Kind: ToolStart, Tool: &ToolData{ID: id, Name: name, Input: input}})
}

func ToolResultEvent(id, name, output string, isErr bool, durationMs int) Event {
	return stamp(Event{Kind: ToolResult, Tool: &ToolData{ID: id, Name: name, Output: output, IsError: isErr, DurationMs: durationMs}})
}

func TaskUpdateEvent(task TaskData) Event {
	return stamp(Event{Kind: TaskUpdate, Task: &task})
}

func ArtifactStartEvent(artifact ArtifactData) Event {
	return stamp(Event{Kind: ArtifactStart, Artifact: &artifact})
}

func ArtifactDeltaEvent(artifact ArtifactData) Event {
	return stamp(Event{Kind: ArtifactDelta, Artifact: &artifact})
}

func ArtifactDoneEvent(artifact ArtifactData) Event {
	artifact.Complete = true
	return stamp(Event{Kind: ArtifactDone, Artifact: &artifact})
}

func ContextStartEvent(data ContextData) Event {
	return stamp(Event{Kind: ContextStart, Context: &data})
}

func ContextDoneEvent(data ContextData) Event {
	return stamp(Event{Kind: ContextDone, Context: &data})
}

func ProjectionStartEvent(data ProjectionData) Event {
	return stamp(Event{Kind: ProjectionStart, Projection: &data})
}

func ProjectionDoneEvent(data ProjectionData) Event {
	return stamp(Event{Kind: ProjectionDone, Projection: &data})
}

func PermissionRequestEvent(data PermissionData) Event {
	return stamp(Event{Kind: PermissionRequest, Permission: &data})
}

func PermissionResolvedEvent(data PermissionData) Event {
	return stamp(Event{Kind: PermissionResolved, Permission: &data})
}

func UsageEvent(data UsageData) Event {
	return stamp(Event{Kind: Usage, Usage: &data})
}

func DoneEvent(reason string, inputTokens, outputTokens int) Event {
	return stamp(Event{Kind: Done, DoneInfo: &DoneData{Reason: reason, InputTokens: inputTokens, OutputTokens: outputTokens}})
}

func ErrorEvent(message string) Event {
	return stamp(Event{Kind: Error, Content: message})
}

func RawEvent(data json.RawMessage) Event {
	return stamp(Event{Kind: Raw, RawData: data})
}

// TitleEvent creates a dedicated Meta event carrying a captured session title on
// Meta["title"]. It is OFF the Status/Text answer paths so it cannot pollute the
// waiting timeline or render as answer content. (L8 title fast-path.)
func TitleEvent(title string) Event {
	return stamp(Event{Kind: Meta, Meta: map[string]any{"title": title}})
}

func Burst(content string) []Event {
	events := make([]Event, 0, 2)
	if content != "" {
		events = append(events, TextEvent(content))
	}
	events = append(events, DoneEvent("stop", 0, 0))
	return events
}

func Collect(events *[]Event) Emitter {
	return func(ev Event) bool {
		*events = append(*events, ev)
		return true
	}
}

func Discard() Emitter {
	return func(Event) bool { return true }
}

func (e Event) WithSource(source string) Event {
	e.Source = source
	return e
}

func (e Event) WithMeta(key string, value any) Event {
	if e.Meta == nil {
		e.Meta = map[string]any{}
	}
	e.Meta[key] = value
	return e
}

// WithTTFT stamps the turn's time-to-first-content onto a Done event so the GUI
// footer can render the real TTFT. No-op for non-Done events or a nil measurement.
func (e Event) WithTTFT(ttftMs *int) Event {
	if e.DoneInfo != nil && ttftMs != nil {
		e.DoneInfo.TTFTMs = ttftMs
	}
	return e
}

func stamp(ev Event) Event {
	if ev.At == "" {
		ev.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return ev
}
