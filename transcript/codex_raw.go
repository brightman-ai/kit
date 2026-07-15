package transcript

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
// custom_tool_call_output. Its `output` has TWO shapes in real rollouts:
//
//	function_call_output      → a plain string
//	custom_tool_call_output   → an ARRAY of content parts [{type:"input_text", text:"…"}]
//
// Modeling it as `string` made every custom_tool_call_output line fail to unmarshal and
// be skipped whole — on a real 16 MB rollout that silently dropped 275 of 284 tool
// results in a single round (the UI showed the calls but never their output, and the
// projector could not tell a finished tool from an interrupted one). RawMessage + a
// tolerant decoder is why both shapes now land.
type codexFunctionOutput struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

// codexOutputPart is one element of the custom_tool_call_output array form.
type codexOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// text decodes either output shape into the tool result the UI renders.
func (o *codexFunctionOutput) text() string {
	if len(o.Output) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(o.Output, &s) == nil {
		return s
	}
	var parts []codexOutputPart
	if json.Unmarshal(o.Output, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if t := strings.TrimSpace(p.Text); t != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(o.Output) // unknown shape → keep the raw payload rather than losing it
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
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Effort          string `json:"effort"`
	ServiceTier     string `json:"service_tier"`
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
	// Codex input_tokens is INCLUSIVE of cached_input_tokens. The provider-neutral
	// contract uses mutually-exclusive token classes (like Claude), so input means
	// fresh/uncached input and cached input is surfaced once in its own class.
	if in := u.InputTokens - u.CachedInputTokens; in > 0 {
		m["input_tokens"] = in
	}
	// output_tokens is already inclusive of reasoning_output_tokens (the wire's
	// total_tokens equals input_tokens + output_tokens). Reasoning is a breakdown,
	// not a second billed bucket.
	if u.OutputTokens != 0 {
		m["output_tokens"] = u.OutputTokens
	}
	if u.ReasoningOutputTokens != 0 {
		m["thinking_tokens"] = u.ReasoningOutputTokens
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

// codexExecCommand projects Codex's custom exec wrapper onto the canonical
// tool_input.command field. The wrapper is JavaScript, not JSON:
//
//	const r = await tools.exec_command({ cmd: "rg ...", workdir: "..." });
//
// We do not execute or generally parse JavaScript. This bounded scanner recognizes
// only the generated call/object grammar, balances strings/comments/nesting, and
// decodes a top-level cmd string (or string array). Failure is an honest empty result;
// the caller keeps the lossless raw input alongside the optional projection.
func codexExecCommand(input string) string {
	const maxScan = 1 << 20
	if len(input) > maxScan {
		input = input[:maxScan]
	}
	const call = "tools.exec_command"
	for search := 0; search < len(input); {
		rel := strings.Index(input[search:], call)
		if rel < 0 {
			return ""
		}
		start := search + rel + len(call)
		i := skipJSSpace(input, start)
		if i >= len(input) || input[i] != '(' {
			search = start
			continue
		}
		i = skipJSSpace(input, i+1)
		if i >= len(input) || input[i] != '{' {
			search = start
			continue
		}
		if cmd := scanJSObjectCommand(input, i); cmd != "" {
			return cmd
		}
		search = start
	}
	return ""
}

func scanJSObjectCommand(s string, objectStart int) string {
	brace, bracket, paren := 1, 0, 0
	previous := byte('{')
	for i := objectStart + 1; i < len(s); {
		i = skipJSSpace(s, i)
		if i >= len(s) {
			break
		}
		ch := s[i]
		if brace == 1 && bracket == 0 && paren == 0 && (previous == '{' || previous == ',') {
			key, end, ok := scanJSPropertyKey(s, i)
			if ok {
				colon := skipJSSpace(s, end)
				if colon < len(s) && s[colon] == ':' {
					if key == "cmd" {
						if value, _, ok := scanJSCommandValue(s, skipJSSpace(s, colon+1)); ok {
							return strings.TrimSpace(value)
						}
						return ""
					}
				}
			}
		}
		switch ch {
		case '\'', '"', '`':
			_, next, ok := scanJSQuoted(s, i)
			if !ok {
				return ""
			}
			i = next
			previous = 'v'
			continue
		case '{':
			brace++
		case '}':
			brace--
			if brace == 0 {
				return ""
			}
		case '[':
			bracket++
		case ']':
			if bracket > 0 {
				bracket--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		}
		if brace == 1 && bracket == 0 && paren == 0 && (ch == '{' || ch == ',') {
			previous = ch
		} else if ch != ' ' && ch != '\t' && ch != '\r' && ch != '\n' {
			previous = ch
		}
		i++
	}
	return ""
}

func scanJSPropertyKey(s string, start int) (string, int, bool) {
	if start >= len(s) {
		return "", start, false
	}
	if s[start] == '\'' || s[start] == '"' {
		value, end, ok := scanJSQuoted(s, start)
		return value, end, ok
	}
	if !isJSIdentStart(s[start]) {
		return "", start, false
	}
	end := start + 1
	for end < len(s) && isJSIdentPart(s[end]) {
		end++
	}
	return s[start:end], end, true
}

func scanJSCommandValue(s string, start int) (string, int, bool) {
	if start >= len(s) {
		return "", start, false
	}
	if s[start] == '\'' || s[start] == '"' || s[start] == '`' {
		return scanJSQuoted(s, start)
	}
	if s[start] != '[' {
		return "", start, false
	}
	var values []string
	for i := start + 1; i < len(s); {
		i = skipJSSpace(s, i)
		if i >= len(s) {
			break
		}
		if s[i] == ']' {
			return strings.Join(values, " "), i + 1, len(values) > 0
		}
		if s[i] != '\'' && s[i] != '"' && s[i] != '`' {
			return "", i, false
		}
		value, next, ok := scanJSQuoted(s, i)
		if !ok {
			return "", i, false
		}
		values = append(values, value)
		i = skipJSSpace(s, next)
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		if i < len(s) && s[i] == ']' {
			return strings.Join(values, " "), i + 1, true
		}
		return "", i, false
	}
	return "", start, false
}

func scanJSQuoted(s string, start int) (string, int, bool) {
	if start >= len(s) {
		return "", start, false
	}
	quote := s[start]
	for i := start + 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			raw := s[start : i+1]
			if quote == '"' {
				if value, err := strconv.Unquote(raw); err == nil {
					return value, i + 1, true
				}
			}
			return decodeJSQuotedFallback(raw), i + 1, true
		}
		if quote != '`' && (s[i] == '\n' || s[i] == '\r') {
			return "", i, false
		}
	}
	return "", start, false
}

func decodeJSQuotedFallback(raw string) string {
	if len(raw) < 2 {
		return ""
	}
	body := raw[1 : len(raw)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' || i+1 >= len(body) {
			b.WriteByte(body[i])
			continue
		}
		i++
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '\n': // JavaScript line continuation
		case '\r':
			if i+1 < len(body) && body[i+1] == '\n' {
				i++
			}
		case 'x':
			if i+2 < len(body) {
				if v, err := strconv.ParseUint(body[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					break
				}
			}
			b.WriteByte('x')
		case 'u':
			if i+4 < len(body) {
				if v, err := strconv.ParseUint(body[i+1:i+5], 16, 16); err == nil {
					var buf [utf8.UTFMax]byte
					n := utf8.EncodeRune(buf[:], rune(v))
					b.Write(buf[:n])
					i += 4
					break
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}

func skipJSSpace(s string, start int) int {
	for i := start; i < len(s); {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
		case '/':
			if i+1 >= len(s) {
				return i
			}
			if s[i+1] == '/' {
				i += 2
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			if s[i+1] == '*' {
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return len(s)
				}
				i += end + 4
				continue
			}
			return i
		default:
			return i
		}
	}
	return len(s)
}

func isJSIdentStart(ch byte) bool {
	return ch == '_' || ch == '$' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}
func isJSIdentPart(ch byte) bool { return isJSIdentStart(ch) || ch >= '0' && ch <= '9' }

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
