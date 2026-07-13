// Package sessionsource is the CHG-014 Runtime-SSOT session aggregation layer.
//
// Core principle (SSOT-SESSION-LOADER.md §0): the agent runtime owns the
// session transcript; deepwork is a viewer/orchestrator. A SessionSource reads
// a runtime's own storage (claude jsonl, codex history, deepwork DB) and never
// writes or deletes it. Soft-delete (hidden) lives only in deepwork's own table
// and merely filters the aggregated list — the runtime transcript is untouched.
//
// Terminal abstraction (§6): one interface + many implementations + one
// aggregator. Adding a new runtime = adding a Source; the aggregator is unchanged.
package transcript

import (
	"context"
	"time"
)

// Source kind constants — the SSOT origin of a session.
const (
	KindClaude   = "claude"
	KindCodex    = "codex"
	KindDeepwork = "deepwork"
)

// SessionMeta is one row in the aggregated session list (list view, no body).
type SessionMeta struct {
	// ID is a stable, source-scoped identifier used to round-trip back to the
	// SSOT for a transcript load. For claude it is the jsonl basename (uuid);
	// for deepwork it is the numeric session id as a string.
	ID     string `json:"id"`
	Source string `json:"source"` // claude | codex | deepwork
	// SsotPath is the on-disk transcript path (claude jsonl / codex jsonl) or
	// the deepwork session id. Surfaced to the UI for the "源自 …" tooltip.
	SsotPath  string    `json:"ssot_path"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	TurnCount int       `json:"turn_count"`
	// Hidden is the deepwork soft-delete flag (virtual delete). The SSOT is
	// never removed; hidden rows are filtered out of the default list.
	Hidden bool `json:"hidden"`
	// FriendlyName is the deepwork-owned soft-rename. When non-empty it has
	// already been applied onto Title by the aggregator; it is surfaced here so
	// the UI can distinguish a renamed session from its SSOT title. The runtime
	// transcript title is never modified (SSOT protection).
	FriendlyName string `json:"friendly_name,omitempty"`
	// Activity / Attention / Owned are the WLS-8b live activity state, derived at
	// list time (NOT stored): Activity = LIVE|OBSERVED|QUIET, Attention =
	// working|waiting|error|idle, Owned = this MuxHost holds a live REPL for it.
	// Empty Activity means the list route did not derive it (no deriver wired) —
	// the frontend treats absent state as QUIET/idle. These drive the two-axis tab
	// + tree badge and the OBSERVED「在另一会话中活跃·观测中」banner/composer-lock.
	Activity  string `json:"activity,omitempty"`
	Attention string `json:"attention,omitempty"`
	Owned     bool   `json:"owned,omitempty"`
	// SessionExecution is orthogonal to the root liveness/write-authority axes above.
	// A quiet root may still have running child AgentExecutions.
	ExecutionActivity   string `json:"execution_activity,omitempty"` // running | idle
	RootAgentState      string `json:"root_agent_state,omitempty"`   // running | waiting | idle | error
	InteractionMode     string `json:"interaction_mode,omitempty"`   // owned | observed | history
	RunningAgents       int    `json:"running_agents,omitempty"`
	ExecutionObservedAt int64  `json:"execution_observed_at,omitempty"`
	AgentSourceStatus   string `json:"agent_source_status,omitempty"` // complete | partial | unknown
}

// SessionRef addresses one session inside a source for transcript loading.
type SessionRef struct {
	ProjectDir string // the project root_dir the session belongs to
	ID         string // source-scoped id (claude jsonl basename / deepwork session id)
}

// WindowRequest addresses a bounded suffix/range of an append-only transcript.
// Before is an opaque source cursor (currently a byte boundary), not a run index.
type WindowRequest struct {
	Before     *int64
	Limit      int
	Generation string
}

// WindowResult carries a projected AgentRun window. Generation remains stable across
// appends and changes on replacement; Reset tells clients to discard an invalid cursor.
type WindowResult struct {
	Transcript  *Transcript
	Before      int64
	HasMore     bool
	Version     string
	Generation  string
	Reset       bool
	BytesParsed int64
}

// WindowSource is the performance boundary for large append-only transcripts.
// Implementations find and parse only the requested tail/range.
type WindowSource interface {
	LoadTranscriptWindow(ctx context.Context, ref SessionRef, req WindowRequest) (*WindowResult, error)
}

// Transcript is the parsed body of one session.
//
// Turns is the ADAPTER FACT: the raw, ordered parse of the runtime's own jsonl
// (one Turn per runtime message/event group). It is deliberately NOT on the wire
// — a runtime event is not a UI conversation turn (a single human intent routinely
// spans dozens of model/tool iterations). The UI consumes Runs, the AgentRun
// projection (agentrun.go), which is a pure function of Turns.
//
// Server-side consumers of the raw parse (redaction, touched-file extraction,
// usage metrics) keep reading Turns; they run BEFORE ProjectAgentRuns.
type Transcript struct {
	Source string                 `json:"source"`
	Ref    string                 `json:"ref"`
	Title  string                 `json:"title"`
	Turns  []Turn                 `json:"-"`              // adapter fact, server-side only
	Runs   []AgentRun             `json:"runs"`           // ← the wire contract (ProjectAgentRuns)
	Meta   map[string]interface{} `json:"meta,omitempty"` // usage totals etc.
}

// Turn is one runtime message/event group as the adapter parsed it — NOT a UI
// conversation turn. Role is "user" or "assistant". Blocks are the typed content
// units rendered by the @ce stream.
type Turn struct {
	Index  int     `json:"index"`
	Role   string  `json:"role"` // user | assistant
	Blocks []Block `json:"blocks"`
	// Timestamp of the underlying jsonl line, when available.
	At *time.Time `json:"at,omitempty"`
	// Terminal is the ADAPTER-supplied, runtime-agnostic "this assistant turn
	// yielded control back to the human" fact. Empty = the runtime is still working
	// (a tool loop continues).
	//
	// Derivation per runtime (verified against real transcripts):
	//   claude   message.stop_reason: end_turn → TerminalEndTurn; tool_use → "" ;
	//            a `[Request interrupted by user]` line → TerminalAborted.
	//   codex    event_msg: task_complete → TerminalEndTurn; turn_aborted → TerminalAborted.
	//   deepwork native: every assistant entry closes its exchange → TerminalEndTurn.
	Terminal string `json:"terminal,omitempty"`

	// InputKind classifies a USER turn. The projector must never GUESS whether a human
	// line opens a new round or steers the running one ("is a run currently open?" is
	// not a decidable contract — a runtime that dies mid-loop would silently swallow the
	// next independent request into the previous round). So each adapter states it, from
	// the runtime's own facts:
	//
	//   codex   `event_msg/task_started` opens a task → the message that drives it is an
	//           intent; a user_message arriving inside an already-started task is a steer.
	//           (Real rollout: task_started × 10, user_message × 16 → 10 intents, 6 steers.)
	//   claude  a user line while the model is still in a tool loop (no end_turn yet) is
	//           queued — the runtime records it as `queue-operation/enqueue` (72 in the
	//           real transcript). Yielded → the next human line is a new intent.
	//   system  runtime notifications (task-notification, interrupt markers) — never a round.
	InputKind string `json:"input_kind,omitempty"`
}

// Turn.Terminal values — how a run ended (runtime-agnostic).
const (
	TerminalEndTurn = "end_turn" // yielded to human → run completed
	TerminalAborted = "aborted"  // interrupted (user ESC / turn_aborted)
	TerminalError   = "error"    // runtime error ended the run
)

// Turn.InputKind values — what a human line MEANS (adapter-decided, never guessed).
const (
	InputIntent    = "intent"    // an independent human request → opens a run (a round)
	InputAmendment = "amendment" // a steer INTO the running run → never a new round
	InputSystem    = "system"    // runtime notification → never a round, never a bubble
)

// Block type constants — aligned with the @ce stream block registry
// (thinking / text / tool-group / agent subflow / usage / error / user-bubble).
const (
	BlockText     = "text"
	BlockThinking = "thinking"
	BlockTool     = "tool"  // a tool_use (+ its tool_result, attached)
	BlockAgent    = "agent" // an `Agent` tool_use → subagent subflow (nested)
	BlockUser     = "user_bubble"
	BlockUsage    = "usage"
	BlockError    = "error"
	// BlockTaskNotification is a runtime system event emitted when a background
	// task / dispatched agent finishes (claude jsonl user line carrying a
	// `<task-notification>` payload — verified schema, claude.go). It is a
	// first-class block kind (extension point for future system-event blocks)
	// rendered as a compact one-line card, NOT a full-width text bubble.
	BlockTaskNotification = "task-notification"
	// BlockCompaction is the runtime's automatic context compaction (codex
	// `event_msg/context_compacted` — verified: 5 occurrences in a real 16 MB
	// rollout). It is part of the run's causal story ("上下文已自动压缩") and must
	// survive an expanded ProcessTrace; it is NOT a conversation turn.
	BlockCompaction = "compaction"
)

// Block is one typed content unit inside a turn. Fields are populated per Type;
// JSON omitempty keeps the wire payload aligned to what the @ce block expects.
type Block struct {
	Type string `json:"type"`

	// EventID is a STABLE identity for this content unit, derived from the source event
	// (claude message.id + ordinal / codex call_id / line ordinal) — never a render-time
	// counter. The UI keys expansion state and list diffing on it, so a reload or a
	// re-fetch must produce the same ids or the user's open/closed choices re-bind to the
	// wrong segments.
	EventID string `json:"event_id,omitempty"`

	// text / thinking
	Text string `json:"text,omitempty"`

	// Final marks the text that belongs to the run's TERMINAL (yielding) iteration — the
	// answer, as opposed to mid-run narration. The projector lifts these out of the
	// process trace into AgentRun.FinalAnswer, which is why it can never be swallowed by
	// a collapse. It is a DOMAIN fact (claude stop_reason=end_turn / codex the message
	// that precedes task_complete), not a position: "the last text block" would let an
	// aborted run's narration impersonate an answer, and would push a real answer back
	// into the trace whenever a notification happened to follow it.
	Final bool `json:"-"`

	// tool / agent (a tool_use)
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolUseID  string                 `json:"tool_use_id,omitempty"`
	ToolInput  map[string]interface{} `json:"tool_input,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	IsError    bool                   `json:"is_error,omitempty"`
	// ResultSeen: a tool_result actually arrived for this call. false = the tool never
	// completed (the run was interrupted / the session died mid-call). The UI must render
	// it as stopped, NOT as done — stamping "done" on every replayed tool (the old
	// behavior) fabricates success for work that never finished.
	ResultSeen bool `json:"result_seen,omitempty"`
	// Orphan: a tool_result with no matching call in this transcript. Kept (never dropped)
	// + counted in RunDiagnostic, so a lossy source degrades visibly.
	Orphan bool `json:"orphan,omitempty"`

	// agent (Agent tool_use → subagent). SubagentType is the dispatched agent
	// kind; Description is the human-readable task; the inner subflow blocks are
	// in Children (P1: empty — sidechain transcript stitching deferred to P2).
	SubagentType string  `json:"subagent_type,omitempty"`
	Description  string  `json:"description,omitempty"`
	Children     []Block `json:"children,omitempty"`

	// agent usage/timing (CHG-014 P3b — Gap-4). DurationMs is the wall-clock
	// delta between the Agent tool_use line and its tool_result line — the only
	// honest end-to-end subagent timing available from the parent jsonl (claude
	// does not inline the subagent's own usage; sidechain transcripts are absent
	// in practice). InTokens/OutTokens stay 0 (→ omitted → frontend "—") unless a
	// future source supplies the subagent's own usage. Honest-degradation rule:
	// never fabricate token counts (SPEC-UX-ROUND3 §5).
	DurationMs int `json:"duration_ms,omitempty"`
	InTokens   int `json:"in_tokens,omitempty"`
	OutTokens  int `json:"out_tokens,omitempty"`

	// task-notification (CHG-014 P3b — Gap-4). NotifyStatus = completed|failed|
	// killed; Text carries the human-readable <summary>; TaskID is the runtime
	// task id (debug/round-trip). Parsed from a claude `<task-notification>` user
	// line — see claude.go.
	NotifyStatus string `json:"notify_status,omitempty"`
	TaskID       string `json:"task_id,omitempty"`

	// usage
	Usage map[string]interface{} `json:"usage,omitempty"`
}

