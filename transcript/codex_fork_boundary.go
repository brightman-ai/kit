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

// CodexOwnStart returns a cursor positioned where a forked rollout's own work begins, carrying
// the CONFIGURATION it inherited.
//
// The distinction is the point: a fork inherits its parent's settings and does not inherit its
// parent's spend. Skipping the copied block wholesale discards both, and the settings are the
// half we need — codex records service_tier only in `thread_settings_applied`, which a resumed or
// forked thread never re-emits, so for those rollouts the copied block is the ONLY place the
// billing tier appears. Dropping it left every request in the file unpriced with no way to tell
// why.
//
// Only fields that describe how the thread is configured are carried: model, tier, effort,
// provider. Nothing about what the parent spent crosses the boundary.
func CodexOwnStart(path, sessionID string) (CodexRequestCursor, error) {
	offset, inherited, err := codexOwnStartOffset(path, sessionID)
	if err != nil {
		return CodexRequestCursor{}, err
	}
	cursor := CodexRequestCursor{Offset: offset}
	if offset > 0 {
		// Past the boundary the child's own session_meta is behind us, so the identity it would
		// have supplied is seeded here along with the inherited configuration.
		cursor.SessionID = sessionID
		cursor.Provider = firstNonEmptyString(inherited.provider, "openai")
		cursor.Model = inherited.model
		cursor.ServiceTier = inherited.tier
		cursor.Speed = inherited.speed
		cursor.BillingMode = inherited.billing
		cursor.Effort = inherited.effort
	}
	return cursor, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// inheritedConfig is the thread configuration observed inside the copied parent block.
type inheritedConfig struct{ model, tier, speed, billing, effort, provider string }

// CodexOwnStartOffset returns just the byte offset. Prefer CodexOwnStart, which also recovers the
// configuration the fork inherited.
func CodexOwnStartOffset(path, sessionID string) (int64, error) {
	offset, _, err := codexOwnStartOffset(path, sessionID)
	return offset, err
}

func codexOwnStartOffset(path, sessionID string) (int64, inheritedConfig, error) {
	var cfg inheritedConfig
	f, err := os.Open(path)
	if err != nil {
		return 0, cfg, err
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
					ModelProvider  string          `json:"model_provider_id"`
					Model          string          `json:"model"`
					Settings       struct {
						Model           string `json:"model"`
						ModelProviderID string `json:"model_provider_id"`
						ServiceTier     string `json:"service_tier"`
						ReasoningEffort string `json:"reasoning_effort"`
					} `json:"thread_settings"`
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
						return lineStart, cfg, nil // shape A
					}
				}
				if firstDeclaration < 0 && (row.Type == "turn_context" || row.Payload.Type == "thread_settings_applied") {
					firstDeclaration = lineStart
				}
				if firstDeclaration < 0 && row.Payload.Type == "token_count" {
					usageBeforeDeclaration = true
				}
				// Configuration seen anywhere in the copied block is INHERITED, so it is kept even
				// though the usage beside it is discarded. This is the only place a resumed or
				// forked thread's service_tier ever appears.
				if row.Payload.Type == "thread_settings_applied" {
					st := row.Payload.Settings
					cfg.model = firstNonEmptyString(st.Model, cfg.model)
					cfg.provider = firstNonEmptyString(st.ModelProviderID, cfg.provider)
					cfg.effort = firstNonEmptyString(st.ReasoningEffort, cfg.effort)
					if st.ServiceTier != "" {
						cfg.tier, cfg.speed, cfg.billing = codexTierAndSpeed(st.ServiceTier)
					}
				}
				if row.Type == "turn_context" && row.Payload.Model != "" {
					cfg.model = firstNonEmptyString(cfg.model, row.Payload.Model)
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return 0, cfg, readErr
		}
	}
	if inherited {
		return 0, cfg, fmt.Errorf("codex child boundary not found: %s", filepath.Base(path))
	}
	if forked && usageBeforeDeclaration && firstDeclaration > 0 {
		return firstDeclaration, cfg, nil // shape B
	}
	return 0, cfg, nil
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
