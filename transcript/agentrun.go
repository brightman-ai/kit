package transcript

import "time"

// AgentRun is the PRODUCT domain unit of a conversation: one independent human
// intent and everything the agent did in response to it.
//
// It exists because a runtime "turn" is not a UI turn. A single human intent
// routinely drives dozens of model iterations and tool calls (real data: a claude
// transcript with 17 real user bubbles carries 552 assistant turns — 32.5× — and a
// codex rollout emits a separate event for every reasoning chunk, tool call and
// token_count). Rendering each of those as its own chat turn produces the bug this
// type kills: repeated avatars, empty answers, duplicated usage footers.
//
// Identity comes from the human intent, never from a jsonl line number, a tool id
// or a provider message id.
type AgentRun struct {
	ID    string `json:"id"`
	Index int    `json:"index"` // 1-based ordinal of the human intent (RoundNav's #N SSOT)

	// UserIntent is the human's own input that opened this run. Nil only for a system
	// run (runtime activity/notification with no human line — Diagnostic.NoIntent).
	UserIntent *RunIntent `json:"user_intent,omitempty"`
	// Amendments are human inputs that steered THIS run while it was still working.
	// They are not rounds (the adapter, not the projector, decides this — Turn.InputKind).
	Amendments []RunIntent `json:"amendments,omitempty"`

	// Segments is the agent's PROCESS, in strict transcript order: narration, thinking,
	// tools, subagents, compaction, errors. Collapsing it in the UI is a visibility state
	// only — segment ids/count/order/content never change.
	Segments []Block `json:"segments"`
	// FinalAnswer is the run's RESULT — the text of the terminal (yielding) iteration.
	// It is a separate field, not a segment, precisely so that collapsing the process can
	// never hide the answer. Empty = this run produced no answer (interrupted, tool-only,
	// crashed): the UI then says so honestly instead of rendering an empty bubble.
	FinalAnswer []Block `json:"final_answer,omitempty"`

	// Usage is the run's single aggregate (the footer consumes only this one).
	Usage *RunUsage `json:"usage,omitempty"`

	Status    string     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	Diagnostic RunDiagnostic `json:"diagnostic"`
}

// RunIntent is one human input (the opening intent, or a mid-run amendment).
type RunIntent struct {
	Text string     `json:"text"`
	At   *time.Time `json:"at,omitempty"`
}

