package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DeepworkSessionProvider is the narrow read API DeepworkSource needs from the
// conversation processor. Declared here (not importing internal/conversation)
// to keep this package free of a heavy dependency and trivially testable with a
// fake. The webui wiring adapts *conversation.Processor onto this interface.
//
// CHG-015 P2/P3 (路 A): the deepwork transcript file (dw-<id>.jsonl) is the
// content SSOT — LoadTranscript reads it, not the DB; ListSessions treats the
// transcript directory as the index (目录即索引). The provider survives only as
// a cache/overlay: (a) workspace-scoped ordering + title rename for ListSessions
// and (b) a graceful content fallback when a session has no jsonl yet (pre-P1 /
// file missing). Delete the DB and both list + open still work from the files.
type DeepworkSessionProvider interface {
	// ListWorkspaceSessions returns the deepwork-owned sessions for a workspace.
	ListWorkspaceSessions(ctx context.Context, workspaceID int64) ([]DeepworkSessionMeta, error)
	// KnownSessionIDs returns the set of ALL deepwork session ids the DB knows about
	// (any workspace, any source) as decimal strings. Directory-as-index recovery uses
	// it to recover ONLY genuine orphans: a transcript file whose id is globally known
	// but absent from THIS workspace's rows belongs to another workspace/source and must
	// not be re-surfaced (CHG-016 R3 cross-workspace session leak). A nil map (provider
	// error / DB gone) degrades to recover-all, preserving the "delete DB still lists".
	KnownSessionIDs(ctx context.Context) (map[string]struct{}, error)
	// LoadSessionTurns returns the typed turns for one deepwork session (DB
	// fallback only — the jsonl file is the primary read source).
	LoadSessionTurns(ctx context.Context, sessionID int64) ([]DeepworkTurn, error)
}

// DeepworkSessionMeta is the projected metadata for a deepwork session.
type DeepworkSessionMeta struct {
	ID        int64
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	TurnCount int
}

// DeepworkTurn is the projected user/assistant exchange of a deepwork session.
type DeepworkTurn struct {
	UserInput string
	AIOutput  string
	At        time.Time
}

// DeepworkSource exposes the deepwork self-hosted agent sessions. CHG-015 路 A:
// the transcript file (transcriptDir/dw-<id>.jsonl) is the content SSOT — three
// sources (claude/codex/deepwork) now all read their own jsonl and project into
// the same Transcript model. The DB is a projection cache / list index, not the
// fact source: delete the DB and the conversation still rebuilds from the file.
type DeepworkSource struct {
	provider      DeepworkSessionProvider
	workspaceID   int64
	transcriptDir string // dir holding dw-<id>.jsonl (the content SSOT)
}

// NewDeepworkSource binds a provider + the workspace id this project maps to.
// transcriptDir is the directory holding the dw-<id>.jsonl files; when empty the
// source degrades to the DB provider (so old wiring / tests without a dir still
// work). Prefer NewDeepworkSourceWithDir to enable file-SSOT reads.
func NewDeepworkSource(p DeepworkSessionProvider, workspaceID int64) *DeepworkSource {
	return &DeepworkSource{provider: p, workspaceID: workspaceID}
}

// NewDeepworkSourceWithDir is the CHG-015 P2 constructor: it additionally binds
// the transcript directory so LoadTranscript reads dw-<id>.jsonl (file SSOT),
// falling back to the DB provider only when the file is absent/empty.
func NewDeepworkSourceWithDir(p DeepworkSessionProvider, workspaceID int64, transcriptDir string) *DeepworkSource {
	return &DeepworkSource{provider: p, workspaceID: workspaceID, transcriptDir: strings.TrimSpace(transcriptDir)}
}

func (s *DeepworkSource) Kind() string { return KindDeepwork }

