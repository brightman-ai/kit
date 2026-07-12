package transcript

import "encoding/json"

// native_schema.go is the SINGLE SSOT for the Deepwork Native Transcript JSONL
// wire schema (deepwork.native_transcript.v1 / v1.1). Before this file the
// shape was declared TWICE — once as an unexported READ view here
// (deepwork_raw.go's former dwLine/dwMessage/dwContentBlock/dwUsage/dwMetrics)
// and once as the WRITE model in a host's worktranscript package
// (Entry/Message/ContentBlock/TurnUsage/TurnMetrics) — with no compiler check
// that the two stayed byte-compatible. Both sides now import these exported
// Native* types instead.
//
// Workstream is deliberately json.RawMessage (opaque): this package must not
// depend on deepwork's workstream event types (kit stays dependency-light and
// import-cycle free). worktranscript marshals its StreamPayload into this
// field on write; the reader in this package never decodes `progress` lines
// into blocks, so it never needs to look inside.
//
// json tags below are byte-identical to the historical write model
// (pkg/worktranscript.Entry et al.) — do not change them without a wire
// migration; anything that previously round-tripped through the two
// hand-synced schemas must keep marshaling/unmarshaling identically.
type NativeEntry struct {
	Format            string          `json:"format,omitempty"`
	Runtime           string          `json:"runtime,omitempty"`
	Type              string          `json:"type"` // user | assistant | result | progress
	UUID              string          `json:"uuid,omitempty"`
	ParentUUID        *string         `json:"parentUuid"`
	SessionID         string          `json:"sessionId"`
	Timestamp         string          `json:"timestamp"`
	Message           *NativeMessage  `json:"message,omitempty"`
	Subtype           string          `json:"subtype,omitempty"`
	DurationMs        int             `json:"duration_ms,omitempty"`
	NumTurns          int             `json:"num_turns,omitempty"`
	DeepworkSessionID int64           `json:"deepworkSessionId,omitempty"`
	DeepworkTurnID    int64           `json:"deepworkTurnId,omitempty"`
	Workstream        json.RawMessage `json:"workstream,omitempty"`
	// Metrics (v1.1) inlines per-turn ttft/usage on the turn-closing `result`
	// entry so the transcript carries the round's metrics without the DB. nil on
	// non-result entries and on legacy (v1) files.
	Metrics *NativeMetrics `json:"metrics,omitempty"`

	// ── v1.2: claude-parity context (additive; json tags match claude's own JSONL,
	// so a reader written for one shape reads the other). Every field is omitempty:
	// a v1/v1.1 file simply carries none of them and decodes unchanged.

	// CWD / GitBranch / Version make a line SELF-DESCRIBING — the same three claude
	// stamps on every entry. Without them a transcript cannot say which checkout it
	// describes, and replay/attribution has to reach outside the file for context.
	CWD       string `json:"cwd,omitempty"`
	GitBranch string `json:"gitBranch,omitempty"`
	Version   string `json:"version,omitempty"`

	// RequestID pairs with NativeMessage.ID as the DEDUP KEY for token accounting —
	// the pair kit/usage keys claude usage on. Deepwork turns carried neither, which
	// is precisely why deepwork-native sessions contribute nothing to the cost report
	// today: there is no stable identity to dedup a re-read line by.
	RequestID string `json:"requestId,omitempty"`

	// IsSidechain / AgentID model a SUBAGENT branch, exactly as claude does: a
	// sidechain entry belongs to a spawned agent's own conversation rather than the
	// main thread, and AgentID names which one. The agent-tree projection is built
	// from these two fields.
	//
	// RESERVED — the deepwork agent runtime does not spawn subagents yet, so nothing
	// sets these. They exist now so the wire format does not need a breaking bump the
	// day it does, and so the reader/tree code can be written against a stable shape.
	IsSidechain bool   `json:"isSidechain,omitempty"`
	AgentID     string `json:"agentId,omitempty"`

	// UserType mirrors claude ("external" for a human turn, "internal" for one the
	// runtime synthesised, e.g. a tool_result fed back in). Empty = unspecified.
	UserType string `json:"userType,omitempty"`
}

// NativeMessage is the inner message object for user/assistant lines.
type NativeMessage struct {
	// ID (v1.2) is the provider's message id (claude: "msg_…"). With NativeEntry.RequestID
	// it forms the dedup key token accounting uses — see RequestID's note.
	ID   string `json:"id,omitempty"`
	Role string `json:"role"`
	// Model (v1.1) is the real model id that produced this assistant turn,
	// mirroring claude's `message.model`. Empty = path did not report.
	Model   string               `json:"model,omitempty"`
	Content []NativeContentBlock `json:"content"`
	// StopReason (v1.2) is why the assistant stopped: "end_turn" | "tool_use" |
	// "max_tokens" | "stop_sequence". This is the signal agent-status detection reads
	// to tell "the agent finished its turn and is waiting for you" apart from "it
	// paused mid-turn to run a tool" — without it, a deepwork session cannot express
	// the running→waiting transition that drives the turn-end notification.
	// Empty = the path did not report one (honest unknown, never guessed).
	StopReason string `json:"stop_reason,omitempty"`
	// Usage (v1.1) inlines the round's token accounting on the assistant message,
	// mirroring claude's `message.usage`. nil = path reported no usage (honest
	// unknown, never fabricated). Self-sufficient: deleting the DB does not lose it.
	Usage *NativeUsage `json:"usage,omitempty"`
}

// NativeContentBlock is one typed content unit. text/thinking carry their
// string; tool_use carries name/id/input; tool_result carries tool_use_id +
// content (a JSON array of {type:text,text} blocks, or a bare string).
type NativeContentBlock struct {
	Type      string          `json:"type"` // text | thinking | tool_use | tool_result
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// NativeUsage is the inlined per-turn token accounting (v1.1). Pointer fields
// keep the "nil = unknown vs 0 = observed" distinction the writer honors.
type NativeUsage struct {
	InputTokens       *int `json:"input_tokens,omitempty"`
	OutputTokens      *int `json:"output_tokens,omitempty"`
	// ThinkingTokens — reasoning token 单列 (CHG-016). Inlined on the assistant
	// line so the deepwork replay footer shows the SAME thinking token the live
	// stream did (nil = unknown → 「—」, distinct from the thinking duration).
	ThinkingTokens    *int `json:"thinking_tokens,omitempty"`
	CacheReadTokens   *int `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens *int `json:"cache_creation_tokens,omitempty"`
	// TTFTMs — first-content latency, inlined on the assistant line's usage so the
	// per-turn replay footer renders TTFT without chasing the separate `result`
	// line (which the unified reader skips). nil = unknown → 「—」.
	TTFTMs *int `json:"ttft_ms,omitempty"`
}

// NativeMetrics is the inlined per-turn timing carried on the `result` entry (v1.1).
type NativeMetrics struct {
	TTFTMs     *int `json:"ttft_ms,omitempty"`
	DurationMs int  `json:"duration_ms,omitempty"`
}