// RunUsage is the run's aggregate — TYPED, because "sum every number in the map" is
// wrong for half of these fields: summing ttft across a run's 30 model iterations, or
// summing a per-iteration duration into a wall clock, invents numbers nobody measured;
// and a float cost truncated into an int silently loses money.
//
// Per-field policy is stated once, here:
//   - tokens/cost  → SUM across iterations (they are per-iteration deltas)
//   - ttft         → FIRST iteration's (time to first token of the run)
//   - duration     → SUM of the runtime's own measured per-iteration durations, when it
//     reports any; otherwise absent → the UI derives the honest wall clock
//     from EndedAt-StartedAt (never fabricated)
//   - model        → the model in force when the run ended, plus Models[] when it switched
type RunUsage struct {
	InputTokens       int      `json:"input_tokens,omitempty"`
	OutputTokens      int      `json:"output_tokens,omitempty"`
	ThinkingTokens    int      `json:"thinking_tokens,omitempty"`
	CacheReadTokens   int      `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens int      `json:"cache_create_tokens,omitempty"`
	TTFTMs            *int     `json:"ttft_ms,omitempty"`
	DurationMs        *int     `json:"duration_ms,omitempty"`
	CostUSD           *float64 `json:"cost_usd,omitempty"`
	Model             string   `json:"model,omitempty"`
	Models            []string `json:"models,omitempty"`
}

// AgentRun.Status — how the run ended, honestly. There is no "success" guess: a run
// whose transcript simply stops (session killed mid-tool-loop) is Unterminated, never
// Completed.
const (
	RunCompleted    = "completed"
	RunInterrupted  = "interrupted"
	RunError        = "error"
	RunUnterminated = "unterminated"
)

// RunDiagnostic exposes what the projection had to cope with, so a wrong-looking round
// is debuggable without re-reading the jsonl — and so a lossy source degrades visibly
// instead of silently.
type RunDiagnostic struct {
	RawTurns      int  `json:"raw_turns"`                // adapter turns folded into this run
	SegmentCount  int  `json:"segment_count"`            //
	UsageBlocks   int  `json:"usage_blocks,omitempty"`   // usage deltas summed into Usage
	OrphanResults int  `json:"orphan_results,omitempty"` // tool results with no matching call
	PendingTools  int  `json:"pending_tools,omitempty"`  // tool calls whose result never arrived
	NoIntent      bool `json:"no_intent,omitempty"`      // runtime activity with no human line
	Unterminated  bool `json:"unterminated,omitempty"`   // the runtime never yielded
}

// ProjectAgentRuns is the SINGLE, runtime-agnostic projector: adapter turns → human-
// facing runs. It has ZERO provider branches by construction: every fact it needs
// (is this human line an intent or a steer? did the runtime yield? which text is the
// answer?) was already stated by the adapter on Turn.InputKind / Turn.Terminal /
// Block.Final. It decides nothing by position and guesses nothing.
//
// Deterministic, idempotent, linear in turns: the same transcript always yields the
// same run ids, counts and order.
func ProjectAgentRuns(tr *Transcript) []AgentRun {
	if tr == nil || len(tr.Turns) == 0 {
		return nil
	}

	runs := make([]AgentRun, 0, 16)
	var cur *AgentRun
	intents := 0

	closeRun := func(terminal string, at *time.Time) {
		if cur == nil {
			return
		}
		switch terminal {
		case TerminalEndTurn:
			cur.Status = RunCompleted
		case TerminalAborted:
			cur.Status = RunInterrupted
		case TerminalError:
			cur.Status = RunError
		default:
			cur.Status = RunUnterminated
			cur.Diagnostic.Unterminated = true
		}
		if at != nil {
			cur.EndedAt = at
		}
		// A tool whose result never arrived did NOT finish. Counting them here (rather
		// than letting the UI assume "persisted ⇒ done") is what keeps an interrupted
		// run from rendering as a successful one.
		for i := range cur.Segments {
			if cur.Segments[i].Type == BlockTool && !cur.Segments[i].ResultSeen && !cur.Segments[i].Orphan {
				cur.Diagnostic.PendingTools++
			}
		}
		cur.Diagnostic.SegmentCount = len(cur.Segments)
		runs = append(runs, *cur)
		cur = nil
	}

	openRun := func(intent *RunIntent, at *time.Time, noIntent bool) {
		// A system/orphan run is NOT a round: it gets index 0, never the next round's
		// number (which would print two "#2"s and desync RoundNav from the backend count).
		idx := 0
		if !noIntent {
			intents++
			idx = intents
		}
		cur = &AgentRun{
			ID:         runID(tr.Ref, idx, at),
			Index:      idx,
			UserIntent: intent,
			Segments:   make([]Block, 0, 8),
			StartedAt:  at,
			Status:     RunUnterminated,
			Diagnostic: RunDiagnostic{NoIntent: noIntent},
		}
	}

	for i := range tr.Turns {
		t := &tr.Turns[i]

		if t.Role == "user" {
			text, notif := userTurnPayload(t)
			switch t.InputKind {
			case InputIntent:
				closeRun("", nil) // an unterminated/system run was open → close it honestly
				openRun(&RunIntent{Text: text, At: t.At}, t.At, false)
				cur.Diagnostic.RawTurns++
			case InputAmendment:
				if cur == nil {
					// The adapter says "steer" but no run is open (a truncated/partial
					// transcript). Honest degradation: it still gets a home, flagged.
					openRun(nil, t.At, true)
				}
				cur.Amendments = append(cur.Amendments, RunIntent{Text: text, At: t.At})
				cur.Diagnostic.RawTurns++
			case InputSystem:
				if notif == nil {
					continue
				}
				if cur == nil {
					openRun(nil, t.At, true) // a notification outside any run → its own system run
				}
				cur.Segments = append(cur.Segments, *notif)
				cur.Diagnostic.RawTurns++
			}
			continue
		}

		// assistant turn
		if cur == nil {
			openRun(nil, t.At, true) // runtime activity before any human line
		}
		cur.Diagnostic.RawTurns++
		for bi := range t.Blocks {
			b := t.Blocks[bi]
			switch {
			case b.Type == BlockUsage:
				cur.Usage = addUsage(cur.Usage, b.Usage)
				cur.Diagnostic.UsageBlocks++
			case b.Orphan:
				cur.Diagnostic.OrphanResults++
				cur.Segments = append(cur.Segments, b) // kept visible, never dropped
			case b.Final:
				// The answer leaves the process trace: it is the run's result.
				cur.FinalAnswer = append(cur.FinalAnswer, b)
			default:
				cur.Segments = append(cur.Segments, b)
			}
		}
		if t.At != nil {
			cur.EndedAt = t.At
		}
		if t.Terminal != "" {
			closeRun(t.Terminal, t.At)
		}
	}
	closeRun("", nil) // transcript ends mid-run → Unterminated (never a fake "completed")

	return runs
}

// userTurnPayload extracts what a user-role adapter turn carries: its human text and/or
// a runtime notification block. Tool results never reach here (adapters attach them onto
// their tool block).
func userTurnPayload(t *Turn) (text string, notif *Block) {
	for bi := range t.Blocks {
		b := &t.Blocks[bi]
		switch b.Type {
		case BlockUser:
			if b.Text != "" {
				text = b.Text
			}
		case BlockTaskNotification:
			notif = b
		}
	}
	return text, notif
}

// addUsage folds one usage delta into the run aggregate, per the field policy stated on
// RunUsage. This is deliberately NOT "range over the map and add every number": ttft and
// duration are not additive quantities, and cost is not an integer.
func addUsage(dst *RunUsage, src map[string]interface{}) *RunUsage {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = &RunUsage{}
	}
	dst.InputTokens += intField(src, "input_tokens")
	dst.OutputTokens += intField(src, "output_tokens")
	dst.ThinkingTokens += intField(src, "thinking_tokens")
	dst.CacheReadTokens += intField(src, "cache_read_input_tokens") + intField(src, "cache_read_tokens")
	dst.CacheCreateTokens += intField(src, "cache_creation_input_tokens") + intField(src, "cache_create_tokens")

	// ttft = time to the FIRST token of the run. Later iterations' ttft is not part of it
	// (and summing them would report a latency nobody experienced).
	if dst.TTFTMs == nil {
		if v, ok := numField(src, "ttft_ms"); ok {
			ms := int(v)
			dst.TTFTMs = &ms
		}
	}
	// duration: only what the runtime actually measured. Absent → the UI derives the wall
	// clock from the run's own timestamps rather than inventing a number here.
	if v, ok := numField(src, "duration_ms", "elapsed_ms"); ok {
		ms := int(v)
		if dst.DurationMs != nil {
			ms += *dst.DurationMs
		}
		dst.DurationMs = &ms
	}
	if v, ok := numField(src, "cost_usd"); ok {
		c := v
		if dst.CostUSD != nil {
			c += *dst.CostUSD
		}
		dst.CostUSD = &c // float, never truncated to int
	}
	if m, ok := src["model"].(string); ok && m != "" {
		dst.Model = m // last non-empty = the model in force when the run ended
		seen := false
		for _, x := range dst.Models {
			if x == m {
				seen = true
				break
			}
		}
		if !seen {
			dst.Models = append(dst.Models, m) // honest breakdown when a run switched models
		}
	}
	return dst
}

// numField reads the first present key as a float64 (JSON numbers arrive as float64;
// in-process maps carry int). Returns ok=false when no key is present.
func numField(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		}
	}
	return 0, false
}

// runID is a stable, source-scoped run identity: equal across live / replay / share for
// the same transcript, and stable across reloads (the UI keys its expansion state on it).
func runID(ref string, index int, at *time.Time) string {
	id := "run-" + itoa(index)
	if at != nil {
		id += "-" + itoa(int(at.UnixMilli()%1e9))
	}
	if ref != "" {
		return ref + ":" + id
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
