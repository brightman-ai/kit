package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CoverageState is the evidence quality of one request-fact dimension.
// Missing is never equivalent to a measured zero.
type CoverageState string

const (
	CoverageComplete CoverageState = "complete"
	CoveragePartial  CoverageState = "partial"
	CoverageMissing  CoverageState = "missing"
)

// RequestCoverage makes every downstream number explain its provenance. Provider
// adapters set these fields; analytics and UI only propagate them.
type RequestCoverage struct {
	Identity CoverageState `json:"identity"`
	Model    CoverageState `json:"model"`
	Tokens   CoverageState `json:"tokens"`
	Effort   CoverageState `json:"effort"`
	Tier     CoverageState `json:"tier"`
	Billing  CoverageState `json:"billing"`
	Timing   CoverageState `json:"timing"`
	CacheTTL CoverageState `json:"cache_ttl"`
}

// ModelRequestUsage is the provider-neutral, request-grain economic fact.
// Token classes are mutually exclusive:
//   - InputTokens = fresh/uncached input
//   - CachedInputTokens = cache reads
//   - CacheWrite* = cache writes split by TTL
//
// OutputTokens is inclusive; ReasoningOutputTokens is a breakdown only.
//
// A request fact is immutable. Pricing and reporting derive projections from it;
// neither may rewrite the transcript truth.
type ModelRequestUsage struct {
	ID              string `json:"id"`
	Runtime         string `json:"runtime"`
	Provider        string `json:"provider,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	WorkItemID      string `json:"work_item_id,omitempty"`
	AgentInstanceID string `json:"agent_instance_id,omitempty"`

	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	ServiceTier  string `json:"service_tier,omitempty"`
	BillingMode  string `json:"billing_mode,omitempty"`
	AuthMode     string `json:"auth_mode,omitempty"`
	InferenceGeo string `json:"inference_geo,omitempty"`
	Speed        string `json:"speed,omitempty"`

	At              time.Time  `json:"at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FirstObservedAt *time.Time `json:"first_observed_at,omitempty"`
	FirstTokenAt    *time.Time `json:"first_token_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`

	InputTokens             int64 `json:"input_tokens"`
	CachedInputTokens       int64 `json:"cached_input_tokens"`
	CacheWrite5mTokens      int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens      int64 `json:"cache_write_1h_tokens"`
	CacheWriteUnknownTokens int64 `json:"cache_write_unknown_tokens"`
	OutputTokens            int64 `json:"output_tokens"`
	ReasoningOutputTokens   int64 `json:"reasoning_output_tokens"`
	RawInputTokens          int64 `json:"raw_input_tokens,omitempty"`

	SourceRef    string          `json:"source_ref"`
	SourceOffset int64           `json:"source_offset,omitempty"`
	Coverage     RequestCoverage `json:"coverage"`
	Diagnostics  []string        `json:"diagnostics,omitempty"`
	// TimingInvalidated is an append-time tombstone for a previously emitted
	// interval. It lets materialized views revoke timing when a later split row
	// proves the same message crossed an interrupt; false is not equivalent to
	// "never observed" and merge code must honor true monotonically.
	TimingInvalidated bool `json:"timing_invalidated,omitempty"`
}

func (u ModelRequestUsage) PhysicalTotalTokens() int64 {
	return u.InputTokens + u.CachedInputTokens + u.CacheWrite5mTokens +
		u.CacheWrite1hTokens + u.CacheWriteUnknownTokens + u.OutputTokens
}

func (u ModelRequestUsage) ContextTokens() int64 {
	return u.InputTokens + u.CachedInputTokens + u.CacheWrite5mTokens +
		u.CacheWrite1hTokens + u.CacheWriteUnknownTokens
}

// ScanCodexRequestUsage projects every event_msg/token_count.last_token_usage
// into one request fact. Source offset is a stable append-only identity when the
// wire does not expose a provider request id.
func ScanCodexRequestUsage(path string) ([]ModelRequestUsage, error) {
	facts, _, err := ScanCodexRequestUsageIncremental(path, CodexRequestCursor{})
	return facts, err
}

