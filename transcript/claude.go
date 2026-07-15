package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeSource reads the claude CLI's session storage:
//
//	~/.claude/projects/{encode(projectDir)}/*.jsonl
//
// where encode replaces every '/' in the absolute project dir with '-'
// (e.g. /home/user/my-project →
//
//	-home-user-my-project).
//
// Each *.jsonl is one session transcript (the SSOT). This source is strictly
// read-only: it never writes or deletes a jsonl.
type ClaudeSource struct {
	// Root is the projects base dir; defaults to ~/.claude/projects.
	Root string
}

// NewClaudeSource builds a ClaudeSource rooted at ~/.claude/projects (or the
// override in DW_CLAUDE_PROJECTS, useful for tests/sandboxes).
func NewClaudeSource() *ClaudeSource {
	return &ClaudeSource{Root: ClaudeProjectsRoot()} // root resolution: roots.go (single source)
}

func (s *ClaudeSource) Kind() string { return KindClaude }

// EncodeProjectDir maps an absolute project dir to claude's directory name.
// claude collapses BOTH '/' and '.' to '-', so `/home/u/.deepwork/ws` becomes
// `-home-u--deepwork-ws` (the '/.' → '--'). Encoding only '/' was a latent bug:
// it worked for dot-free paths (…/my-project) but pointed at a non-existent
// shard for any dotted path (…/.deepwork/…) — which broke session reads AND the
// collaborate jail's RW bind of that shard (jailed agent could not persist its turn).
func EncodeProjectDir(projectDir string) string {
	r := strings.ReplaceAll(projectDir, "/", "-")
	return strings.ReplaceAll(r, ".", "-")
}

// projectDirPath returns the encoded claude project directory for projectDir.
func (s *ClaudeSource) projectDirPath(projectDir string) string {
	return filepath.Join(s.Root, EncodeProjectDir(projectDir))
}

// TranscriptPathFor returns the absolute jsonl path claude stores a session under:
// <Root>/<EncodeProjectDir(projectDir)>/<id>.jsonl. It only builds the path (no
// existence check) — the caller (sessionactivity) stats it for mtime. Empty when
// either input is empty (an unresolvable transcript, not a guess).
func (s *ClaudeSource) TranscriptPathFor(projectDir, id string) string {
	projectDir = strings.TrimSpace(projectDir)
	id = strings.TrimSpace(id)
	if projectDir == "" || id == "" || filepath.Base(id) != id || strings.Contains(id, "..") {
		return ""
	}
	return filepath.Join(s.projectDirPath(projectDir), id+".jsonl")
}

// ListSessions scans the encoded project dir for *.jsonl files and extracts a
// lightweight SessionMeta per file (no full parse — only the cheap header scan).
func (s *ClaudeSource) ListSessions(ctx context.Context, projectDir string) ([]SessionMeta, error) {
	dir := s.projectDirPath(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no claude history for this project — honest empty
		}
		return nil, err
	}

	out := make([]SessionMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		meta := s.scanMeta(path, strings.TrimSuffix(e.Name(), ".jsonl"))
		out = append(out, meta)
	}
	return out, nil
}