// ListSessions enumerates the deepwork sessions for this workspace. CHG-015 P3
// (目录即索引): the transcript directory is the session index — ListSessions
// scans transcriptDir for dw-<id>.jsonl (the SSOT) and the DB provider is a
// cache/overlay (title rename, workspace-scoped ordering). The merge rule:
//
//	DB row present  → use it (authoritative title/ordering, workspace-scoped),
//	                  stamp the on-disk path so the row round-trips to the file.
//	file-only (orphan, e.g. DB deleted/empty) → recover the row from the file
//	                  via the cheap header scan (claude.scanMeta parity).
//
// So deleting the DB does NOT empty the list: every dw-<id>.jsonl still surfaces
// (lazy rebuild — no DB write, the file is both index and content source).
func (s *DeepworkSource) ListSessions(ctx context.Context, projectDir string) ([]SessionMeta, error) {
	// DB index (fast path / overlay). A provider error is non-fatal: fall back
	// to the pure directory scan so a missing/broken DB still lists from files.
	byID := map[string]SessionMeta{}
	var order []string
	if s.provider != nil {
		if rows, err := s.provider.ListWorkspaceSessions(ctx, s.workspaceID); err == nil {
			for _, r := range rows {
				id := strconv.FormatInt(r.ID, 10)
				title := r.Title
				if strings.TrimSpace(title) == "" {
					title = "deepwork session #" + id
				}
				byID[id] = SessionMeta{
					ID:        id,
					Source:    KindDeepwork,
					SsotPath:  s.transcriptPath(id),
					Title:     title,
					CreatedAt: r.CreatedAt,
					UpdatedAt: r.UpdatedAt,
					TurnCount: r.TurnCount,
				}
				order = append(order, id)
			}
		}
	}

	// CHG-016 R3 (workspace isolation): the transcript dir is a single FLAT shared dir
	// (dw-<id>.jsonl for EVERY workspace/source — the per-project dir migration is P2.5),
	// so directory-as-index recovery cannot scope by workspace on its own. The DB is the
	// only workspace/source map. Fetch the set of ALL globally-known ids: a file whose id
	// is globally known but NOT in this workspace's rows belongs to another workspace or
	// to chat/topic — recovering it leaked every session into every project (Human-found).
	// A nil set (provider nil / DB gone) degrades to recover-all (preserves "delete DB
	// still lists" — scoping is impossible without the DB anyway).
	var globalKnown map[string]struct{}
	if s.provider != nil {
		globalKnown, _ = s.provider.KnownSessionIDs(ctx)
	}

	// Directory-as-index recovery: surface only GENUINE orphans (dw-<id>.jsonl with no DB
	// row anywhere). The file header carries no workspace_id, so a globally-known id that
	// is not ours must be skipped to avoid the cross-workspace leak.
	for _, id := range s.scanTranscriptIDs() {
		if _, known := byID[id]; known {
			continue
		}
		if globalKnown != nil {
			if _, elsewhere := globalKnown[id]; elsewhere {
				continue // belongs to another workspace/source — do not leak
			}
		}
		if meta, ok := scanDeepworkMeta(s.transcriptPath(id), id); ok {
			byID[id] = meta
			order = append(order, id)
		}
	}

	out := make([]SessionMeta, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// scanTranscriptIDs lists the dw-<id>.jsonl session ids in the transcript dir
// (the on-disk index). Empty when no dir is wired (DB-only mode) or the dir is
// absent — an honest empty, never an error (a degraded fs must not break list).
func (s *DeepworkSource) scanTranscriptIDs() []string {
	if s.transcriptDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.transcriptDir)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "dw-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(name, "dw-"), ".jsonl"))
	}
	return ids
}

// transcriptPath returns the dw-<id>.jsonl path (flat layout, CHG-015 P2). The
// per-project directory migration (transcripts/projects/<enc-rootDir>/) is P2.5;
// this trunk reads the file from the existing flat dir so the read-from-file
// switch lands without a data migration. Falls back to the deepwork:// ref when
// no dir is wired (DB-only mode).
func (s *DeepworkSource) transcriptPath(id string) string {
	if s.transcriptDir == "" {
		return "deepwork://session/" + id
	}
	return filepath.Join(s.transcriptDir, "dw-"+id+".jsonl")
}

// LoadTranscript reads dw-<id>.jsonl (the content SSOT) into turns/blocks. When
// the file is missing/empty (pre-P1 session or never-written) it degrades to the
// DB provider so old sessions still display (零回归). The file path carries
// thinking/tool/usage blocks (P1 写补) so the workArea panel rebuilds from the
// file with no DB read.
// SessionRuntimeResolver is an OPTIONAL provider capability (type-asserted): it maps a
// deepwork session id to the external CLI runtime that OWNS its transcript. A provider
// that does not implement it (test fakes) simply keeps the deepwork-native read path.
type SessionRuntimeResolver interface {
	// SessionRuntimeInfo returns the session's runtime ("claude"/"codex"/…) and the
	// upstream CLI-owned conversation id (empty for a native deepwork_api session).
	SessionRuntimeInfo(ctx context.Context, sessionID int64) (runtime, runtimeSessionID string, err error)
}

