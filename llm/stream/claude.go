package stream

import (
	"encoding/json"
	"fmt"
	"strings"

	event "github.com/brightman-ai/kit/llm/event"
)

// NewClaudeDecoder returns a stateful decoder for Claude Code
// --output-format stream-json output, including --include-partial-messages.
func NewClaudeDecoder() Decoder {
	return &claudeDecoder{
		blocks:       make(map[string]*claudeBlockState),
		textStreamed: make(map[string]bool),
		toolEmitted:  make(map[string]bool),
	}
}

type claudeDecoder struct {
	currentMessageID string
	blocks           map[string]*claudeBlockState
	textStreamed     map[string]bool
	toolEmitted      map[string]bool
}

type claudeBlockState struct {
	typ   string
	id    string
	name  string
	input strings.Builder
}

type claudeWireEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Error     string `json:"error,omitempty"`
	Message   *struct {
		ID      string               `json:"id,omitempty"`
		Model   string               `json:"model,omitempty"`
		Content []claudeContentBlock `json:"content"`
		Usage   *claudeUsage         `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Event      *claudeStreamEvent `json:"event,omitempty"`
	Delta      *claudeDelta       `json:"delta,omitempty"` // older direct delta shape
	Result     string             `json:"result,omitempty"`
	Cost       float64            `json:"total_cost_usd,omitempty"`
	CostUSD    float64            `json:"cost_usd,omitempty"`
	DurationMs float64            `json:"duration_ms,omitempty"`
	NumTurns   int                `json:"num_turns,omitempty"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type claudeStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index,omitempty"`
	Message *struct {
		ID string `json:"id,omitempty"`
	} `json:"message,omitempty"`
	ContentBlock *claudeContentBlock `json:"content_block,omitempty"`
	Delta        *claudeDelta        `json:"delta,omitempty"`
	TTFTMs       float64             `json:"ttft_ms,omitempty"`
}

type claudeDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

func (d *claudeDecoder) DecodeLine(line []byte) []event.Event {
	if len(line) == 0 {
		return nil
	}

	var raw claudeWireEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return []event.Event{event.ErrorEvent(fmt.Sprintf("parse error: %s", err.Error()))}
	}

	switch raw.Type {
	case "stream_event":
		if raw.Event == nil {
			return nil
		}
		return d.decodeStreamEvent(raw.Event)
	case "assistant":
		return d.decodeAssistant(raw)
	case "content_block_delta":
		return d.decodeDelta(raw.Delta)
	case "user":
		return decodeClaudeToolResults(raw)
	case "result":
		return []event.Event{decodeClaudeDone(raw)}
	case "system":
		status := "system"
		if raw.Subtype != "" {
			status = raw.Subtype
		}
		ev := event.StatusEvent(status)
		if raw.SessionID != "" {
			ev = ev.WithMeta("session_id", raw.SessionID)
		}
		return []event.Event{ev}
	case "rate_limit_event":
		return []event.Event{event.StatusEvent("rate_limit").WithMeta("raw", string(line))}
	default:
		return []event.Event{event.RawEvent(json.RawMessage(line))}
	}
}

func (d *claudeDecoder) Flush() []event.Event {
	var out []event.Event
	for key, block := range d.blocks {
		if block.typ == "tool_use" {
			if ev, ok := d.toolStartFromBlock(block); ok {
				out = append(out, ev)
			}
		}
		delete(d.blocks, key)
	}
	return out
}

func (d *claudeDecoder) decodeStreamEvent(ev *claudeStreamEvent) []event.Event {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			d.currentMessageID = ev.Message.ID
		}
		if ev.TTFTMs > 0 {
			return []event.Event{event.StatusEvent("streaming").WithMeta("ttft_ms", ev.TTFTMs)}
		}
	case "content_block_start":
		if ev.ContentBlock == nil {
			return nil
		}
		block := ev.ContentBlock
		d.blocks[d.blockKey(ev.Index)] = &claudeBlockState{
			typ:  block.Type,
			id:   block.ID,
			name: block.Name,
		}
		if block.Type == "thinking" {
			return []event.Event{event.StatusEvent("thinking")}
		}
		if block.Type == "tool_use" {
			return []event.Event{event.StatusEvent("tool_call")}
		}
	case "content_block_delta":
		return d.decodeStreamDelta(ev.Index, ev.Delta)
	case "content_block_stop":
		key := d.blockKey(ev.Index)
		block := d.blocks[key]
		delete(d.blocks, key)
		if block == nil || block.typ != "tool_use" {
			return nil
		}
		if ev, ok := d.toolStartFromBlock(block); ok {
			return []event.Event{ev}
		}
	}
	return nil
}