// CountSessionsForDir returns the number of *.jsonl sessions claude stored for
// projectDir — a cheap directory listing (no file parse). claude shards by the
// encoded project dir, so this touches only that one dir's entries, never the
// whole projects tree. Used by the GET /api/workspaces session_count fast-path
// (the codex equivalent CountSessionsByDir sweeps the whole tree once).
//
// A missing dir is an honest 0 (no claude history), never an error.
func (s *ClaudeSource) CountSessionsForDir(projectDir string) int {
	dir := s.projectDirPath(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

// scanMeta does a single streaming pass over a jsonl to extract list metadata:
// title (ai-title event preferred, else first real user message), created (first
// timestamp), updated (last timestamp), turn_count (number of independent human
// intents — mid-run steers are amendments, matching AgentRun/RoundNav semantics).
func (s *ClaudeSource) scanMeta(path, id string) SessionMeta {
	meta := SessionMeta{ID: id, Source: KindClaude, SsotPath: path}

	// (path,size,mtime) 记忆化：未变文件零重解析（metacache.go, 2026-07-03 轮询风暴修复；
	// claude 单 jsonl 可达 18MB，此前每次列表请求全量重解析）。
	cached, st, hit := loadMetaCache(path)
	if hit {
		return cached.meta
	}

	f, err := os.Open(path)
	if err != nil {
		return meta
	}
	defer f.Close()

	var aiTitle, firstUser string
	var firstTS, lastTS time.Time
	userTurns := 0
	runOpen := false

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20) // tolerate very long lines (18MB files)
	for sc.Scan() {
		var line rawLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		if ts := line.time(); !ts.IsZero() {
			if firstTS.IsZero() {
				firstTS = ts
			}
			lastTS = ts
		}
		switch line.Type {
		case "ai-title":
			if line.AITitle != "" {
				aiTitle = line.AITitle
			}
		case "user":
			if line.IsMeta {
				continue // skip local-command / slash-command meta echoes
			}
			txt := line.userText()
			if txt == "" || isCommandEcho(txt) {
				continue
			}
			// The list's turn_count must mean the same thing as the round count the
			// transcript view shows (an independent human intent). A task-notification and
			// an ESC interrupt marker are runtime bookkeeping written on user-role lines —
			// counting them made the list claim rounds the human never sent.
			if isInterruptMarker(txt) {
				runOpen = false
				continue
			}
			if parseTaskNotification(txt) != nil {
				continue
			}
			// Same adapter fact as LoadTranscript: a human line while the model is
			// inside its tool loop is a steer/amendment, not another round. Parsing
			// stop_reason here adds no second domain rule and keeps list/RoundNav exact.
			if !runOpen {
				userTurns++
				runOpen = true
			}
			if firstUser == "" {
				firstUser = txt
			}
		case "assistant":
			if m := line.msg(); m != nil && (m.StopReason == "end_turn" || m.StopReason == "stop_sequence") {
				runOpen = false
			}
		}
	}

	meta.Title = firstNonEmpty(aiTitle, truncate(firstUser, 80), "claude session "+shortID(id))
	meta.CreatedAt = firstTS
	meta.UpdatedAt = lastTS
	if meta.UpdatedAt.IsZero() {
		// fall back to file mtime so the row sorts sensibly
		if st, err := os.Stat(path); err == nil {
			meta.UpdatedAt = st.ModTime()
			if meta.CreatedAt.IsZero() {
				meta.CreatedAt = st.ModTime()
			}
		}
	}
	meta.TurnCount = userTurns
	storeMetaCache(path, st, meta, "", true)
	return meta
}

// LoadTranscript fully parses one jsonl into ordered turns/blocks.
func (s *ClaudeSource) LoadTranscript(ctx context.Context, ref SessionRef) (*Transcript, error) {
	path := s.TranscriptPathFor(ref.ProjectDir, ref.ID)
	if path == "" {
		return nil, os.ErrNotExist
	}
	return s.LoadTranscriptFile(ctx, path, ref.ID)
}

// LoadTranscriptFile parses a Claude transcript already resolved by a trusted caller.
// It exists for child AgentExecution streams, which live under
// <root-session>/subagents/agent-<id>.jsonl rather than the project shard's top level.
// HTTP handlers must capability-check the agent against its root tree before calling
// this method; accepting arbitrary user paths is intentionally not this layer's job.
func (s *ClaudeSource) LoadTranscriptFile(ctx context.Context, path, refID string) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.loadTranscriptReader(ctx, f, refID)
}

func (s *ClaudeSource) loadTranscriptReader(ctx context.Context, reader io.Reader, refID string) (*Transcript, error) {
	tr := &Transcript{Source: KindClaude, Ref: refID, Meta: map[string]interface{}{}}
	// tool_use id → (turnIdx, blockIdx) so a later tool_result can attach.
	pending := map[string]toolLoc{}
	var aiTitle string
	var totalIn, totalOut, totalCacheRead int
	// Coalesce the split lines of ONE claude assistant message (thinking/text/tool_use
	// each on its own jsonl line, all sharing message.id, possibly across intervening
	// tool_result user lines) back into a single turn. Tracks the last assistant turn's
	// index + message.id; a match folds blocks in instead of spawning another bubble.
	lastAsstIdx := -1
	lastAsstMsgID := ""
	// runOpen = the model is still working on the current human intent (its last message
	// said stop_reason=tool_use, no end_turn / interrupt since). It is what makes a human
	// line's meaning DECIDABLE: while open, a human line is a queued steer (the runtime
	// records exactly that as `queue-operation/enqueue` — 72 in the real transcript);
	// once yielded, the next human line is a new intent. The projector never re-derives
	// this — the adapter states it on Turn.InputKind.
	runOpen := false

	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var line rawLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		switch line.Type {
		case "ai-title":
			if line.AITitle != "" {
				aiTitle = line.AITitle
			}
			continue
		case "system":
			// claude's own auto-compaction boundary — part of the run's causal story
			// (from here on the agent cannot see the earlier context), so it is a visible
			// process segment. Every other system subtype is UI metadata.
			if line.Subtype == "compact_boundary" {
				tr.Turns = append(tr.Turns, Turn{
					Index: len(tr.Turns), Role: "assistant", At: tsPtr(line.time()),
					Blocks: []Block{{Type: BlockCompaction, Text: "上下文已自动压缩"}},
				})
			}
			continue
		case "user":
			if line.IsMeta {
				continue
			}
			s.appendUserTurn(tr, &line, pending, &runOpen)
		case "assistant":
			s.appendAssistantTurn(tr, &line, pending, &totalIn, &totalOut, &totalCacheRead, &lastAsstIdx, &lastAsstMsgID, &runOpen)
		default:
			// mode / permission-mode / last-prompt / attachment / file-history-snapshot /
			// queue-operation → metadata (queue-operation's payload is re-delivered as a
			// normal user line when it dequeues, so consuming it here would double-count).
			continue
		}
	}

	tr.Title = firstNonEmpty(aiTitle, transcriptFirstUser(tr), "claude session "+shortID(refID))
	tr.Meta["input_tokens"] = totalIn
	tr.Meta["output_tokens"] = totalOut
	tr.Meta["cache_read_tokens"] = totalCacheRead
	return tr, nil
}

