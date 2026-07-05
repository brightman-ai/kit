// Package llm defines a minimal, OpenAI-compatible chat port: the message,
// option, provider and streaming shapes an application needs to talk to a chat
// LLM, with no concrete client or transport bundled in. It lets independent
// components share one contract — and swap providers — instead of each
// redefining these types and writing adapters between them. Dependency-free
// (standard library only).
package llm

import (
	"context"
	"strings"
)

// Message represents a chat message.
//
// Content is `any` so a message can carry EITHER a plain string (the common case)
// OR an OpenAI-compatible multimodal content array
// ([]ContentPart{{Type:"text"...},{Type:"image_url"...}}) for vision turns. Both
// marshal to the wire shape the OpenAI-compatible /chat/completions endpoint
// expects (string → "content":"...", array → "content":[...]). [CHG-015 需求8]
type Message struct {
	Role    string `json:"role"` // system, user, assistant, tool
	Content any    `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ContentPart is one element of an OpenAI-compatible multimodal content array.
// Text parts carry Text; image parts carry ImageURL (a data: URL or http URL).
type ContentPart struct {
	Type     string        `json:"type"` // "text" | "image_url"
	Text     string        `json:"text,omitempty"`
	ImageURL *ContentImage `json:"image_url,omitempty"`
}

// ContentImage is the image_url payload of an image content part.
type ContentImage struct {
	URL string `json:"url"` // data:image/...;base64,... OR https://...
}

// VisionUserMessage builds a multimodal user message: the prompt text followed by
// one image_url part per image URL. Empty images → falls back to a plain text
// message (so callers never need to branch). [CHG-015 需求8]
func VisionUserMessage(text string, imageURLs []string) Message {
	if len(imageURLs) == 0 {
		return UserMessage(text)
	}
	parts := make([]ContentPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, ContentPart{Type: "text", Text: text})
	}
	for _, u := range imageURLs {
		if u == "" {
			continue
		}
		parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ContentImage{URL: u}})
	}
	if len(parts) == 0 {
		return UserMessage(text)
	}
	return Message{Role: "user", Content: parts}
}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Response represents a chat completion response.
type Response struct {
	ID      string     `json:"id"`
	Model   string     `json:"model"`
	Content string     `json:"content"`
	Role    string     `json:"role"`
	Tools   []ToolCall `json:"tool_calls,omitempty"`
	Usage   Usage      `json:"usage"`
}

// Usage represents token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`    // prompt_tokens_details.cached_tokens (cache read)
	ReasoningTokens  int `json:"reasoning_tokens"` // completion_tokens_details.reasoning_tokens (thinking 单列)
}

// StreamChunk represents a streaming response chunk.
type StreamChunk struct {
	ID               string          `json:"id"`
	Model            string          `json:"model,omitempty"` // 真实使用的 model id (provider 回填)
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"` // GLM-5: 思考过程增量
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`        // GLM-5: 工具调用增量
	Usage            *Usage          `json:"usage,omitempty"`             // 流末尾 usage chunk (include_usage)
	Done             bool            `json:"done"`
	Error            error           `json:"-"`
}

// ToolCallDelta represents incremental tool call data in streaming (GLM-5 specific)
type ToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"` // 累积拼接
	} `json:"function,omitempty"`
}

// Tool represents a tool definition for function calling.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction represents a function definition.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Options represents options for chat completion.
type Options struct {
	Model       string          `json:"model,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	TopP        float64         `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	Thinking    *ThinkingConfig `json:"thinking,omitempty"`    // GLM-5: 启用深度思考
	ToolStream  bool            `json:"tool_stream,omitempty"` // GLM-5: 启用工具流式输出
	// Effort is the abstract reasoning tier ("low"|"medium"|"high"), CHG-015.
	// Providers translate it to their native shape (e.g. GLM Thinking) via
	// ApplyEffort; providers without reasoning silently ignore it (no-op).
	Effort string `json:"effort,omitempty"`
	// ResponseFormat is the OpenAI-compatible response_format payload, e.g.
	// map[string]string{"type":"json_object"} to force JSON mode. nil → field
	// not sent. [r9a DESIGN-r9-P1 §3.1: JSON mode 实测显著提升 decompose 解析成功率]
	ResponseFormat any `json:"response_format,omitempty"`
}

// ThinkingConfig configures deep thinking mode (GLM-5 specific)
type ThinkingConfig struct {
	Type string `json:"type"` // "enabled" | "disabled"
}

// ApplyEffort translates the abstract Effort tier into the OpenAI-compatible
// native shape (GLM Thinking), in place. It is a no-op when Effort is empty or
// when Thinking was already set explicitly. Providers that do not support
// reasoning simply never send the resulting field (silent degrade). [CHG-015]
func (o *Options) ApplyEffort() {
	if o == nil || o.Effort == "" || o.Thinking != nil {
		return
	}
	switch o.Effort {
	case "low":
		o.Thinking = &ThinkingConfig{Type: "disabled"}
	case "medium", "high":
		o.Thinking = &ThinkingConfig{Type: "enabled"}
	}
}

// Provider is the interface for LLM providers.
type Provider interface {
	// Chat sends a chat completion request and returns the response.
	Chat(ctx context.Context, messages []Message, opts *Options) (*Response, error)

	// Stream sends a chat completion request and streams the response.
	Stream(ctx context.Context, messages []Message, opts *Options) (<-chan StreamChunk, error)

	// Name returns the provider name.
	Name() string

	// Models returns available models.
	Models() []string
}

// MessageText coerces a message's Content to plain text: returns the string as-is
// for a string Content, or concatenates the text parts of a multimodal content
// array (image parts contribute nothing). Lets text-only readers (token estimate,
// isolation checks, last-user extraction) treat any Message uniformly. [CHG-015 需求8]
func MessageText(m Message) string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []ContentPart:
		var b strings.Builder
		for _, p := range c {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// SystemMessage creates a system message.
func SystemMessage(content string) Message {
	return Message{Role: "system", Content: content}
}

// UserMessage creates a user message.
func UserMessage(content string) Message {
	return Message{Role: "user", Content: content}
}

// AssistantMessage creates an assistant message.
func AssistantMessage(content string) Message {
	return Message{Role: "assistant", Content: content}
}

// ToolMessage creates a tool result message.
func ToolMessage(name, content string) Message {
	return Message{Role: "tool", Name: name, Content: content}
}
