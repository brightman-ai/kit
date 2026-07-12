package transcript

import (
	"encoding/json"
	"strings"
	"time"
)

// codexLine is the union shape of one line in a codex rollout-*.jsonl. Every
// line has a top-level type (session_meta | event_msg | response_item |
// turn_context) and a polymorphic payload decoded lazily per type.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexPayloadType peeks the payload's inner "type" without a full decode
// (payloads are heterogeneous; we branch on this before unmarshalling).
type codexPayloadType struct {
	Type string `json:"type"`
}

// codexSessionMeta is the first line's payload (type=session_meta). It carries
// the session id (uuid), the originating cwd (project dir), and the wall clock.
type codexSessionMeta struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// codexMessage is a response_item payload of type=message. role is
// user/assistant/developer; content is an array of typed text parts whose text
// lives in either "text" (input_text/output_text) field.
type codexMessage struct {
	Role    string             `json:"role"`
	Content []codexMessagePart `json:"content"`
}

type codexMessagePart struct {
	Type string `json:"type"` // input_text | output_text
	Text string `json:"text"`
}

func (m *codexMessage) text() string {
	var b strings.Builder
	for _, p := range m.Content {
		if t := strings.TrimSpace(p.Text); t != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// codexFunctionCall is a response_item payload of type=function_call (or
// custom_tool_call). call_id pairs it with its later *_output line. arguments
// is a JSON string for function_call; input is a raw string for custom tools.
type codexFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // function_call: JSON-encoded args object
	Input     string `json:"input"`     // custom_tool_call: raw tool input (e.g. patch)
	CallID    string `json:"call_id"`
}

// codexFunctionOutput is a response_item payload of type=function_call_output /
// custom_tool_call_output. output is the tool's textual result.
type codexFunctionOutput struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// codexReasoning is a response_item payload of type=reasoning. summary holds
// human-readable reasoning bullets; content/encrypted_content are model-internal
// (usually encrypted) and not surfaced.
type codexReasoning struct {
	Summary []codexReasoningPart `json:"summary"`
}

type codexReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r *codexReasoning) text() string {
	var b strings.Builder
	for _, p := range r.Summary {
		if t := strings.TrimSpace(p.Text); t != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// codexTokenCount is an event_msg payload of type=token_count. codex emits one
// per turn (and on rate-limit refresh); info is null on the rate-limit-only
// lines and populated on real turn-completion lines. last_token_usage is the
// per-turn delta; total_token_usage is the cumulative running total. The
// rate_limits block is intentionally ignored (deepwork has no rate-limit UI).
type codexTokenCount struct {
	Info *codexTokenInfo `json:"info"`
}

type codexTokenInfo struct {
	TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
	LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
}

// codexTokenUsage is codex's token accounting. Field names differ from claude's
// usage block, so usageMap() remaps onto the SSOT keys the @ce UsageFooter eats
// (input_tokens / output_tokens / cache_read_input_tokens). cached_input_tokens
// is codex's name for cache-read; reasoning_output_tokens is folded into the
// surfaced output (claude likewise counts reasoning toward output_tokens).
type codexTokenUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}

func (l *codexLine) asTokenCount() *codexTokenCount {
	var t codexTokenCount
	if json.Unmarshal(l.Payload, &t) != nil {
		return nil
	}
	return &t
}

// codexTurnContext is a top-level type=turn_context line's payload. codex emits
// one before the turns it governs; "model" is the only field the usage/cost
// path needs (a session can switch models mid-conversation, e.g. user runs
// /model). Other turn_context fields (sandbox_policy, approval_policy, ...)
// are intentionally not modeled here — add them if a future consumer needs them.
type codexTurnContext struct {
	Model string `json:"model"`
}

func (l *codexLine) asTurnContext() *codexTurnContext {
	var t codexTurnContext
	if json.Unmarshal(l.Payload, &t) != nil {
		return nil
	}
	return &t
}

// usageMap projects codex's per-turn token usage onto the generic map the @ce
// usage block consumes — using the SAME keys claude inlines (SSOT), so the
// frontend stays runtime-agnostic. nil → nil so the caller emits no usage block
// (honest unknown). The all-zero turn (codex emits a token_count even for a
// no-op turn) is also nil-suppressed.
func (u *codexTokenUsage) usageMap() map[string]interface{} {
	if u == nil {
		return nil
	}
	m := map[string]interface{}{}
	if u.InputTokens != 0 {
		m["input_tokens"] = u.InputTokens
	}
	// codex counts reasoning tokens separately; fold into output to match how
	// claude's output_tokens already includes reasoning (one comparable number).
	if out := u.OutputTokens + u.ReasoningOutputTokens; out != 0 {
		m["output_tokens"] = out
	}
	if u.CachedInputTokens != 0 {
		m["cache_read_input_tokens"] = u.CachedInputTokens
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func (l *codexLine) time() time.Time {
	return parseCodexTime(l.Timestamp)
}

func parseCodexTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// payloadType returns the payload's inner "type" (or "" for session_meta /
// turn_context whose payload has no such discriminator we need).
func (l *codexLine) payloadType() string {
	if len(l.Payload) == 0 {
		return ""
	}
	var pt codexPayloadType
	if json.Unmarshal(l.Payload, &pt) != nil {
		return ""
	}
	return pt.Type
}

func (l *codexLine) asSessionMeta() *codexSessionMeta {
	var m codexSessionMeta
	if json.Unmarshal(l.Payload, &m) != nil {
		return nil
	}
	return &m
}

func (l *codexLine) asMessage() *codexMessage {
	var m codexMessage
	if json.Unmarshal(l.Payload, &m) != nil {
		return nil
	}
	return &m
}

func (l *codexLine) asFunctionCall() *codexFunctionCall {
	var c codexFunctionCall
	if json.Unmarshal(l.Payload, &c) != nil {
		return nil
	}
	return &c
}

func (l *codexLine) asFunctionOutput() *codexFunctionOutput {
	var o codexFunctionOutput
	if json.Unmarshal(l.Payload, &o) != nil {
		return nil
	}
	return &o
}

func (l *codexLine) asReasoning() *codexReasoning {
	var r codexReasoning
	if json.Unmarshal(l.Payload, &r) != nil {
		return nil
	}
	return &r
}

// codexArgsToInput parses a function_call.arguments JSON string into a generic
// map for the tool block's tool_input (mirrors claude's structured ToolInput).
// Falls back to a {"_raw": ...} wrapper when arguments is not a JSON object.
func codexArgsToInput(args string) map[string]interface{} {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal([]byte(args), &m) == nil {
		return m
	}
	return map[string]interface{}{"_raw": args}
}

// codexPatchPath extracts the target file path from an apply_patch input body.
// codex's apply_patch embeds the path inside the patch text (not as a discrete
// field) on the first `*** Update File: <path>` / `*** Add File: <path>` /
// `*** Delete File: <path>` (and `*** Move to: <path>` for renames) line. This
// normalizes the path into tool_input["path"] in the BACKEND so the frontend's
// runtime-agnostic toolPath (path / file_path) renders it without knowing codex
// patch syntax. Returns "" when no File marker is present (honest empty).
func codexPatchPath(input string) string {
	for _, raw := range strings.Split(input, "\n") {
		line := strings.TrimSpace(raw)
		for _, marker := range []string{
			"*** Update File:",
			"*** Add File:",
			"*** Delete File:",
			"*** Move to:",
		} {
			if strings.HasPrefix(line, marker) {
				if p := strings.TrimSpace(strings.TrimPrefix(line, marker)); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

// isCodexNoise filters the non-user wrapper messages codex injects as user-role
// response_items (AGENTS.md preamble, environment_context, permissions, etc.).
// These mirror claude's command-echo noise and must not become user turns.
func isCodexNoise(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	return strings.HasPrefix(s, "# AGENTS.md") ||
		strings.HasPrefix(s, "<environment_context>") ||
		strings.HasPrefix(s, "<permissions instructions>") ||
		strings.HasPrefix(s, "<collaboration_mode>") ||
		strings.HasPrefix(s, "<user_instructions>") ||
		strings.HasPrefix(s, "<INSTRUCTIONS>") ||
		// Verified on a real rollout: codex re-injects its OWN interruption notice and the
		// expanded skill body as user-role messages (`<turn_aborted>` ×7, `<skill>` ×1).
		// They are runtime bookkeeping, not human input — counting them as user bubbles both
		// invented rounds and mis-classified the human's next line as a mid-run steer.
		strings.HasPrefix(s, "<turn_aborted>") ||
		strings.HasPrefix(s, "<skill>") ||
		strings.HasPrefix(s, "<name>")
}