// LoadTranscriptWindow finds runtime yield boundaries from the tail and parses only
// that byte range. Large immutable prefixes never enter the JSON/block parser.
func (s *ClaudeSource) LoadTranscriptWindow(ctx context.Context, ref SessionRef, req WindowRequest) (*WindowResult, error) {
	path := s.TranscriptPathFor(ref.ProjectDir, ref.ID)
	if path == "" {
		return nil, os.ErrNotExist
	}
	return s.LoadTranscriptFileWindow(ctx, path, ref.ID, req)
}

// LoadTranscriptFileWindow is the trusted-path variant used by child executions.
func (s *ClaudeSource) LoadTranscriptFileWindow(ctx context.Context, path, refID string, req WindowRequest) (*WindowResult, error) {
	rng, err := resolveSourceWindow(ctx, path, req, claudeYieldBoundary)
	if err != nil {
		return nil, err
	}
	closer, reader, err := sectionReader(path, rng.start, rng.end)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	tr, err := s.loadTranscriptReader(ctx, reader, refID)
	if err != nil {
		return nil, err
	}
	tr.Runs = ProjectAgentRuns(tr)
	tr.Turns = nil
	return &WindowResult{
		Transcript: tr, Before: rng.start, HasMore: rng.hasMore,
		Version: rng.version, Generation: rng.generation, Reset: rng.reset,
		BytesParsed: rng.end - rng.start,
	}, nil
}

func claudeYieldBoundary(raw []byte) (bool, string) {
	if len(raw) == 0 {
		return false, ""
	}
	var line rawLine
	if json.Unmarshal(raw, &line) != nil {
		return false, ""
	}
	if line.Type == "assistant" {
		if m := line.msg(); m != nil && (m.StopReason == "end_turn" || m.StopReason == "stop_sequence") {
			return true, "assistant:" + m.ID
		}
	}
	if line.Type == "user" && !line.IsMeta {
		text := line.contentString()
		if text == "" {
			var b strings.Builder
			for _, part := range line.contentParts() {
				if part.Type == "text" {
					b.WriteString(part.Text)
				}
			}
			text = b.String()
		}
		if isInterruptMarker(text) {
			return true, "interrupt:" + line.Timestamp
		}
	}
	return false, ""
}

