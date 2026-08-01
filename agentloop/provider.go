package agentloop

import (
	"context"
	"strings"

	"github.com/brightman-ai/kit/llm"
	"github.com/brightman-ai/kit/llm/toolcall"
)

// ProviderConfig describes how to turn one loop input into one provider call.
type ProviderConfig struct {
	// Options carries model / max_tokens / Tools / etc. Never mutated: the
	// fallback attempt needs a tool-less copy, and mutating the caller's struct
	// to get it would silently disarm tools for every later turn sharing it.
	Options *llm.Options

	// System, when non-empty, is prepended as a system message on every call.
	System string

	// History is prior conversation, inserted between System and the input.
	History []llm.Message

	// OnUsage reports token accounting per call. 计费是宿主的账 —— 这层只转交，
	// 不累计、不解释。
	OnUsage func(usage llm.Usage, at Attempt)

	// OnText / OnReasoning receive streaming deltas (ProviderStreamModel only).
	// 宿主拿它们往自己的事件流上发，所以这里不预设事件形状。
	OnText      func(delta string, at Attempt)
	OnReasoning func(delta string, at Attempt)
}

// ProviderStreamModel is ProviderModel over the provider's streaming API: the
// answer arrives as deltas (surfaced via OnText/OnReasoning) while the loop
// still gets a complete Round.
//
// 为什么两个都要：多轮 agent 循环和"逐字出字"不是二选一。用同步 Chat 跑循环，用户
// 要盯着空白等一整轮；用流式但丢掉循环，就没有工具。这里让一次流式调用同时产出
// 增量（给 UI）和完整 Round（给循环）。
func ProviderStreamModel(p llm.Provider, cfg ProviderConfig) ModelFunc {
	return func(ctx context.Context, input string, at Attempt) (*Round, error) {
		ch, err := p.Stream(ctx, buildMessages(cfg, input), optionsFor(cfg, at))
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		asm := toolcall.NewAssembler()
		for chunk := range ch {
			if chunk.Error != nil {
				return nil, chunk.Error
			}
			if chunk.Content != "" {
				text.WriteString(chunk.Content)
				if cfg.OnText != nil {
					cfg.OnText(chunk.Content, at)
				}
			}
			if chunk.ReasoningContent != "" && cfg.OnReasoning != nil {
				cfg.OnReasoning(chunk.ReasoningContent, at)
			}
			for _, d := range chunk.ToolCalls {
				asm.Feed(d)
			}
			if chunk.Usage != nil && cfg.OnUsage != nil {
				cfg.OnUsage(*chunk.Usage, at)
			}
		}
		return &Round{Content: text.String(), ToolCalls: asm.Complete()}, nil
	}
}

func buildMessages(cfg ProviderConfig, input string) []llm.Message {
	messages := make([]llm.Message, 0, len(cfg.History)+2)
	if cfg.System != "" {
		messages = append(messages, llm.Message{Role: "system", Content: cfg.System})
	}
	messages = append(messages, cfg.History...)
	return append(messages, llm.Message{Role: "user", Content: input})
}

// optionsFor returns cfg.Options, or a tool-less copy on the fallback attempt.
// 拷贝而非就地清空：调用方常把同一个 Options 复用于后续 turn，就地改会让工具从此静默消失。
func optionsFor(cfg ProviderConfig, at Attempt) *llm.Options {
	opts := cfg.Options
	if at.Fallback && opts != nil && len(opts.Tools) > 0 {
		clone := *opts
		clone.Tools = nil
		clone.ToolChoice = nil
		return &clone
	}
	return opts
}

// ProviderModel adapts any kit/llm Provider into a ModelFunc.
//
// 它替宿主守住一条容易漏的规矩：**兜底那一次必须不带工具**。忘了这条，模型会在兜底轮
// 继续要工具，兜底等于没做，还多烧一轮 —— 而且不报错。放在这里，所有用 kit/llm
// Provider 的宿主都免费拿到，不必各自记得。
func ProviderModel(p llm.Provider, cfg ProviderConfig) ModelFunc {
	return func(ctx context.Context, input string, at Attempt) (*Round, error) {
		resp, err := p.Chat(ctx, buildMessages(cfg, input), optionsFor(cfg, at))
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return &Round{}, nil
		}
		if cfg.OnUsage != nil {
			cfg.OnUsage(resp.Usage, at)
		}
		// resp.Tools 就是 []llm.ToolCall —— agentloop 用的同一个类型，零转换。
		// 这是"不另立词汇"的直接回报：少一层映射，就少一处会漂的地方。
		return &Round{Content: resp.Content, ToolCalls: resp.Tools}, nil
	}
}
