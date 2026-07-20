package transcript

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// toolLoc points at a tool block already appended to the transcript, so a later
// tool_result line can attach its output to the right block. useAt is the
// timestamp of the originating tool_use line, kept so an Agent block can derive
// its wall-clock duration from the (later) tool_result line's timestamp.
type toolLoc struct {
	t, b  int
	useAt time.Time
}

// taskNotification is a parsed claude `<task-notification>` payload. The runtime
// emits it as a `user` line with a bare-string content of:
//
//	<task-notification>
//	  <task-id>…</task-id>
//	  <tool-use-id>…</tool-use-id>
//	  <output-file>…</output-file>
//	  <status>completed|failed|killed</status>
//	  <summary>…</summary>
//	</task-notification>
//
// (verified against real ~/.claude jsonl — status ∈ {completed, failed, killed}).
type taskNotification struct {
	TaskID  string
	Status  string
	Summary string
}

var (
	tnTagRe     = regexp.MustCompile(`(?s)<task-notification>.*</task-notification>`)
	tnTaskIDRe  = regexp.MustCompile(`(?s)<task-id>(.*?)</task-id>`)
	tnStatusRe  = regexp.MustCompile(`(?s)<status>(.*?)</status>`)
	tnSummaryRe = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
)

// parseTaskNotification returns the parsed payload when text is a
// `<task-notification>` envelope, or nil otherwise.
func parseTaskNotification(text string) *taskNotification {
	s := strings.TrimSpace(text)
	if !tnTagRe.MatchString(s) {
		return nil
	}
	tn := &taskNotification{}
	if m := tnTaskIDRe.FindStringSubmatch(s); m != nil {
		tn.TaskID = strings.TrimSpace(m[1])
	}
	if m := tnStatusRe.FindStringSubmatch(s); m != nil {
		tn.Status = strings.TrimSpace(m[1])
	}
	if m := tnSummaryRe.FindStringSubmatch(s); m != nil {
		tn.Summary = strings.TrimSpace(m[1])
	}
	return tn
}

// rawLine is the union shape of a single claude jsonl line. content is decoded
// lazily because it is either a string or an array of typed parts.
type rawLine struct {
	Type      string          `json:"type"`
	IsMeta    bool            `json:"isMeta"`
	Timestamp string          `json:"timestamp"`
	AITitle   string          `json:"aiTitle"`
	Message   json.RawMessage `json:"message"`
	// Subtype discriminates `system` lines. The one that matters to a reader is
	// `compact_boundary` (claude auto-compacted the context — from here on the agent
	// cannot see the earlier conversation, which is part of the run's causal story).
	// Verified subtypes in a real transcript: compact_boundary, local_command,
	// stop_hook_summary, away_summary, scheduled_task_fire.
	Subtype string `json:"subtype"`
}

// rawMessage is the inner `message` object for user/assistant lines.
type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Usage   json.RawMessage `json:"usage"`
	// ID is the Anthropic message id (msg_XXX). claude jsonl writes ONE assistant
	// message's content blocks (thinking / text / tool_use) as SEPARATE lines that
	// all share this id (and each REPEATS the full usage) — coalesced back into one
	// turn by message.id in appendAssistantTurn (else a turn renders as N bubbles +
	// its usage counts N times).
	ID string `json:"id"`
	// Model is the engine that produced an assistant line (claude jsonl inlines it on
	// message.model). Surfaced onto the turn's usage block + tr.Meta so the overview
	// renders the model name + derives cost — instead of an honest-but-blank「—」.
	Model string `json:"model"`
	// StopReason is claude's yield fact: `end_turn` = the model handed control back
	// to the human (the run is over); `tool_use` = it is still working through a tool
	// loop. Verified distribution on a real 60 MB transcript: tool_use 1066 /
	// end_turn 53 / null 2. This is what lets the AgentRun projector tell a mid-run
	// steer apart from a fresh human intent — no time-gap guessing.
	StopReason string `json:"stop_reason"`
}

// interruptMarker matches the user-role line claude writes when the human hits ESC
// (`[Request interrupted by user]`, and the tool-permission variant). It is NOT a
// human message — it is the runtime's abort fact. Verified: 7 occurrences on the
// real transcript; treating them as user bubbles both fabricated rounds AND hid the
// interruption, which is why they become Turn.Terminal = aborted instead.
func isInterruptMarker(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "[Request interrupted by user")
}

// contentPart is one typed element of a message.content array.
type contentPart struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text"`
	Thinking  string                 `json:"thinking"`
	Name      string                 `json:"name"`
	ID        string                 `json:"id"`
	Input     map[string]interface{} `json:"input"`
	ToolUseID string                 `json:"tool_use_id"`
	IsError   bool                   `json:"is_error"`
	Content   json.RawMessage        `json:"content"` // tool_result body: string OR array
}

func (l *rawLine) time() time.Time {
	if l.Timestamp == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, l.Timestamp); err != nil {
			return time.Time{}
		}
	}
	return t
}