// appendUserTurn turns a user line into either a tool_result attachment (when
// its content carries tool_result blocks) and/or a user_bubble turn.
func (s *ClaudeSource) appendUserTurn(tr *Transcript, line *rawLine, pending map[string]toolLoc, runOpen *bool) {
	at := line.time()
	parts := line.contentParts()

	// First, route any tool_result blocks back onto their originating tool block.
	var bubbleText strings.Builder
	for _, p := range parts {
		if p.Type == "tool_result" {
			loc, ok := pending[p.ToolUseID]
			if !ok {
				// Orphan result (its call is not in this transcript — truncated file,
				// resumed session). Keep it VISIBLE + counted rather than dropping content
				// on the floor; the projector reports it in RunDiagnostic.
				tr.Turns = append(tr.Turns, Turn{
					Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
					Blocks: []Block{{
						Type: BlockTool, EventID: p.ToolUseID, ToolUseID: p.ToolUseID,
						ToolResult: p.resultText(), IsError: p.IsError,
						ResultSeen: true, Orphan: true, EndedAt: tsPtr(at),
					}},
				})
				continue
			}
			blk := &tr.Turns[loc.t].Blocks[loc.b]
			blk.ToolResult = p.resultText()
			blk.IsError = p.IsError
			blk.ResultSeen = true // the call actually completed (vs. one cut off by an interrupt)
			blk.EndedAt = tsPtr(at)
			// Every Claude tool has an honest tool_use→tool_result interval.
			// Unmatched calls never execute this branch, so interrupted time is
			// kept out of completed-latency aggregates by construction.
			if !loc.useAt.IsZero() && !at.IsZero() {
				if d := at.Sub(loc.useAt).Milliseconds(); d > 0 {
					blk.DurationMs = int(d)
				}
			}
			delete(pending, p.ToolUseID)
			continue
		}
		if p.Type == "text" && p.Text != "" {
			if bubbleText.Len() > 0 {
				bubbleText.WriteString("\n")
			}
			bubbleText.WriteString(p.Text)
		}
	}
	if s := line.contentString(); s != "" && !isCommandEcho(s) {
		if bubbleText.Len() > 0 {
			bubbleText.WriteString("\n")
		}
		bubbleText.WriteString(s)
	}

	text := strings.TrimSpace(bubbleText.String())
	if text == "" || isCommandEcho(text) {
		return
	}

	// ESC interrupt: the runtime's abort fact, not a human message. Stamp it onto the
	// open assistant turn (Terminal=aborted) so the AgentRun projector closes the run
	// honestly — and emit NO user bubble (it never was one). Without this the marker
	// both invented a round and left the run looking like it was still working, so the
	// human's post-ESC retype got mis-bucketed as a mid-run steer.
	if isInterruptMarker(text) {
		for i := len(tr.Turns) - 1; i >= 0; i-- {
			if tr.Turns[i].Role == "assistant" {
				if tr.Turns[i].Terminal == "" { // never overwrite an existing yield fact
					tr.Turns[i].Terminal = TerminalAborted
				}
				break
			}
		}
		*runOpen = false // the run is over → the human's next line is a NEW intent
		return
	}

	// task-notification: a runtime system event (background task / agent done),
	// NOT a real user message. Surface it as a compact task-notification block
	// (its own turn) instead of letting it fall through to a full-width bubble.
	if tn := parseTaskNotification(text); tn != nil {
		turn := Turn{Index: len(tr.Turns), Role: "user", At: tsPtr(at), InputKind: InputSystem}
		turn.Blocks = append(turn.Blocks, Block{
			Type:         BlockTaskNotification,
			EventID:      tn.TaskID,
			NotifyStatus: tn.Status,
			TaskID:       tn.TaskID,
			Text:         tn.Summary,
		})
		tr.Turns = append(tr.Turns, turn)
		return
	}

	// The human line's MEANING, stated by the adapter (never guessed downstream): while
	// the model is still in its tool loop, a typed message is queued INTO that run (a
	// steer); once it has yielded, the next message opens a new round.
	kind := InputIntent
	if *runOpen {
		kind = InputAmendment
	} else {
		*runOpen = true
	}
	turn := Turn{Index: len(tr.Turns), Role: "user", At: tsPtr(at), InputKind: kind}
	turn.Blocks = append(turn.Blocks, Block{Type: BlockUser, Text: text})
	tr.Turns = append(tr.Turns, turn)
}

