package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// deepwork_raw.go parses the Deepwork Native Transcript JSONL
// (deepwork.native_transcript.v1 / v1.1, written by pkg/worktranscript) into the
// unified Transcript{Turns[]→Blocks[]} model — the deepwork mirror of
// claude_raw.go. The transcript file is the content SSOT (CHG-015 路 A): the
// reader rebuilds user/assistant/thinking/tool/usage blocks straight from the
// file so deleting the DB does not lose the conversation (or the workArea panel,
// which folds from the same per-turn usage/tool blocks).
//
// The on-disk shape is the exported NativeEntry/NativeMessage/NativeContentBlock/
// NativeUsage/NativeMetrics schema (native_schema.go) — the SAME types
// pkg/worktranscript uses to write the file (imported there, not re-declared).
// One schema, two call sites: no more hand-synced read/write copies to drift.

// scanDeepworkMeta does a single streaming pass over a dw-<id>.jsonl to extract
// list metadata WITHOUT a full block parse: title (first real user line),
// created (first timestamp), updated (last timestamp), turn_count (number of
// user lines). This is the directory-as-index path (CHG-015 P3): ListSessions
// scans the transcript dir and builds one SessionMeta per file, exactly the way
// ClaudeSource.scanMeta does — so deleting the DB still yields a full session
// list straight from the files (目录即索引). ok is false on an unreadable /
// content-less file so the caller drops the row.
func scanDeepworkMeta(path, id string) (meta SessionMeta, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, false
	}
	defer f.Close()

	meta = SessionMeta{ID: id, Source: KindDeepwork, SsotPath: path}
	var firstUser string
	var firstTS, lastTS time.Time
	userTurns := 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20) // tolerate very long lines
	for sc.Scan() {
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var line NativeEntry
		if err := json.Unmarshal(raw, &line); err != nil {
			continue // ignore half-written / corrupt lines (complete-line 守卫)
		}
		if ts := nativeEntryTime(&line); !ts.IsZero() {
			if firstTS.IsZero() {
				firstTS = ts
			}
			lastTS = ts
		}
		if line.Type == "user" && line.Message != nil {
			if txt := firstText(line.Message.Content); txt != "" {
				userTurns++
				if firstUser == "" {
					firstUser = txt
				}
			}
		}
	}

	meta.Title = firstNonEmpty(truncate(firstUser, 80), "deepwork session "+shortID(id))
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
	return meta, userTurns > 0 || !firstTS.IsZero()
}

// firstText returns the first non-empty text block of a content slice (used by
// the cheap meta scan to derive a session title without a full parse).
func firstText(blocks []NativeContentBlock) string {
	for i := range blocks {
		if blocks[i].Type == "text" && strings.TrimSpace(blocks[i].Text) != "" {
			return strings.TrimSpace(blocks[i].Text)
		}
	}
	return ""
}

// nativeEntryTime parses the entry's RFC3339(Nano) timestamp, tolerating either
// precision (the writer always emits Nano; older/foreign lines may not).
func nativeEntryTime(l *NativeEntry) time.Time {
	if l.Timestamp == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
		return t
	}
	return time.Time{}
}

// nativeContentInputMap decodes a tool_use input (JSON object) into a generic
// map, mirroring claude's structured ToolInput. A non-object input is wrapped
// as {_raw:...}.
func nativeContentInputMap(b *NativeContentBlock) map[string]interface{} {
	if len(b.Input) == 0 {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal(b.Input, &m) == nil {
		return m
	}
	var s string
	if json.Unmarshal(b.Input, &s) == nil && strings.TrimSpace(s) != "" {
		return map[string]interface{}{"_raw": s}
	}
	return nil
}

// nativeContentResultText flattens a tool_result's content (the writer encodes
// it as a JSON array of {type:text,text:...} blocks; tolerate a bare string too).
func nativeContentResultText(b *NativeContentBlock) string {
	if len(b.Content) == 0 {
		return ""
	}
	switch b.Content[0] {
	case '"':
		var s string
		if json.Unmarshal(b.Content, &s) == nil {
			return s
		}
	case '[':
		var parts []NativeContentBlock
		if json.Unmarshal(b.Content, &parts) == nil {
			var sb strings.Builder
			for _, p := range parts {
				if p.Text != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(p.Text)
				}
			}
			return sb.String()
		}
	}
	return ""
}

// nativeUsageMap projects the inlined v1.1 usage onto the generic map the @ce
// usage block + workArea metrics consume (same keys claude inlines). nil → nil
// so the caller emits no usage block (honest unknown for legacy v1 / non-reporting paths).
func nativeUsageMap(u *NativeUsage) map[string]interface{} {
	if u == nil {
		return nil
	}
	m := map[string]interface{}{}
	if u.InputTokens != nil {
		m["input_tokens"] = *u.InputTokens
	}
	if u.OutputTokens != nil {
		m["output_tokens"] = *u.OutputTokens
	}
	if u.ThinkingTokens != nil {
		m["thinking_tokens"] = *u.ThinkingTokens
	}
	if u.CacheReadTokens != nil {
		m["cache_read_input_tokens"] = *u.CacheReadTokens
	}
	if u.CacheCreateTokens != nil {
		m["cache_creation_input_tokens"] = *u.CacheCreateTokens
	}
	if u.TTFTMs != nil {
		m["ttft_ms"] = *u.TTFTMs
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