// CodexRequestCursor is the minimal parser state needed to resume an
// append-only rollout. Offset always points at the first unconsumed byte; a
// partial JSONL tail is deliberately left unconsumed until its newline arrives.
type CodexRequestCursor struct {
	Offset                 int64     `json:"offset"`
	MalformedLines         int64     `json:"malformed_lines"`
	SessionID              string    `json:"session_id,omitempty"`
	Model                  string    `json:"model,omitempty"`
	Effort                 string    `json:"effort,omitempty"`
	ServiceTier            string    `json:"service_tier,omitempty"`
	Provider               string    `json:"provider,omitempty"`
	Speed                  string    `json:"speed,omitempty"`
	BillingMode            string    `json:"billing_mode,omitempty"`
	LastCausalAt           time.Time `json:"last_causal_at,omitempty"`
	NextCausalAt           time.Time `json:"next_causal_at,omitempty"`
	PendingFirstObservedAt time.Time `json:"pending_first_observed_at,omitempty"`
	PendingResponseEndedAt time.Time `json:"pending_response_ended_at,omitempty"`
}

// ScanCodexRequestUsageIncremental returns only request facts appended after
// cursor.Offset and the parser state required for the next append. Truncation
// resets the cursor, making log rotation safe without inventing continuity.
func ScanCodexRequestUsageIncremental(path string, cursor CodexRequestCursor) ([]ModelRequestUsage, CodexRequestCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cursor, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, cursor, err
	}
	if cursor.Offset < 0 || cursor.Offset > info.Size() {
		cursor = CodexRequestCursor{}
	}
	if _, err := f.Seek(cursor.Offset, io.SeekStart); err != nil {
		return nil, cursor, err
	}

	sessionID, model, effort, tier, provider := cursor.SessionID, cursor.Model, cursor.Effort, cursor.ServiceTier, cursor.Provider
	speed, billingMode := cursor.Speed, cursor.BillingMode
	var out []ModelRequestUsage
	br := bufio.NewReaderSize(f, 256*1024)
	offset := cursor.Offset
	for {
		lineBytes, readErr := br.ReadBytes('\n')
		lineOffset := offset
		// A writer can be between write(2) calls while we refresh. Do not parse or
		// advance past that tail; the next refresh will see the complete record.
		if readErr == io.EOF && len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] != '\n' {
			break
		}
		offset = lineOffset + int64(len(lineBytes))
		trimmed := bytes.TrimSpace(lineBytes)
		if len(trimmed) > 0 {
			var line codexLine
			if json.Unmarshal(trimmed, &line) == nil {
				switch line.Type {
				case "session_meta":
					if meta := line.asSessionMeta(); meta != nil && meta.ID != "" {
						sessionID = meta.ID
					}
				case "turn_context":
					if tc := line.asTurnContext(); tc != nil {
						if tc.Model != "" {
							model = tc.Model
						}
						if value := firstNonEmpty(tc.ReasoningEffort, tc.Effort); value != "" {
							effort = value
						}
						if tc.ServiceTier != "" {
							tier, speed, billingMode = codexTierAndSpeed(tc.ServiceTier)
						}
					}
				case "response_item":
					at := line.time()
					switch line.payloadType() {
					case "message":
						if msg := line.asMessage(); msg != nil {
							text := msg.text()
							switch msg.Role {
							case "user":
								if strings.HasPrefix(strings.TrimSpace(text), "<turn_aborted>") {
									cursor.LastCausalAt = time.Time{}
									cursor.NextCausalAt = time.Time{}
									cursor.PendingFirstObservedAt = time.Time{}
									cursor.PendingResponseEndedAt = time.Time{}
								} else if !at.IsZero() && !isCodexNoise(text) {
									cursor.LastCausalAt = at
									cursor.NextCausalAt = time.Time{}
									cursor.PendingFirstObservedAt = time.Time{}
									cursor.PendingResponseEndedAt = time.Time{}
								}
							case "assistant":
								observeCodexAssistantEvent(&cursor, at)
							}
						}
					case "function_call_output", "custom_tool_call_output":
						if !at.IsZero() {
							// Codex writes token_count *after* the tool result, but that
							// usage belongs to the assistant response which requested the
							// tool. Stage this as the next request's causal input and only
							// promote it after the current token_count is emitted.
							cursor.NextCausalAt = at
						}
					case "reasoning", "function_call", "custom_tool_call":
						observeCodexAssistantEvent(&cursor, at)
					}
				case "event_msg":
					switch line.payloadType() {
					case "thread_settings_applied":
						var p struct {
							ThreadSettings struct {
								Model           string `json:"model"`
								ModelProviderID string `json:"model_provider_id"`
								ServiceTier     string `json:"service_tier"`
								ReasoningEffort string `json:"reasoning_effort"`
							} `json:"thread_settings"`
						}
						if json.Unmarshal(line.Payload, &p) == nil {
							if p.ThreadSettings.Model != "" {
								model = p.ThreadSettings.Model
							}
							if p.ThreadSettings.ModelProviderID != "" {
								provider = p.ThreadSettings.ModelProviderID
							}
							if p.ThreadSettings.ServiceTier != "" {
								tier, speed, billingMode = codexTierAndSpeed(p.ThreadSettings.ServiceTier)
							}
							if p.ThreadSettings.ReasoningEffort != "" {
								effort = p.ThreadSettings.ReasoningEffort
							}
						}
					case "token_count":
						tc := line.asTokenCount()
						if tc == nil || tc.Info == nil || tc.Info.LastTokenUsage == nil {
							break
						}
						raw := tc.Info.LastTokenUsage
						if raw.InputTokens == 0 && raw.OutputTokens == 0 && raw.CachedInputTokens == 0 {
							break
						}
						fresh := raw.InputTokens - raw.CachedInputTokens
						diag := []string(nil)
						if fresh < 0 {
							fresh = 0
							diag = append(diag, "cached_input_exceeds_inclusive_input")
						}
						at := line.time()
						ended := cursor.PendingResponseEndedAt
						if ended.IsZero() {
							ended = at
						}
						idSession := sessionID
						if idSession == "" {
							idSession = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
						}
						fact := ModelRequestUsage{
							ID: fmt.Sprintf("codex:%s:%d", idSession, lineOffset), Runtime: "codex",
							Provider: provider, SessionID: sessionID, Model: model, Effort: effort,
							ServiceTier: tier, Speed: speed, BillingMode: billingMode, At: at,
							StartedAt: timePtrUnlessZero(cursor.LastCausalAt), FirstObservedAt: timePtrUnlessZero(cursor.PendingFirstObservedAt), EndedAt: timePtrUnlessZero(ended),
							InputTokens: int64(fresh), CachedInputTokens: int64(raw.CachedInputTokens),
							OutputTokens: int64(raw.OutputTokens), ReasoningOutputTokens: int64(raw.ReasoningOutputTokens),
							RawInputTokens: int64(raw.InputTokens), SourceRef: filepath.Base(path), SourceOffset: lineOffset,
							Coverage: RequestCoverage{Identity: presentCoverage(sessionID), Model: presentCoverage(model), Tokens: CoverageComplete,
								Effort: presentCoverage(effort), Tier: presentCoverage(tier), Billing: presentCoverage(billingMode),
								Timing: CoveragePartial, CacheTTL: CoverageComplete},
							Diagnostics: diag,
						}
						if provider == "" {
							fact.Provider = "openai"
						}
						if fact.StartedAt == nil {
							fact.Diagnostics = appendUnique(fact.Diagnostics, "response_start_missing")
						}
						out = append(out, fact)
						cursor.LastCausalAt = cursor.NextCausalAt
						cursor.NextCausalAt = time.Time{}
						cursor.PendingFirstObservedAt = time.Time{}
						cursor.PendingResponseEndedAt = time.Time{}
					case "turn_aborted":
						cursor.LastCausalAt = time.Time{}
						cursor.NextCausalAt = time.Time{}
						cursor.PendingFirstObservedAt = time.Time{}
						cursor.PendingResponseEndedAt = time.Time{}
					}
				}
			} else {
				cursor.MalformedLines++
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			cursor.Offset = offset
			cursor.SessionID, cursor.Model, cursor.Effort, cursor.ServiceTier, cursor.Provider = sessionID, model, effort, tier, provider
			cursor.Speed, cursor.BillingMode = speed, billingMode
			return out, cursor, readErr
		}
	}
	cursor.Offset = offset
	cursor.SessionID, cursor.Model, cursor.Effort, cursor.ServiceTier, cursor.Provider = sessionID, model, effort, tier, provider
	cursor.Speed, cursor.BillingMode = speed, billingMode
	return out, cursor, nil
}