// SessionSource is the terminal abstraction: one runtime's session storage,
// read-only. Implementations: ClaudeSource, CodexSource, DeepworkSource.
type SessionSource interface {
	// Kind returns the SSOT origin tag (claude | codex | deepwork).
	Kind() string
	// ListSessions enumerates the sessions this runtime stored for projectDir.
	ListSessions(ctx context.Context, projectDir string) ([]SessionMeta, error)
	// LoadTranscript parses one session's body from the SSOT into turns/blocks.
	LoadTranscript(ctx context.Context, ref SessionRef) (*Transcript, error)
}

// SessionOverlay is the deepwork-owned overlay state for one runtime session:
// soft-delete (Hidden) and soft-rename (FriendlyName). Both are independent and
// neither touches the runtime transcript (SSOT protection, §0).
type SessionOverlay struct {
	Hidden       bool
	FriendlyName string
}

// HiddenStore is the deepwork-owned soft-delete / soft-rename layer. It only
// marks/filters/renames in deepwork's own table; it MUST NOT touch any runtime
// transcript (SSOT protection, §0).
type HiddenStore interface {
	// IsHidden reports whether (source, ssotKey) is soft-deleted.
	IsHidden(ctx context.Context, source, ssotKey string) (bool, error)
	// HiddenSet returns the set of hidden ssot keys for a source (batch filter).
	HiddenSet(ctx context.Context, source string) (map[string]bool, error)
	// Overlays returns the full overlay state (hidden + friendly_name) per ssot
	// key for a source, in one batch read (used by the aggregator).
	Overlays(ctx context.Context, source string) (map[string]SessionOverlay, error)
	// SetHidden marks/unmarks a session as hidden (idempotent). Preserves any
	// existing friendly_name.
	SetHidden(ctx context.Context, source, ssotKey string, hidden bool) error
	// SetFriendlyName sets/clears the soft-rename (idempotent). Preserves the
	// existing hidden flag. Empty name clears the rename.
	SetFriendlyName(ctx context.Context, source, ssotKey, name string) error
}