// appendAssistantTurn parses an assistant line into a turn of typed blocks and
// registers any tool_use ids so their results can be back-attached.
func (s *ClaudeSource) appendAssistantTurn(tr *Transcript, line *rawLine, pending map[string]toolLoc, totalIn, totalOut, totalCacheRead *int, lastAsstIdx *int, lastAsstMsgID *string, runOpen *bool) {
	at := line.time()
	m := line.msg()
	msgID := ""
	if m != nil {
		msgID = m.ID
	}
	// The yield fact, read ONCE here: end_turn means the model handed control back to the
	// human → this iteration is the run's terminal one, so its text IS the answer (that is
	// a domain fact, not "the last text block we happened to see").
	yields := m != nil && (m.StopReason == "end_turn" || m.StopReason == "stop_sequence")

	// Coalesce: one claude assistant message's blocks arrive as SEPARATE jsonl lines
	// sharing message.id (可跨中间的 tool_result user 行 → 见 219 例非连续), and each
	// line REPEATS the full usage. Fold a same-id line into the current assistant turn
	// so a turn = one bubble (not N) and usage counts once (not N×). message.id 全局
	// 唯一 → id 命中必是同一逻辑消息, 无需按行连续性判定。
	merge := msgID != "" && *lastAsstMsgID == msgID &&
		*lastAsstIdx >= 0 && *lastAsstIdx < len(tr.Turns) &&
		tr.Turns[*lastAsstIdx].Role == "assistant"

	turnIdx := *lastAsstIdx
	if !merge {
		turnIdx = len(tr.Turns)
		tr.Turns = append(tr.Turns, Turn{Index: turnIdx, Role: "assistant", At: tsPtr(at)})
	}
	turn := &tr.Turns[turnIdx] // stable: we only append to turn.Blocks below, not tr.Turns
	// Advance the turn's timestamp to the LATEST split line (chronological jsonl) so 总耗时
	// = completion − preceding user send stays the full turn duration, not just time-to-first
	// -thinking-line (folding onto the first line's ts would shrink a 24s turn to ~5s).
	if merge && !at.IsZero() {
		turn.At = tsPtr(at)
	}

	for _, p := range line.contentParts() {
		// Stable identity from the SOURCE event (message id + ordinal), so a reload
		// reproduces the same segment ids and the user's expand/collapse choices stay bound
		// to the same segments.
		eventID := msgID + "#" + itoa(len(turn.Blocks))
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				turn.Blocks = append(turn.Blocks, Block{
					Type: BlockText, Text: p.Text, EventID: eventID,
					Final: yields, // terminal iteration's text = the run's answer
				})
			}
		case "thinking":
			if strings.TrimSpace(p.Thinking) != "" {
				turn.Blocks = append(turn.Blocks, Block{Type: BlockThinking, Text: p.Thinking, EventID: eventID})
			}
		case "tool_use":
			blk := Block{ToolName: p.Name, ToolUseID: p.ID, ToolInput: p.Input, EventID: p.ID, StartedAt: tsPtr(at)}
			if p.Name == "Agent" {
				// Agent tool_use = subagent dispatch → AgentBlock subflow.
				blk.Type = BlockAgent
				blk.SubagentType = stringField(p.Input, "subagent_type")
				blk.Description = stringField(p.Input, "description")
			} else {
				blk.Type = BlockTool
			}
			turn.Blocks = append(turn.Blocks, blk)
			pending[p.ID] = toolLoc{t: turnIdx, b: len(turn.Blocks) - 1, useAt: at}
		}
	}

	// Engine (message.model) → tr.Meta once, for the overview model name + cost derivation.
	// Usage counted ONCE per message.id: each split line repeats the full usage, so fold
	// it in only while this turn carries none yet (turnHasUsage) — else totals inflate N×.
	if m != nil && m.Model != "" {
		if _, ok := tr.Meta["model"]; !ok {
			tr.Meta["model"] = m.Model
		}
		if u := line.usage(); u != nil && !turnHasUsage(turn) {
			u["model"] = m.Model
			turn.Blocks = append(turn.Blocks, Block{Type: BlockUsage, Usage: u})
			*totalIn += intField(u, "input_tokens")
			*totalOut += intField(u, "output_tokens")
			*totalCacheRead += intField(u, "cache_read_input_tokens")
		}
	} else if u := line.usage(); u != nil && !turnHasUsage(turn) {
		turn.Blocks = append(turn.Blocks, Block{Type: BlockUsage, Usage: u})
		*totalIn += intField(u, "input_tokens")
		*totalOut += intField(u, "output_tokens")
		*totalCacheRead += intField(u, "cache_read_input_tokens")
	}

	// Yield fact (AgentRun boundary): end_turn closes the run; tool_use means the loop
	// continues. Split lines of one message repeat stop_reason → merging is idempotent.
	if yields {
		turn.Terminal = TerminalEndTurn
		*runOpen = false // yielded → the human's next line opens a NEW round
	}

	// A brand-new turn that produced nothing renderable → drop it (preserve prior
	// behavior). Its yield fact must NOT die with it: hand the terminal to the last
	// surviving assistant turn, else the run would stay open and swallow the next
	// human message as a steer.
	if !merge && len(turn.Blocks) == 0 {
		terminal := turn.Terminal
		tr.Turns = tr.Turns[:turnIdx]
		if terminal != "" {
			for i := len(tr.Turns) - 1; i >= 0; i-- {
				if tr.Turns[i].Role == "assistant" {
					tr.Turns[i].Terminal = terminal
					break
				}
			}
		}
		return
	}
	*lastAsstIdx = turnIdx
	*lastAsstMsgID = msgID
}

// turnHasUsage reports whether a usage block was already folded into this turn — the
// dedupe guard for a claude message whose duplicated split lines each repeat its usage.
func turnHasUsage(turn *Turn) bool {
	for i := range turn.Blocks {
		if turn.Blocks[i].Type == BlockUsage {
			return true
		}
	}
	return false
}
