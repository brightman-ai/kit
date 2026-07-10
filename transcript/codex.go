package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CodexSource reads the codex CLI's session storage (read-only). Real layout
// (verified on ~/.codex):
//
//	<Root>/sessions/YYYY/MM/DD/rollout-<ISO>-<uuid>.jsonl  — one per session
//	<Root>/session_index.jsonl                             — session index
//	<Root>/history.jsonl                                   — prompt history
//
// Each rollout-*.jsonl is one session transcript (the SSOT). Its first line is a
// session_meta payload carrying the session id (uuid), the originating cwd, and
// the wall clock — so ListSessions scopes by cwd == projectDir, mirroring how
// ClaudeSource shards by the encoded project dir. This source never writes or
// deletes a rollout file.
type CodexSource struct {
	Root string // defaults to ~/.codex

	// cwdIndexCache memoizes the cwd→count sweep (CountSessionsByDir) for a short
	// TTL, invalidated by a fingerprint of the sessions dir. A single
	// /api/workspaces request counts every workspace off one sweep; the cache also
	// absorbs bursts (panel refreshes) without re-walking 475 files. The cache is
	// per-CodexSource; buildAggregator currently makes a fresh source per request,
	// so a process-wide shared instance amplifies the win further (future).
	mu            sync.Mutex
	cachedCounts  map[string]int
	cacheFinger   string
	cacheExpireAt time.Time
}

// cwdIndexTTL bounds how long a cwd→count sweep is reused. The fingerprint
// (file count + newest mtime) already invalidates on real change; the TTL is a
// cheap safety net so a stale fingerprint can never serve forever.
const cwdIndexTTL = 5 * time.Second

// NewCodexSource roots at ~/.codex (override via DW_CODEX_HOME for tests).
func NewCodexSource() *CodexSource {
	root := strings.TrimSpace(os.Getenv("DW_CODEX_HOME"))
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".codex")
		}
	}
	return &CodexSource{Root: root}
}

func (s *CodexSource) Kind() string { return KindCodex }

// sessionsDir is the rollout root: <Root>/sessions.
func (s *CodexSource) sessionsDir() string {
	return filepath.Join(s.Root, "sessions")
}

// rolloutFiles walks <Root>/sessions/**/ collecting every rollout-*.jsonl path.
// A missing sessions dir is an honest empty list, never an error (an unparsed or
// absent runtime must not degrade the whole fleet).
func (s *CodexSource) rolloutFiles() ([]string, error) {
	root := s.sessionsDir()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable subtrees; keep walking
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// ListSessions scans every rollout-*.jsonl, keeps those whose session_meta.cwd
// matches projectDir (when projectDir is non-empty), and extracts a lightweight
// SessionMeta per file via a single streaming pass.
func (s *CodexSource) ListSessions(ctx context.Context, projectDir string) ([]SessionMeta, error) {
	files, err := s.rolloutFiles()
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(projectDir)

	out := make([]SessionMeta, 0, len(files))
	for _, path := range files {
		meta, cwd, ok := s.scanMeta(path)
		if !ok {
			continue
		}
		if want != "" && cwd != want {
			continue // scope to the project, mirroring claude's per-dir sharding
		}
		out = append(out, meta)
	}
	return out, nil
}

// CountSessionsByDir sweeps every rollout-*.jsonl once and returns cwd→count,
// reading ONLY the first line of each file (the session_meta payload carries the
// originating cwd) instead of full-parsing the transcript. This is the perf
// fast-path behind GET /api/workspaces (DirCounter): O(files) total with ~few-KB
// reads per file, replacing the old O(workspaces × files) full-parse.
//
// Result is memoized under a fingerprint (file count + newest mtime) + short TTL,
// so repeated calls within one request (and bursts) reuse the sweep.
func (s *CodexSource) CountSessionsByDir(ctx context.Context) (map[string]int, error) {
	finger, err := s.sessionsFingerprint()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.cachedCounts != nil && s.cacheFinger == finger && time.Now().Before(s.cacheExpireAt) {
		out := make(map[string]int, len(s.cachedCounts))
		for k, v := range s.cachedCounts {
			out[k] = v
		}
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	files, err := s.rolloutFiles()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, 16)
	for _, path := range files {
		if cwd := s.firstLineCwd(path); cwd != "" {
			counts[cwd]++
		}
	}

	s.mu.Lock()
	s.cachedCounts = counts
	s.cacheFinger = finger
	s.cacheExpireAt = time.Now().Add(cwdIndexTTL)
	s.mu.Unlock()

	out := make(map[string]int, len(counts))
	for k, v := range counts {
		out[k] = v
	}
	return out, nil
}

// firstLineCwd reads only the first line of a rollout jsonl (the session_meta)
// and returns its cwd. Returns "" when the file is unreadable or the first line
// is not a session_meta carrying a cwd (honest skip; never errors the sweep).
func (s *CodexSource) firstLineCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20) // session_meta is small; cap at 1 MB
	if !sc.Scan() {
		return ""
	}
	var line codexLine
	if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "session_meta" {
		return ""
	}
	m := line.asSessionMeta()
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.Cwd)
}