func (s *DeepworkSource) LoadTranscript(ctx context.Context, ref SessionRef) (*Transcript, error) {
	// SSOT (路 D): a deepwork session BACKED by an external CLI runtime does NOT own its
	// transcript — the runtime does. The runtime's own jsonl is the COMPLETE record,
	// including out-of-band collaborate-driven turns (a jailed `claude --resume` writes
	// the claude jsonl, never the deepwork Recorder). So read via the runtime source, not
	// the deepwork re-recording (dw-<id>.jsonl) — which is a projection that drifts the
	// moment a turn bypasses the Recorder. This ends the "one conversation, two files"
	// redundancy for CLI sessions. deepwork-native (deepwork_api, in-process) sessions
	// have no runtime jsonl → the native file IS their SSOT (fall through below).
	if r, ok := s.provider.(SessionRuntimeResolver); ok {
		if id, err := strconv.ParseInt(strings.TrimSpace(ref.ID), 10, 64); err == nil {
			if rt, rsid, rerr := r.SessionRuntimeInfo(ctx, id); rerr == nil && strings.TrimSpace(rsid) != "" {
				runtimeRef := SessionRef{ProjectDir: ref.ProjectDir, ID: strings.TrimSpace(rsid)}
				switch rt {
				case KindClaude:
					return NewClaudeSource().LoadTranscript(ctx, runtimeRef)
				case KindCodex:
					return NewCodexSource().LoadTranscript(ctx, runtimeRef)
				}
			}
		}
	}
	if s.transcriptDir != "" {
		path := filepath.Join(s.transcriptDir, "dw-"+ref.ID+".jsonl")
		if tr, ok := s.loadFromFile(path, ref.ID); ok {
			return tr, nil
		}
		// fall through to DB when the jsonl is absent/empty (graceful).
	}
	return s.loadFromDB(ctx, ref)
}

// loadFromFile parses the native transcript jsonl into the unified model. ok is
// false when the file cannot be opened or yields zero turns (→ caller falls back
// to the DB), so a missing/empty/legacy-stub file never crashes nor blanks out.
func (s *DeepworkSource) loadFromFile(path, id string) (tr *Transcript, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	tr = &Transcript{Source: KindDeepwork, Ref: id, Meta: map[string]interface{}{}}
	var totalIn, totalOut, totalCacheRead int
	var sawUsage bool

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20) // tolerate very long lines
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var line NativeEntry
		if err := json.Unmarshal(raw, &line); err != nil {
			continue // ignore half-written / corrupt lines (complete-line守卫)
		}
		switch line.Type {
		case "user":
			s.appendUserTurn(tr, &line)
		case "assistant":
			s.appendAssistantTurn(tr, &line, &totalIn, &totalOut, &totalCacheRead, &sawUsage)
		case "result":
			// The `result` entry carries the turn's wall-clock duration (the assistant
			// usage inlines only ttft/tokens). Stitch it onto the just-closed assistant
			// turn's usage block so the replay footer shows 总耗时 — the SAME wall clock
			// the live Done frame carried (live≡replay). Duration otherwise died with the
			// skipped result line → replay 总耗时 blank.
			attachResultDuration(tr, &line)
		default:
			// progress / unknown → not a standalone reread block (per-token `progress`
			// events are the live stream, not reread).
			continue
		}
	}

	if len(tr.Turns) == 0 {
		return nil, false // empty/legacy-stub → let DB fallback try
	}
	if sawUsage {
		tr.Meta["input_tokens"] = totalIn
		tr.Meta["output_tokens"] = totalOut
		tr.Meta["cache_read_tokens"] = totalCacheRead
	}
	tr.Title = firstNonEmpty(transcriptFirstUser(tr), "deepwork session "+shortID(id))
	return tr, true
}

// appendUserTurn turns a native `user` line into a user_bubble turn.
func (s *DeepworkSource) appendUserTurn(tr *Transcript, line *NativeEntry) {
	if line.Message == nil {
		return
	}
	at := nativeEntryTime(line)
	var sb strings.Builder
	for _, c := range line.Message.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(c.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return
	}
	tr.Turns = append(tr.Turns, Turn{
		Index: len(tr.Turns), Role: "user", At: tsPtr(at),
		Blocks: []Block{{Type: BlockUser, Text: text}},
	})
}

