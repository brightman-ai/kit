package agentloop

import (
	"context"
	"testing"

	"github.com/brightman-ai/kit/llm"
)

type fakeProvider struct {
	calls    []fakeCall
	response func(n int) *llm.Response
}

type fakeCall struct {
	messages []llm.Message
	opts     *llm.Options
}

func (f *fakeProvider) Chat(_ context.Context, m []llm.Message, o *llm.Options) (*llm.Response, error) {
	f.calls = append(f.calls, fakeCall{messages: m, opts: o})
	return f.response(len(f.calls)), nil
}
func (f *fakeProvider) Stream(context.Context, []llm.Message, *llm.Options) (<-chan llm.StreamChunk, error) {
	panic("unused")
}
func (f *fakeProvider) Name() string     { return "fake" }
func (f *fakeProvider) Models() []string { return []string{"fake-1"} }

func toolOpts() *llm.Options {
	return &llm.Options{
		Model: "fake-1",
		Tools: []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "read_tree"}}},
	}
}

// TestProviderModelDropsToolsOnFallbackWithoutMutatingCaller — 两件事一起钉：
// 兜底轮必须无工具（否则兜底白做），且**不能靠改调用方的 Options 实现** ——
// 那个 Options 常被宿主复用于后续 turn，就地清空 Tools 会让工具从此静默消失。
func TestProviderModelDropsToolsOnFallbackWithoutMutatingCaller(t *testing.T) {
	fp := &fakeProvider{response: func(n int) *llm.Response {
		if n == 1 {
			return &llm.Response{Tools: []llm.ToolCall{Call("c0", "read_tree", `{}`)}}
		}
		return &llm.Response{Content: "答案"}
	}}
	opts := toolOpts()
	model := ProviderModel(fp, ProviderConfig{Options: opts, System: "你是助手"})

	if _, err := model(t.Context(), "问题", Attempt{Round: 0}); err != nil {
		t.Fatal(err)
	}
	if len(fp.calls[0].opts.Tools) != 1 {
		t.Fatal("普通轮必须带工具")
	}
	if _, err := model(t.Context(), "兜底", Attempt{Round: 1, Fallback: true}); err != nil {
		t.Fatal(err)
	}
	if len(fp.calls[1].opts.Tools) != 0 {
		t.Fatal("兜底轮必须不带工具，否则模型会接着调下去、兜底等于没做")
	}
	if len(opts.Tools) != 1 {
		t.Fatal("调用方的 Options 被就地改了 —— 复用它的后续 turn 会静默失去工具")
	}
}

func TestProviderModelBuildsSystemHistoryInput(t *testing.T) {
	fp := &fakeProvider{response: func(int) *llm.Response { return &llm.Response{Content: "ok"} }}
	model := ProviderModel(fp, ProviderConfig{
		System:  "系统提示",
		History: []llm.Message{{Role: "user", Content: "上一轮"}, {Role: "assistant", Content: "上一答"}},
	})
	if _, err := model(t.Context(), "本轮问题", Attempt{}); err != nil {
		t.Fatal(err)
	}
	got := fp.calls[0].messages
	if len(got) != 4 {
		t.Fatalf("消息数 = %d, want 4 (system + 2 history + input)", len(got))
	}
	if got[0].Role != "system" || got[0].Content != "系统提示" {
		t.Errorf("第一条应是 system: %+v", got[0])
	}
	if got[3].Role != "user" || got[3].Content != "本轮问题" {
		t.Errorf("最后一条应是本轮输入: %+v", got[3])
	}
}

func TestProviderModelReportsUsagePerAttempt(t *testing.T) {
	fp := &fakeProvider{response: func(n int) *llm.Response {
		return &llm.Response{Content: "ok", Usage: llm.Usage{CompletionTokens: n * 10}}
	}}
	var seen []int
	model := ProviderModel(fp, ProviderConfig{
		OnUsage: func(u llm.Usage, _ Attempt) { seen = append(seen, u.CompletionTokens) },
	})
	for i := range 2 {
		if _, err := model(t.Context(), "x", Attempt{Round: i}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != 10 || seen[1] != 20 {
		t.Fatalf("每次调用都应回报用量, got %v", seen)
	}
}

// TestProviderModelDrivesAFullLoop — 端到端：Provider → ProviderModel → Run。
// 证明"第二个消费者只需十行"不是宣称。
func TestProviderModelDrivesAFullLoop(t *testing.T) {
	fp := &fakeProvider{response: func(n int) *llm.Response {
		if n == 1 {
			return &llm.Response{Tools: []llm.ToolCall{Call("c0", "read_tree", `{"root":271}`)}}
		}
		return &llm.Response{Content: "资产 Engine 有八节"}
	}}
	res, err := Run(t.Context(), Config{
		Model: ProviderModel(fp, ProviderConfig{Options: toolOpts(), System: "只依据给定材料回答"}),
		Tools: func(_ context.Context, c toolCall, _ int) Outcome {
			if ToolName(c) != "read_tree" {
				t.Errorf("unexpected tool %q", ToolName(c))
			}
			return Outcome{Result: "§1..§8", Status: StatusSuccess}
		},
		MaxRounds: 3,
	}, "资产 Engine 讲了什么")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "资产 Engine 有八节" || res.Stop != StopAnswered {
		t.Fatalf("content=%q stop=%q", res.Content, res.Stop)
	}
	if res.Rounds != 2 || len(res.Observations) != 1 {
		t.Fatalf("rounds=%d observations=%d, want 2/1", res.Rounds, len(res.Observations))
	}
}
