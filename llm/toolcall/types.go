// Package toolcall provides LLM tool call argument reassembly from streaming chunks.
// Handles incremental tool_calls[i].function.arguments fragments from OpenAI-compatible
// APIs (OpenAI, DeepSeek, GLM, Kimi, Qwen, Grok, Doubao, Gemini) and produces
// complete, validated ToolCall objects.
//
// Designed for extraction to github.com/brightman-ai/kit/toolcall.
package toolcall

// Delta represents an incremental tool call fragment from a streaming LLM response.
// In OpenAI-compatible APIs, tool_calls arrive as deltas:
//   - First delta for an index: carries ID + function.name (+ possibly partial arguments)
//   - Subsequent deltas: carry only function.arguments fragments to concatenate
type Delta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"` // incremental JSON fragment
	} `json:"function,omitempty"`
}

// ToolCall is a fully reassembled tool call with complete arguments.
type ToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and complete arguments JSON.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // complete JSON string
}
