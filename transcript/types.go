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
}

// SessionRef addresses one session inside a source for transcript loading.
type SessionRef struct {
	ProjectDir string // the project root_dir the session belongs to
	ID         string // source-scoped id (claude jsonl basename / deepwork session id)
}

// Transcript is the parsed body of one session: an ordered list of turns,
// each carrying typed blocks aligned to the @ce frontend block contract.
type Transcript struct {
	Source string                 `json:"source"`
	Ref    string                 `json:"ref"`
	Title  string                 `json:"title"`
	Turns  []Turn                 `json:"turns"`
	Meta   map[string]interface{} `json:"meta,omitempty"` // usage totals etc.
}

// Turn is one exchange. Role is "user" or "assistant". Blocks are the typed
// content units rendered by the @ce stream.
type Turn struct {
	Index  int     `json:"index"`
	Role   string  `json:"role"` // user | assistant
	Blocks []Block `json:"blocks"`
	// Timestamp of the underlying jsonl line, when available.
	At *time.Time `json:"at,omitempty"`
}

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
)

// Block is one typed content unit inside a turn. Fields are populated per Type;
// JSON omitempty keeps the wire payload aligned to what the @ce block expects.
type Block struct {
	Type string `json:"type"`

	// text / thinking
	Text string `json:"text,omitempty"`

	// tool / agent (a tool_use)
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolUseID  string                 `json:"tool_use_id,omitempty"`
	ToolInput  map[string]interface{} `json:"tool_input,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	IsError    bool                   `json:"is_error,omitempty"`

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