func (l *rawLine) msg() *rawMessage {
	if len(l.Message) == 0 {
		return nil
	}
	var m rawMessage
	if err := json.Unmarshal(l.Message, &m); err != nil {
		return nil
	}
	return &m
}

// contentParts returns the typed content parts when message.content is an array.
func (l *rawLine) contentParts() []contentPart {
	m := l.msg()
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	if m.Content[0] != '[' {
		return nil
	}
	var parts []contentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return nil
	}
	return parts
}

// contentString returns the content when message.content is a bare string.
func (l *rawLine) contentString() string {
	m := l.msg()
	if m == nil || len(m.Content) == 0 {
		return ""
	}
	if m.Content[0] != '"' {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// userText returns the user message's plain text (string content, or the
// concatenated text parts), used for title/turn-count detection.
func (l *rawLine) userText() string {
	if s := l.contentString(); s != "" {
		return s
	}
	var b strings.Builder
	for _, p := range l.contentParts() {
		if p.Type == "text" && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// usage decodes message.usage into a generic map for the usage footer.
func (l *rawLine) usage() map[string]interface{} {
	m := l.msg()
	if m == nil || len(m.Usage) == 0 {
		return nil
	}
	var u map[string]interface{}
	if err := json.Unmarshal(m.Usage, &u); err != nil {
		return nil
	}
	if len(u) == 0 {
		return nil
	}
	return u
}

// resultText flattens a tool_result's content (string or array of text parts).
func (p *contentPart) resultText() string {
	if len(p.Content) == 0 {
		return ""
	}
	if p.Content[0] == '"' {
		var s string
		if json.Unmarshal(p.Content, &s) == nil {
			return s
		}
		return ""
	}
	if p.Content[0] == '[' {
		var parts []contentPart
		if json.Unmarshal(p.Content, &parts) == nil {
			var b strings.Builder
			for _, sub := range parts {
				if sub.Text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(sub.Text)
				}
			}
			return b.String()
		}
	}
	return ""
}

// ── small helpers ───────────────────────────────────────────────────────────

func tsPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// isCommandEcho filters out the local-command / slash-command meta echoes that
// claude writes as user lines (they are tooling noise, not real user turns).
func isCommandEcho(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "<command-name>") ||
		strings.HasPrefix(s, "<local-command-") ||
		strings.HasPrefix(s, "<command-message>") ||
		strings.Contains(s, "<local-command-stdout>") ||
		strings.HasPrefix(s, "Caveat:")
}

var (
	cmdNameRe = regexp.MustCompile(`(?s)<command-name[^>]*>(.*?)</command-name>`)
	cmdArgsRe = regexp.MustCompile(`(?s)<command-args[^>]*>(.*?)</command-args>`)
)

// restoreSlashCommand recovers the human's actual words from a custom
// slash-command envelope (skill / user command). claude expands what the human
// typed (`/foo bar baz`) into a <command-message>/<command-name>/<command-args>
// wrapper on a single user line — the typed words survive only inside
// <command-args>; the rest is the runtime's own bookkeeping. The three tags'
// order is not fixed (real jsonl has both name-before-message and
// message-before-name), so each is located independently rather than by
// position or a hand-rolled offset.
//
// Returns ok=false when there is nothing to recover: an empty <command-args>
// (e.g. `/compact`, `/clear`, `/model`) is a real command but carries no human
// text, so it stays dropped like any other tooling echo.
func restoreSlashCommand(s string) (string, bool) {
	m := cmdArgsRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	args := strings.TrimSpace(m[1])
	if args == "" {
		return "", false
	}
	m = cmdNameRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	name := strings.TrimSpace(m[1])
	if name == "" {
		return "", false
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name // defensive: real samples always carry the slash already
	}
	return name + " " + args, true
}

// unwrapCommandEcho is the single place every isCommandEcho call site routes
// through, so a slash command's <command-args> gets recovered exactly once
// instead of being re-parsed at each of the three read paths (turn count,
// bubble assembly, bubble accept/reject). Ordinary text passes through
// untouched; a wrapper with recoverable args becomes "/name args" — a real
// human turn, previously lost entirely; every other echo shape (empty-args
// wrapper, local-command-stdout, Caveat) still resolves to "" and is dropped.
func unwrapCommandEcho(s string) string {
	if !isCommandEcho(s) {
		return s
	}
	if restored, ok := restoreSlashCommand(s); ok {
		return restored
	}
	return ""
}

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func intField(m map[string]interface{}, key string) int {
	if m == nil {
		return 0
	}
	// Tolerate both float64 (claude usage decoded straight from raw JSON) and
	// int/int64 (deepwork usageMap stores ints) so the Meta token totals are
	// correct for every source — not silently 0 for the deepwork path.
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// transcriptFirstUser returns the first user bubble text (title fallback).
func transcriptFirstUser(tr *Transcript) string {
	for _, t := range tr.Turns {
		if t.Role == "user" {
			for _, b := range t.Blocks {
				if b.Type == BlockUser {
					return truncate(b.Text, 80)
				}
			}
		}
	}
	return ""
}
