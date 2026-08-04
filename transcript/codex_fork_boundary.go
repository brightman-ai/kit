package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── where a forked codex rollout's OWN work begins ────────────────────────────────────────────
//
// Codex materializes a fork by copying the parent's rollout into the child file after the child's
// session_meta — the parent's session_meta included. The model needs that history as context; the
// accounting does not. Those rows are the PARENT's requests, already recorded in the parent's own
// rollout, so counting them charges the same tokens twice.
//
// The scale is not marginal. Over one 7-day window on a live machine, 69 subagent rollouts yield
// 56,873 requests / 8.44B tokens when read from byte 0, and 6,975 requests / 0.89B tokens when
// read from the child's own start. Nearly 8 billion tokens of the first number are a second copy
// of work already counted elsewhere.
//
// Codex writes the copy in TWO shapes, and a rule for one is blind to the other:
//
//	A. the parent's session_meta is copied in too. The child's own `task_started` then marks the
//	   resumption — identified by a session_meta whose id is not the child's (proving the
//	   inherited block began) plus a turn id whose uuid-v7 timestamp falls within 10 minutes
//	   AFTER the child's session id (separating the child's first real turn from the older
//	   copied ones).
//
//	B. only the parent's EVENTS are copied, with no session_meta among them. Shape A's detector
//	   never fires, so the whole block reads as the child's own work. Here the boundary is the
//	   child's first turn declaration (turn_context / thread_settings_applied): codex cannot run
//	   a turn it has not configured, so usage recorded before the first declaration was
//	   configured by somebody else.
//
// Both are needed. On this machine the four rollouts carrying every unattributed request were all
// shape B — 168 of them proven to be the parent's by finding the identical event run inside the
// parent rollout — and shape A's rule returned 0 for every one.
//
// Shape B is guarded twice against eating real work: it applies only to a rollout that DECLARES a
// parent (forked_from_id / subagent source), and across 601 local rollouts, pre-declaration usage
// events appear in 26 files, all forked, and in zero ordinary ones.
//
// A file with neither shape returns 0 — an ordinary rollout is read whole.

// CodexOwnStartOffset returns the byte offset where a forked/subagent rollout's own events begin.
//
// It returns an error rather than 0 when a fork's boundary cannot be found: silently reading such
// a file from the top would double-count thousands of requests, and a loud failure that skips one
// file is much cheaper than a quiet one that inflates the bill.
func CodexOwnStartOffset(path, sessionID string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sessionMillis, validSessionID := uuidV7Millis(sessionID)
	reader := bufio.NewReader(f)
	var offset int64
	inherited := false            // shape A: a foreign session_meta was copied in
	forked := false               // this rollout declares a parent
	firstDeclaration := int64(-1) // shape B boundary
	usageBeforeDeclaration := false
	for {
		lineStart := offset
		line, readErr := reader.ReadBytes('\n')
		offset += int64(len(line))
		if len(line) > 0 {
			var row struct {
				Type    string `json:"type"`
				Payload struct {
					Type           string          `json:"type"`
					ID             string          `json:"id"`
					TurnID         string          `json:"turn_id"`
					ForkedFromID   string          `json:"forked_from_id"`
					ParentThreadID string          `json:"parent_thread_id"`
					Source         json.RawMessage `json:"source"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &row) == nil {
				switch {
				case row.Type == "session_meta" && row.Payload.ID != "" && row.Payload.ID != sessionID:
					inherited = true
				case row.Type == "session_meta":
					forked = forked || row.Payload.ForkedFromID != "" || row.Payload.ParentThreadID != "" ||
						hasSubagentSource(row.Payload.Source)
				}
				if inherited && row.Type == "event_msg" && row.Payload.Type == "task_started" && validSessionID {
					if taskMillis, ok := uuidV7Millis(row.Payload.TurnID); ok &&
						taskMillis >= sessionMillis && taskMillis-sessionMillis <= int64((10*time.Minute)/time.Millisecond) {
						return lineStart, nil // shape A
					}
				}
				if firstDeclaration < 0 && (row.Type == "turn_context" || row.Payload.Type == "thread_settings_applied") {
					firstDeclaration = lineStart
				}
				if firstDeclaration < 0 && row.Payload.Type == "token_count" {
					usageBeforeDeclaration = true
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return 0, readErr
		}
	}
	if inherited {
		return 0, fmt.Errorf("codex child boundary not found: %s", filepath.Base(path))
	}
	if forked && usageBeforeDeclaration && firstDeclaration > 0 {
		return firstDeclaration, nil // shape B
	}
	return 0, nil
}

// hasSubagentSource reports whether a session_meta's `source` names a subagent spawn — the shape
// that carries the parent id nested a level deeper than a top-level read would find.
func hasSubagentSource(source json.RawMessage) bool {
	if len(source) == 0 {
		return false
	}
	var s struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	return json.Unmarshal(source, &s) == nil && len(s.Subagent) > 0
}

// codexRolloutSessionID reads the id a rollout claims for itself — its FIRST session_meta. Any
// later session_meta belongs to copied-in history.
func codexRolloutSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.Type == "session_meta" {
			return row.Payload.ID
		}
	}
	return ""
}

// uuidV7Millis extracts the millisecond timestamp a uuid-v7 carries in its leading 48 bits.
func uuidV7Millis(value string) (int64, bool) {
	hex := strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(hex) < 12 {
		return 0, false
	}
	millis, err := strconv.ParseInt(hex[:12], 16, 64)
	return millis, err == nil
}