func (d *claudeDecoder) decodeStreamDelta(index int, delta *claudeDelta) []event.Event {
	if delta == nil {
		return nil
	}
	switch delta.Type {
	case "text_delta":
		if delta.Text == "" {
			return nil
		}
		if d.currentMessageID != "" {
			d.textStreamed[d.currentMessageID] = true
		}
		return []event.Event{event.TextEvent(delta.Text)}
	case "thinking_delta":
		if delta.Thinking == "" {
			return nil
		}
		if d.currentMessageID != "" {
			d.textStreamed[d.currentMessageID] = true
		}
		return []event.Event{event.ThinkingEvent(delta.Thinking)}
	case "input_json_delta":
		if block := d.blocks[d.blockKey(index)]; block != nil && block.typ == "tool_use" {
			block.input.WriteString(delta.PartialJSON)
		}
	}
	return nil
}

func (d *claudeDecoder) decodeDelta(delta *claudeDelta) []event.Event {
	if delta == nil {
		return nil
	}
	if delta.Text != "" {
		return []event.Event{event.TextEvent(delta.Text)}
	}
	if delta.Thinking != "" {
		return []event.Event{event.ThinkingEvent(delta.Thinking)}
	}
	return nil
}

func (d *claudeDecoder) decodeAssistant(raw claudeWireEvent) []event.Event {
	var out []event.Event
	if raw.Message != nil && raw.Message.ID != "" {
		d.currentMessageID = raw.Message.ID
	}
	if raw.Message != nil {
		if raw.Message.Usage != nil {
			out = append(out, event.UsageEvent(event.UsageData{
				InputTokens:              raw.Message.Usage.InputTokens,
				OutputTokens:             raw.Message.Usage.OutputTokens,
				CacheReadInputTokens:     raw.Message.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: raw.Message.Usage.CacheCreationInputTokens,
				TotalTokens: raw.Message.Usage.InputTokens +
					raw.Message.Usage.OutputTokens +
					raw.Message.Usage.CacheReadInputTokens +
					raw.Message.Usage.CacheCreationInputTokens,
			}))
		}
		alreadyStreamed := raw.Message.ID != "" && d.textStreamed[raw.Message.ID]
		for _, c := range raw.Message.Content {
			switch c.Type {
			case "text":
				if !alreadyStreamed && c.Text != "" {
					out = append(out, event.TextEvent(c.Text))
				}
			case "thinking":
				if !alreadyStreamed && c.Thinking != "" {
					out = append(out, event.ThinkingEvent(c.Thinking))
				}
			case "tool_use":
				if d.toolEmitted[c.ID] {
					continue
				}
				input := c.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				d.toolEmitted[c.ID] = true
				out = append(out, event.ToolStartEvent(c.ID, c.Name, input))
			}
		}
	}
	if raw.Error != "" {
		out = append(out, event.ErrorEvent(raw.Error))
	}
	return out
}

func decodeClaudeToolResults(raw claudeWireEvent) []event.Event {
	if raw.Message == nil {
		return nil
	}
	out := make([]event.Event, 0, len(raw.Message.Content))
	for _, c := range raw.Message.Content {
		if c.Type != "tool_result" {
			continue
		}
		out = append(out, event.ToolResultEvent(c.ToolUseID, "", stringifyClaudeToolResult(c.Content), c.IsError, 0))
	}
	return out
}

func decodeClaudeDone(raw claudeWireEvent) event.Event {
	reason := "stop"
	if raw.Subtype == "error" || raw.IsError {
		reason = "error"
	}
	ev := event.DoneEvent(reason, 0, 0)
	if raw.Cost+raw.CostUSD > 0 || raw.DurationMs > 0 || raw.NumTurns > 0 {
		ev = ev.WithMeta("cost_usd", raw.Cost+raw.CostUSD)
		ev = ev.WithMeta("duration_ms", raw.DurationMs)
		ev = ev.WithMeta("num_turns", raw.NumTurns)
	}
	if raw.IsError && raw.Result != "" {
		ev.Content = raw.Result
	}
	return ev
}

func (d *claudeDecoder) toolStartFromBlock(block *claudeBlockState) (event.Event, bool) {
	if block.id == "" || d.toolEmitted[block.id] {
		return event.Event{}, false
	}
	input := strings.TrimSpace(block.input.String())
	if input == "" {
		input = "{}"
	}
	d.toolEmitted[block.id] = true
	return event.ToolStartEvent(block.id, block.name, json.RawMessage(input)), true
}

func (d *claudeDecoder) blockKey(index int) string {
	return fmt.Sprintf("%s:%d", d.currentMessageID, index)
}

func stringifyClaudeToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}