// sessionsFingerprint is a cheap change-detector for the rollout tree: file count
// plus the newest mtime across the tree. A new/removed/touched rollout flips it,
// invalidating the cwd-index cache without re-reading file contents. A missing
// tree is the stable empty fingerprint.
func (s *CodexSource) sessionsFingerprint() (string, error) {
	root := s.sessionsDir()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return "empty", nil
		}
		return "", err
	}
	var (
		count  int
		newest time.Time
	)
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		count++
		if info, ierr := d.Info(); ierr == nil {
			if mt := info.ModTime(); mt.After(newest) {
				newest = mt
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return strconv.Itoa(count) + ":" + newest.UTC().Format(time.RFC3339Nano), nil
}

// scanMeta does one streaming pass over a rollout jsonl: id + created + cwd from
// session_meta, title from the first real user message (fallback cwd basename),
// updated from the last timestamped line, turn_count from user messages. Returns
// (meta, cwd, ok); ok is false when the file has no session_meta id.
func (s *CodexSource) scanMeta(path string) (SessionMeta, string, bool) {
	// (path,size,mtime) 记忆化：未变文件零重解析（metacache.go, 2026-07-03 轮询风暴修复）。
	cached, st, hit := loadMetaCache(path)
	if hit {
		return cached.meta, cached.cwd, cached.ok
	}

	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, "", false
	}
	defer f.Close()

	var (
		id, cwd, firstUser string
		firstTS, lastTS    time.Time
		userTurns          int
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // rollout lines can be large
	for sc.Scan() {
		var line codexLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if ts := line.time(); !ts.IsZero() {
			if firstTS.IsZero() {
				firstTS = ts
			}
			lastTS = ts
		}
		switch line.Type {
		case "session_meta":
			if m := line.asSessionMeta(); m != nil {
				id = m.ID
				cwd = m.Cwd
				if ts := parseCodexTime(m.Timestamp); !ts.IsZero() {
					firstTS = ts
				}
			}
		case "response_item":
			if line.payloadType() != "message" {
				continue
			}
			m := line.asMessage()
			if m == nil || m.Role != "user" {
				continue
			}
			if txt := m.text(); txt != "" && !isCodexNoise(txt) {
				userTurns++
				if firstUser == "" {
					firstUser = txt
				}
			}
		}
	}

	if id == "" {
		id = idFromRolloutName(filepath.Base(path)) // fall back to filename uuid
	}
	if id == "" {
		storeMetaCache(path, st, SessionMeta{}, "", false) // 坏文件也缓存，避免每请求重扫
		return SessionMeta{}, "", false
	}

	meta := SessionMeta{ID: id, Source: KindCodex, SsotPath: path}
	meta.Title = firstNonEmpty(
		truncate(firstUser, 80),
		codexTitleFromCwd(cwd),
		"codex session "+shortID(id),
	)
	meta.CreatedAt = firstTS
	meta.UpdatedAt = lastTS
	if meta.UpdatedAt.IsZero() {
		if st, err := os.Stat(path); err == nil {
			meta.UpdatedAt = st.ModTime()
			if meta.CreatedAt.IsZero() {
				meta.CreatedAt = st.ModTime()
			}
		}
	}
	meta.TurnCount = userTurns
	storeMetaCache(path, st, meta, cwd, true)
	return meta, cwd, true
}

// LoadTranscript fully parses one rollout jsonl into ordered turns/blocks. The
// ref carries the session id (uuid); the file is resolved by walking the rollout
// tree and matching the id (the per-date path is not encoded in the ref).
func (s *CodexSource) LoadTranscript(ctx context.Context, ref SessionRef) (*Transcript, error) {
	path, err := s.resolvePath(ref.ID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return &Transcript{Source: KindCodex, Ref: ref.ID}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := &Transcript{Source: KindCodex, Ref: ref.ID, Meta: map[string]interface{}{}}
	// call_id → (turnIdx, blockIdx) so a later *_output line attaches its result.
	pending := map[string]toolLoc{}
	var cwd, firstUser string
	// usage accumulation (mirrors claude/deepwork): per-turn token_count events
	// surface a usage block in flow order; their last_token_usage deltas sum into
	// Meta so the footer shows session totals. sawUsage guards Meta emission so an
	// all-zero session stays an honest unknown (no fabricated zeros).
	var totalIn, totalOut, totalCacheRead int
	var sawUsage bool

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var line codexLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		at := line.time()
		switch line.Type {
		case "session_meta":
			if m := line.asSessionMeta(); m != nil {
				cwd = m.Cwd
			}
		case "response_item":
			switch line.payloadType() {
			case "message":
				m := line.asMessage()
				if m == nil {
					continue
				}
				switch m.Role {
				case "user":
					txt := m.text()
					if txt == "" || isCodexNoise(txt) {
						continue
					}
					if firstUser == "" {
						firstUser = txt
					}
					tr.Turns = append(tr.Turns, Turn{
						Index: len(tr.Turns), Role: "user", At: tsPtr(at),
						Blocks: []Block{{Type: BlockUser, Text: txt}},
					})
				case "assistant":
					if txt := m.text(); txt != "" {
						tr.Turns = append(tr.Turns, Turn{
							Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
							Blocks: []Block{{Type: BlockText, Text: txt}},
						})
					}
				}
			case "reasoning":
				if r := line.asReasoning(); r != nil {
					if txt := r.text(); txt != "" {
						tr.Turns = append(tr.Turns, Turn{
							Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
							Blocks: []Block{{Type: BlockThinking, Text: txt}},
						})
					}
				}
			case "function_call":
				if c := line.asFunctionCall(); c != nil {
					appendCodexToolCall(tr, pending, at, c.Name, c.CallID, codexArgsToInput(c.Arguments))
				}
			case "custom_tool_call":
				if c := line.asFunctionCall(); c != nil {
					var input map[string]interface{}
					if strings.TrimSpace(c.Input) != "" {
						input = map[string]interface{}{"input": c.Input}
						// apply_patch embeds its target path in the patch body
						// (*** Update/Add/Delete File: <path>). Normalize it into
						// tool_input["path"] here so the runtime-agnostic frontend
						// toolPath renders it without parsing codex patch syntax.
						if p := codexPatchPath(c.Input); p != "" {
							input["path"] = p
						}
					}
					appendCodexToolCall(tr, pending, at, c.Name, c.CallID, input)
				}
			case "function_call_output", "custom_tool_call_output":
				if o := line.asFunctionOutput(); o != nil {
					if loc, ok := pending[o.CallID]; ok {
						blk := &tr.Turns[loc.t].Blocks[loc.b]
						blk.ToolResult = o.Output
						delete(pending, o.CallID)
					}
				}
			}
		case "event_msg":
			// codex carries token usage out-of-band on event_msg/token_count
			// (per-turn last_token_usage delta). Surface it as a usage block in
			// flow order + sum into Meta — same shape as claude/deepwork so the
			// @ce UsageFooter is runtime-agnostic.
			if line.payloadType() != "token_count" {
				continue
			}
			tc := line.asTokenCount()
			if tc == nil || tc.Info == nil {
				continue // rate-limit-only line (info=null) → no usage payload
			}
			u := tc.Info.LastTokenUsage.usageMap()
			if u == nil {
				continue // all-zero turn → honest skip
			}
			tr.Turns = append(tr.Turns, Turn{
				Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
				Blocks: []Block{{Type: BlockUsage, Usage: u}},
			})
			totalIn += intField(u, "input_tokens")
			totalOut += intField(u, "output_tokens")
			totalCacheRead += intField(u, "cache_read_input_tokens")
			sawUsage = true
		}
	}

	if sawUsage {
		tr.Meta["input_tokens"] = totalIn
		tr.Meta["output_tokens"] = totalOut
		tr.Meta["cache_read_tokens"] = totalCacheRead
	}
	tr.Title = firstNonEmpty(
		truncate(firstUser, 80),
		codexTitleFromCwd(cwd),
		"codex session "+shortID(ref.ID),
	)
	return tr, nil
}

// appendCodexToolCall appends a tool_use block as its own assistant turn and
// registers call_id → block location so the matching output back-attaches.
func appendCodexToolCall(tr *Transcript, pending map[string]toolLoc, at time.Time, name, callID string, input map[string]interface{}) {
	turn := Turn{
		Index: len(tr.Turns), Role: "assistant", At: tsPtr(at),
		Blocks: []Block{{Type: BlockTool, ToolName: name, ToolUseID: callID, ToolInput: input}},
	}
	tr.Turns = append(tr.Turns, turn)
	pending[callID] = toolLoc{t: len(tr.Turns) - 1, b: 0}
}

// RolloutPathFor returns the absolute rollout-*.jsonl path for a codex session id
// by walking the sessions tree (the same suffix match the transcript loader uses).
// Empty when the id is empty, unknown, or the tree is unreadable — an unresolvable
// transcript (the caller treats "" as non-advancing), never an error to the caller.
func (s *CodexSource) RolloutPathFor(id string) string {
	path, err := s.resolvePath(id)
	if err != nil {
		return ""
	}
	return path
}

// resolvePath walks the rollout tree and returns the file whose name embeds id.
// The rollout filename is rollout-<ISO>-<uuid>.jsonl, so an id suffix match is
// exact and cheap.
func (s *CodexSource) resolvePath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	files, err := s.rolloutFiles()
	if err != nil {
		return "", err
	}
	suffix := id + ".jsonl"
	for _, p := range files {
		if strings.HasSuffix(filepath.Base(p), suffix) {
			return p, nil
		}
	}
	return "", nil // unknown id → honest empty transcript (handled by caller)
}

// idFromRolloutName extracts the trailing uuid from rollout-<ISO>-<uuid>.jsonl.
// The uuid is the last 5 dash-separated groups (8-4-4-4-12).
func idFromRolloutName(name string) string {
	name = strings.TrimSuffix(name, ".jsonl")
	parts := strings.Split(name, "-")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[len(parts)-5:], "-")
}

// codexTitleFromCwd derives a fallback title from the session's cwd basename.
func codexTitleFromCwd(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	return "codex @ " + filepath.Base(cwd)
}