func observeCodexAssistantEvent(cursor *CodexRequestCursor, at time.Time) {
	if cursor == nil || cursor.LastCausalAt.IsZero() || at.IsZero() {
		return
	}
	if cursor.PendingFirstObservedAt.IsZero() {
		cursor.PendingFirstObservedAt = at
	}
	cursor.PendingResponseEndedAt = at
}

// Codex Fast is a subscription speed mode, not OpenAI API priority pricing.
// The CLI encodes it as service_tier=fast. Preserve both dimensions explicitly:
// standard API-equivalent unit prices plus the official credit multiplier.
func codexTierAndSpeed(raw string) (tier, speed, billingMode string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fast":
		return "default", "fast", "subscription"
	case "default", "standard":
		return raw, "standard", ""
	case "priority":
		return "priority", "", ""
	default:
		return raw, "", ""
	}
}

type claudeRequestUsage struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
	ServiceTier              string `json:"service_tier"`
	InferenceGeo             string `json:"inference_geo"`
	Speed                    string `json:"speed"`
	CacheCreation            struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// ScanClaudeRequestUsage deduplicates split/repeated assistant rows by message.id.
// The first observation establishes ordering; later duplicates can only improve
// maxima and end time, never multiply tokens.
func ScanClaudeRequestUsage(path string) ([]ModelRequestUsage, error) {
	facts, _, err := ScanClaudeRequestUsageIncremental(path, ClaudeRequestCursor{})
	return facts, err
}