// appendAssistantTurn parses a native `assistant` line into a turn of typed
// blocks: thinking → tool (tool_use + attached tool_result) → text → usage,
// mirroring the writer's block order (AppendAssistant) and claude's reread path.
func (s *DeepworkSource) appendAssistantTurn(tr *Transcript, line *NativeEntry, totalIn, totalOut, totalCacheRead *int, sawUsage *bool) {
	if line.Message == nil {
		return
	}
	at := nativeEntryTime(line)
	turn := Turn{Index: len(tr.Turns), Role: "assistant", At: tsPtr(at)}
	// tool_use id → block index so a following tool_result attaches its output.
	pending := map[string]int{}

	for i := range line.Message.Content {
		c := &line.Message.Content[i]
		switch c.Type {
		case "thinking":
			if strings.TrimSpace(c.Thinking) != "" {
				turn.Blocks = append(turn.Blocks, Block{Type: BlockThinking, Text: c.Thinking})
			}
		case "text":
			if strings.TrimSpace(c.Text) != "" {
				turn.Blocks = append(turn.Blocks, Block{Type: BlockText, Text: c.Text})
			}
		case "tool_use":
			turn.Blocks = append(turn.Blocks, Block{
				Type:      BlockTool,
				ToolName:  c.Name,
				ToolUseID: c.ID,
				ToolInput: nativeContentInputMap(c),
			})
			if c.ID != "" {
				pending[c.ID] = len(turn.Blocks) - 1
			}
		case "tool_result":
			if bi, hit := pending[c.ToolUseID]; hit {
				turn.Blocks[bi].ToolResult = nativeContentResultText(c)
				turn.Blocks[bi].IsError = c.IsError
			} else {
				// orphan result (no preceding tool_use in this turn) → keep it
				// visible as a standalone tool block rather than dropping content.
				turn.Blocks = append(turn.Blocks, Block{
					Type:       BlockTool,
					ToolUseID:  c.ToolUseID,
					ToolName:   c.Name,
					ToolResult: nativeContentResultText(c),
					IsError:    c.IsError,
				})
			}
		}
	}

	// usage footer (v1.1 inlined) → usage block + accumulate totals. Model (v1.1)
	// rides the assistant `message.model`; inline it onto the usage block (per-turn
	// SSOT) AND the transcript meta (session-level fallback) so the replay footer
	// shows the model just like the live stream did — live≡replay, no drift.
	u := nativeUsageMap(line.Message.Usage)
	if model := strings.TrimSpace(line.Message.Model); model != "" {
		if u == nil {
			u = map[string]interface{}{}
		}
		u["model"] = model
		tr.Meta["model"] = model // last non-empty wins → the session's model
	}
	if u != nil {
		turn.Blocks = append(turn.Blocks, Block{Type: BlockUsage, Usage: u})
		*totalIn += intField(u, "input_tokens")
		*totalOut += intField(u, "output_tokens")
		*totalCacheRead += intField(u, "cache_read_input_tokens")
		*sawUsage = true
	}

	if len(turn.Blocks) == 0 {
		return
	}
	tr.Turns = append(tr.Turns, turn)
}

// attachResultDuration inlines a `result` entry's wall-clock duration onto the
// immediately-preceding assistant turn's usage block, so the replay footer renders
// 总耗时 (the assistant `message.usage` carries only ttft/tokens; the duration lives
// on the turn-closing result line). No-op when there is no duration, no assistant
// turn, or the turn is not the last one appended (defensive against reordering).
func attachResultDuration(tr *Transcript, line *NativeEntry) {
	dur := line.DurationMs
	if dur <= 0 && line.Metrics != nil {
		dur = line.Metrics.DurationMs
	}
	if dur <= 0 || len(tr.Turns) == 0 {
		return
	}
	last := &tr.Turns[len(tr.Turns)-1]
	if last.Role != "assistant" {
		return
	}
	for i := range last.Blocks {
		if last.Blocks[i].Type == BlockUsage {
			if last.Blocks[i].Usage == nil {
				last.Blocks[i].Usage = map[string]interface{}{}
			}
			last.Blocks[i].Usage["duration_ms"] = dur
			return
		}
	}
	// A token-less turn has no usage block yet — add one carrying just the duration
	// so 总耗时 still renders (honest: tokens stay「—」, duration shows).
	last.Blocks = append(last.Blocks, Block{Type: BlockUsage, Usage: map[string]interface{}{"duration_ms": dur}})
}

// loadFromDB is the graceful fallback for sessions with no jsonl yet (pre-P1 or
// missing file). It projects the DB turns into text-only user/assistant bubbles
// — exactly what the DB read path surfaced before P2, so old sessions never
// regress below their prior (text-only) display.
func (s *DeepworkSource) loadFromDB(ctx context.Context, ref SessionRef) (*Transcript, error) {
	if s.provider == nil {
		return &Transcript{Source: KindDeepwork, Ref: ref.ID}, nil
	}
	sid, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil {
		return nil, err
	}
	turns, err := s.provider.LoadSessionTurns(ctx, sid)
	if err != nil {
		return nil, err
	}
	tr := &Transcript{Source: KindDeepwork, Ref: ref.ID}
	for _, t := range turns {
		at := t.At
		if u := strings.TrimSpace(t.UserInput); u != "" {
			tr.Turns = append(tr.Turns, Turn{
				Index: len(tr.Turns), Role: "user", At: tsPtr(at),
				Blocks: []Block{{Type: BlockUser, Text: u}},
			})
		}
		if a := strings.TrimSpace(t.AIOutput); a != "" {
			tr.Turns = append(tr.Turns, Turn{
				Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
				Blocks: []Block{{Type: BlockText, Text: a}},
			})
		}
	}
	return tr, nil
}