// ClaudeRequestCursor is intentionally smaller than Codex's: Claude assistant
// rows carry their own model and usage dimensions.
type ClaudeRequestCursor struct {
	Offset               int64     `json:"offset"`
	MalformedLines       int64     `json:"malformed_lines"`
	LastCausalAt         time.Time `json:"last_causal_at,omitempty"`
	ActiveMessageID      string    `json:"active_message_id,omitempty"`
	ActiveStartedAt      time.Time `json:"active_started_at,omitempty"`
	InvalidatedMessageID string    `json:"invalidated_message_id,omitempty"`
}

// ScanClaudeRequestUsageIncremental emits a deduplicated batch for the appended
// byte range. Callers merge facts by provider identity across batches.
func ScanClaudeRequestUsageIncremental(path string, cursor ClaudeRequestCursor) ([]ModelRequestUsage, ClaudeRequestCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cursor, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, cursor, err
	}
	if cursor.Offset < 0 || cursor.Offset > info.Size() {
		cursor = ClaudeRequestCursor{}
	}
	if _, err := f.Seek(cursor.Offset, io.SeekStart); err != nil {
		return nil, cursor, err
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	byID := make(map[string]*ModelRequestUsage)
	order := make([]string, 0, 64)
	br := bufio.NewReaderSize(f, 256*1024)
	offset := cursor.Offset
	for {
		lineBytes, readErr := br.ReadBytes('\n')
		lineOffset := offset
		if readErr == io.EOF && len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] != '\n' {
			break
		}
		offset = lineOffset + int64(len(lineBytes))
		trimmed := bytes.TrimSpace(lineBytes)
		if len(trimmed) > 0 && (bytes.Contains(trimmed, []byte(`"assistant"`)) || bytes.Contains(trimmed, []byte(`"user"`))) {
			var line rawLine
			if unmarshalErr := json.Unmarshal(trimmed, &line); unmarshalErr != nil {
				cursor.MalformedLines++
			} else if line.Type == "user" {
				at := line.time()
				text := line.userText()
				if isInterruptMarker(text) {
					// An abort closes the causal chain. A later model response needs a
					// new human input/tool result before it can have an observed start.
					cursor.LastCausalAt = time.Time{}
					cursor.InvalidatedMessageID = cursor.ActiveMessageID
					cursor.ActiveMessageID = ""
					cursor.ActiveStartedAt = time.Time{}
				} else {
					hasToolResult := false
					for _, part := range line.contentParts() {
						if part.Type == "tool_result" {
							hasToolResult = true
							break
						}
					}
					if !at.IsZero() && (hasToolResult || (!line.IsMeta && text != "")) {
						// Do not reset ActiveMessageID: Claude can emit split rows for the
						// same message around a tool_result. The next *new* message uses
						// this boundary; a duplicate keeps its original start.
						cursor.LastCausalAt = at
					}
				}
			} else if line.Type == "assistant" {
				msg := line.msg()
				if msg != nil && len(msg.Usage) > 0 {
					var u claudeRequestUsage
					if json.Unmarshal(msg.Usage, &u) == nil {
						idPart := msg.ID
						identity := CoverageComplete
						if idPart == "" {
							idPart = fmt.Sprintf("offset-%d", lineOffset)
							identity = CoveragePartial
						}
						id := "claude:" + sessionID + ":" + idPart
						at := line.time()
						startedAt := cursor.LastCausalAt
						if msg.ID != "" && msg.ID == cursor.ActiveMessageID {
							startedAt = cursor.ActiveStartedAt
						} else if msg.ID != "" {
							cursor.ActiveMessageID = msg.ID
							cursor.ActiveStartedAt = startedAt
							// One causal input can start at most one new provider response.
							// Split rows reuse ActiveStartedAt and do not consume again.
							cursor.LastCausalAt = time.Time{}
						}
						fact := byID[id]
						if fact == nil {
							fact = &ModelRequestUsage{
								ID: id, Runtime: "claude", Provider: "anthropic", SessionID: sessionID,
								Model: msg.Model, At: at, StartedAt: timePtrUnlessZero(startedAt), FirstObservedAt: timePtrUnlessZero(at), EndedAt: timePtrUnlessZero(at),
								SourceRef: filepath.Base(path), SourceOffset: lineOffset,
								Coverage: RequestCoverage{
									Identity: identity, Model: presentCoverage(msg.Model), Tokens: CoverageComplete,
									Effort: CoverageMissing, Tier: presentCoverage(u.ServiceTier), Billing: CoverageMissing,
									Timing: CoveragePartial, CacheTTL: CoverageComplete,
								},
							}
							if fact.StartedAt == nil {
								fact.Diagnostics = appendUnique(fact.Diagnostics, "response_start_missing")
							} else {
								fact.Diagnostics = appendUnique(fact.Diagnostics, "response_start_from_preceding_causal_event")
							}
							byID[id] = fact
							order = append(order, id)
						}
						fact.Model = firstNonEmpty(fact.Model, msg.Model)
						fact.ServiceTier = firstNonEmpty(fact.ServiceTier, u.ServiceTier)
						fact.InferenceGeo = firstNonEmpty(fact.InferenceGeo, u.InferenceGeo)
						fact.Speed = firstNonEmpty(fact.Speed, u.Speed)
						fact.InputTokens = maxInt64(fact.InputTokens, u.InputTokens)
						fact.CachedInputTokens = maxInt64(fact.CachedInputTokens, u.CacheReadInputTokens)
						fact.OutputTokens = maxInt64(fact.OutputTokens, u.OutputTokens)
						fact.CacheWrite5mTokens = maxInt64(fact.CacheWrite5mTokens, u.CacheCreation.Ephemeral5mInputTokens)
						fact.CacheWrite1hTokens = maxInt64(fact.CacheWrite1hTokens, u.CacheCreation.Ephemeral1hInputTokens)
						split := fact.CacheWrite5mTokens + fact.CacheWrite1hTokens
						if u.CacheCreationInputTokens > split {
							fact.CacheWriteUnknownTokens = maxInt64(fact.CacheWriteUnknownTokens, u.CacheCreationInputTokens-split)
							fact.Coverage.CacheTTL = CoveragePartial
							fact.Diagnostics = appendUnique(fact.Diagnostics, "cache_write_ttl_partial")
						}
						if !at.IsZero() && (fact.EndedAt == nil || at.After(*fact.EndedAt)) {
							fact.EndedAt = &at
						}
						if msg.ID != "" && msg.ID == cursor.InvalidatedMessageID {
							fact.StartedAt = nil
							fact.FirstTokenAt = nil
							fact.TimingInvalidated = true
							fact.Coverage.Timing = CoverageMissing
							fact.Diagnostics = appendUnique(fact.Diagnostics, "response_interval_crossed_interrupt")
						}
						if fact.StartedAt != nil && fact.EndedAt != nil && !fact.EndedAt.After(*fact.StartedAt) {
							fact.Diagnostics = appendUnique(fact.Diagnostics, "response_interval_invalid")
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			cursor.Offset = offset
			return nil, cursor, readErr
		}
	}
	out := make([]ModelRequestUsage, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].SourceOffset < out[j].SourceOffset
		}
		if out[i].At.IsZero() {
			return false
		}
		if out[j].At.IsZero() {
			return true
		}
		return out[i].At.Before(out[j].At)
	})
	cursor.Offset = offset
	return out, cursor, nil
}

func presentCoverage(v string) CoverageState {
	if strings.TrimSpace(v) == "" {
		return CoverageMissing
	}
	return CoverageComplete
}
func timePtrUnlessZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
