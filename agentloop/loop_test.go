package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/brightman-ai/kit/llm"
	"github.com/brightman-ai/kit/workstream"
)

// toolCall 只是本测试文件里的书写便利 —— agentloop 用的就是 kit/llm 的 ToolCall，
// 不另立一套词汇（那正是这次提取要消灭的东西）。
type toolCall = llm.ToolCall

func isValidJSONOrEmpty(raw json.RawMessage) bool {
	return len(raw) == 0 || json.Valid(raw)
}

// scriptedModel returns the given rounds in order; running past the end is a
// test bug and fails loudly rather than silently returning an empty round.
func scriptedModel(t *testing.T, rounds ...*Round) (ModelFunc, *[]string) {
	t.Helper()
	var inputs []string
	i := 0
	return func(_ context.Context, input string, _ Attempt) (*Round, error) {
		inputs = append(inputs, input)
		if i >= len(rounds) {
			t.Fatalf("model called %d times, only %d rounds scripted (input=%q)", i+1, len(rounds), input)
		}
		r := rounds[i]
		i++
		return r, nil
	}, &inputs
}

func answering(content string) *Round { return &Round{Content: content} }

func calling(content string, calls ...string) *Round {
	r := &Round{Content: content}
	for i, name := range calls {
		r.ToolCalls = append(r.ToolCalls, Call(fmt.Sprintf("c%d", i), name, `{}`))
	}
	return r
}

func TestRunReturnsAnswerWhenModelStopsCallingTools(t *testing.T) {
	model, _ := scriptedModel(t, answering("最终答案"))
	res, err := Run(t.Context(), Config{Model: model}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "最终答案" {
		t.Fatalf("content = %q", res.Content)
	}
	if res.Stop != StopAnswered {
		t.Fatalf("stop = %q, want %q", res.Stop, StopAnswered)
	}
	if res.Rounds != 1 {
		t.Fatalf("rounds = %d, want 1", res.Rounds)
	}
}

// TestRunWithoutToolsRunsExactlyOneRound — 宿主没有工具时，模型即使（幻觉地）
// 要求调工具也只能跑一轮。这是正确的退化行为，不是 bug：没有工具就没有新证据，
// 再转下去只会烧钱。
func TestRunWithoutToolsRunsExactlyOneRound(t *testing.T) {
	model, _ := scriptedModel(t, calling("我想查一下", "search"))
	res, err := Run(t.Context(), Config{Model: model, Tools: nil}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Rounds != 1 || res.Stop != StopAnswered {
		t.Fatalf("rounds=%d stop=%q, want 1 / %q", res.Rounds, res.Stop, StopAnswered)
	}
}

func TestRunFoldsResultsAndRestatesOriginalRequest(t *testing.T) {
	model, inputs := scriptedModel(t, calling("", "read"), answering("读完了"))
	res, err := Run(t.Context(), Config{
		Model: model,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "第一节内容", Status: StatusSuccess}
		},
	}, "原始问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "读完了" || res.Stop != StopAnswered {
		t.Fatalf("content=%q stop=%q", res.Content, res.Stop)
	}
	if len(*inputs) != 2 {
		t.Fatalf("模型应被调用 2 次, got %d", len(*inputs))
	}
	second := (*inputs)[1]
	if !strings.Contains(second, "第一节内容") {
		t.Errorf("第二轮输入应含工具结果: %q", second)
	}
	if !strings.Contains(second, "Original request: 原始问题") {
		t.Errorf("第二轮输入应重述原问题（否则长 trace 会把问题挤出焦点）: %q", second)
	}
}

func TestTerminalToolEndsWithItsResult(t *testing.T) {
	model, _ := scriptedModel(t, calling("模型的话", "write_file"))
	res, err := Run(t.Context(), Config{
		Model: model,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "写入的正文", Status: StatusSuccess, End: EndWithResult}
		},
	}, "写文件")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "写入的正文" {
		t.Fatalf("EndWithResult 应以工具结果为终答, got %q", res.Content)
	}
	if res.Stop != StopTerminalTool {
		t.Fatalf("stop = %q", res.Stop)
	}
}

func TestTerminalToolCanEndWithModelContent(t *testing.T) {
	model, _ := scriptedModel(t, calling("已派发给子任务", "delegate_task"))
	res, err := Run(t.Context(), Config{
		Model: model,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "child_session:42", Status: StatusSuccess, End: EndWithModelContent}
		},
	}, "派活")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "已派发给子任务" {
		t.Fatalf("EndWithModelContent 应以模型本轮内容为终答, got %q", res.Content)
	}
	if len(res.Observations) != 1 || res.Observations[0].Outcome.Result != "child_session:42" {
		t.Fatal("工具执行仍应被观察记录（宿主要据此落 Step）")
	}
}

// TestStallingLoopStopsOnNoProgressBudget — 模型反复调同一个工具拿同一份结果，
// 必须在预算用完时停，而不是把 MaxRounds 烧完。
func TestStallingLoopStopsOnNoProgressBudget(t *testing.T) {
	calls := 0
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) {
		calls++
		return calling("", "search"), nil
	})
	res, err := Run(t.Context(), Config{
		Model:               model,
		MaxRounds:           10,
		MaxNoProgressRounds: 2,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "同一份结果", Status: StatusSuccess}
		},
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopNoProgress {
		t.Fatalf("stop = %q, want %q", res.Stop, StopNoProgress)
	}
	// 第 1 轮拿到新证据算进展；第 2、3 轮重复 → 预算 2 用完。
	if res.Rounds != 3 {
		t.Fatalf("rounds = %d, want 3 (1 进展 + 2 无进展)", res.Rounds)
	}
	if calls >= 10 {
		t.Fatal("止损没生效，把 MaxRounds 烧完了")
	}
}

func TestZeroNoProgressBudgetRunsToMaxRounds(t *testing.T) {
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) {
		return calling("", "search"), nil
	})
	res, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 3, MaxNoProgressRounds: 0,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "同一份", Status: StatusSuccess}
		},
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopExhausted || res.Rounds != 3 {
		t.Fatalf("stop=%q rounds=%d, want %q / 3", res.Stop, res.Rounds, StopExhausted)
	}
}

func TestFallbackAnswersAfterExhaustion(t *testing.T) {
	round := 0
	model := ModelFunc(func(_ context.Context, input string, _ Attempt) (*Round, error) {
		round++
		if strings.Contains(input, "FALLBACK") {
			return answering("兜底答案"), nil
		}
		return calling("", "search"), nil
	})
	var sawStop StopReason
	res, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 2,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: fmt.Sprintf("证据 %d", round), Status: StatusSuccess}
		},
		Fallback: func(obs []Observation, stop StopReason) string {
			sawStop = stop
			return fmt.Sprintf("FALLBACK: %d 条证据", len(obs))
		},
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "兜底答案" || !res.FallbackUsed {
		t.Fatalf("content=%q fallbackUsed=%v", res.Content, res.FallbackUsed)
	}
	if sawStop != StopExhausted {
		t.Fatalf("Fallback 应收到真实停机理由, got %q", sawStop)
	}
	// 停机理由必须保留 —— 兜底答出来了不代表循环是正常收敛的，宿主要能分辨。
	if res.Stop != StopExhausted {
		t.Fatalf("兜底成功不该把 Stop 改写成 answered, got %q", res.Stop)
	}
}

// TestFallbackAttemptIsFlagged — 宿主靠这个标志决定"这一次不挂工具"。
// 它必须是显式的：靠 Round 序号推会在提前止损时算错，而算错的后果是兜底那轮
// 又带上了工具、模型接着调下去，兜底白做且多烧一轮。
func TestFallbackAttemptIsFlagged(t *testing.T) {
	var attempts []Attempt
	model := ModelFunc(func(_ context.Context, _ string, at Attempt) (*Round, error) {
		attempts = append(attempts, at)
		if at.Fallback {
			return answering("兜底"), nil
		}
		return calling("", "search"), nil
	})
	_, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 3, MaxNoProgressRounds: 1,
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "同一份", Status: StatusSuccess}
		},
		Fallback: func([]Observation, StopReason) string { return "最后一问" },
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) < 2 {
		t.Fatalf("attempts = %d, 至少应有循环轮 + 兜底轮", len(attempts))
	}
	for i, at := range attempts[:len(attempts)-1] {
		if at.Fallback {
			t.Errorf("第 %d 次不是兜底却被标成 Fallback", i)
		}
	}
	if !attempts[len(attempts)-1].Fallback {
		t.Error("最后一次是兜底，必须标 Fallback=true")
	}
}

func TestEmptyFallbackPromptSkipsTheAttempt(t *testing.T) {
	calls := 0
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) {
		calls++
		return calling("", "search"), nil
	})
	res, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 1,
		Tools:    func(context.Context, toolCall, int) Outcome { return Outcome{Result: "x", Status: StatusSuccess} },
		Fallback: func([]Observation, StopReason) string { return "   " },
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("空 fallback prompt 不该再调模型, calls=%d", calls)
	}
	if res.FallbackUsed {
		t.Fatal("没跑兜底就不该标 FallbackUsed")
	}
}

func TestModelErrorPropagatesWithPartialResult(t *testing.T) {
	boom := errors.New("upstream 502")
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) { return nil, boom })
	res, err := Run(t.Context(), Config{Model: model}, "问题")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if res == nil || res.Rounds != 1 {
		t.Fatal("模型报错时仍应返回已跑轮次，便于宿主落已有证据")
	}
}

func TestMissingModelIsRejected(t *testing.T) {
	if _, err := Run(t.Context(), Config{}, "x"); !errors.Is(err, ErrNoModel) {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

// TestEmitsToolAndStallEvents — 可观测性是这层的交付物之一，不是附赠。
// 循环只发它独家知道的事件：tool_start / tool_result（带 progress）/ 停滞告警。
func TestEmitsToolAndStallEvents(t *testing.T) {
	var events []workstream.Event
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) {
		return calling("", "search"), nil
	})
	_, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 5, MaxNoProgressRounds: 1,
		Emit: workstream.Collect(&events),
		Tools: func(context.Context, toolCall, int) Outcome {
			return Outcome{Result: "同一份", Status: StatusSuccess}
		},
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}

	var starts, results, stalls int
	for _, ev := range events {
		if !workstream.Known(ev.Kind) {
			t.Errorf("发出了未注册的 Kind %q", ev.Kind)
		}
		switch ev.Kind {
		case workstream.ToolStart:
			starts++
		case workstream.ToolResult:
			results++
			if _, ok := ev.Meta["progress"]; !ok {
				t.Error("tool_result 必须带 progress 判定 —— 否则前端看不出循环在不在推进")
			}
			if _, ok := ev.Meta["round"]; !ok {
				t.Error("tool_result 必须带 round")
			}
		case workstream.Status:
			if ev.Status == "tool_loop_no_progress" {
				stalls++
			}
		}
	}
	if starts == 0 || starts != results {
		t.Fatalf("tool_start/tool_result 必须配对: %d/%d", starts, results)
	}
	if stalls == 0 {
		t.Fatal("停滞必须发告警事件，否则运维只看到循环变慢、不知道为什么")
	}
}

func TestEventIDsPairStartWithResultAndMatchObservations(t *testing.T) {
	var events []workstream.Event
	model, _ := scriptedModel(t, calling("", "a", "b"), answering("好"))
	res, err := Run(t.Context(), Config{
		Model: model, Emit: workstream.Collect(&events),
		Tools: func(_ context.Context, c toolCall, _ int) Outcome {
			return Outcome{Result: "r-" + ToolName(c), Status: StatusSuccess}
		},
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(res.Observations))
	}
	ids := map[string]int{}
	for _, ev := range events {
		if ev.Tool != nil {
			ids[ev.Tool.ID]++
		}
	}
	for _, o := range res.Observations {
		if ids[o.EventID] != 2 {
			t.Errorf("Observation.EventID %q 应正好对应一对 start/result 事件, got %d", o.EventID, ids[o.EventID])
		}
	}
}

func TestEventIDsPreserveProviderIdentity(t *testing.T) {
	events := make([]workstream.Event, 0, 4)
	model, _ := scriptedModel(t,
		&Round{ToolCalls: []llm.ToolCall{
			Call("provider-call-a", "read", `{"path":"a"}`),
			Call("provider-call-b", "read", `{"path":"b"}`),
		}},
		&Round{Content: "done"},
	)
	result, err := Run(context.Background(), Config{
		MaxRounds: 2,
		Model:     model,
		Tools: func(context.Context, llm.ToolCall, int) Outcome {
			return Outcome{Result: "ok", Status: StatusSuccess}
		},
		Emit: func(ev workstream.Event) bool {
			events = append(events, ev)
			return true
		},
	}, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"provider-call-a", "provider-call-b"}
	if len(result.Observations) != len(want) {
		t.Fatalf("observations = %d, want %d", len(result.Observations), len(want))
	}
	for i, id := range want {
		if result.Observations[i].EventID != id {
			t.Errorf("observation[%d].EventID = %q, want %q", i, result.Observations[i].EventID, id)
		}
		if result.Observations[i].Call.ID != id {
			t.Errorf("observation[%d].Call.ID = %q, want canonical provider id %q", i, result.Observations[i].Call.ID, id)
		}
		seen := 0
		for _, ev := range events {
			if ev.Tool != nil && ev.Tool.ID == id {
				seen++
			}
		}
		if seen != 2 {
			t.Errorf("provider id %q appears in %d workstream events, want start+result", id, seen)
		}
	}
}

func TestEventIDsCanonicalizeMissingAndDuplicateCalls(t *testing.T) {
	model, _ := scriptedModel(t,
		&Round{ToolCalls: []llm.ToolCall{
			Call("", "read", `{}`),
			Call("duplicate", "read", `{}`),
			Call("duplicate", "read", `{}`),
		}},
		&Round{Content: "done"},
	)
	var executed []string
	result, err := Run(t.Context(), Config{
		MaxRounds: 2,
		Model:     model,
		Tools: func(_ context.Context, call llm.ToolCall, _ int) Outcome {
			executed = append(executed, call.ID)
			return Outcome{Result: "ok", Status: StatusSuccess}
		},
	}, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 3 || len(executed) != 3 {
		t.Fatalf("observations=%d executed=%d, want 3/3", len(result.Observations), len(executed))
	}
	seen := map[string]bool{}
	for i, obs := range result.Observations {
		if obs.EventID == "" || obs.Call.ID != obs.EventID || executed[i] != obs.EventID {
			t.Fatalf("call %d identity diverged: event=%q observation=%q execution=%q", i, obs.EventID, obs.Call.ID, executed[i])
		}
		if seen[obs.EventID] {
			t.Fatalf("canonical id %q reused", obs.EventID)
		}
		seen[obs.EventID] = true
	}
}

func TestMalformedToolArgumentsStillProduceRenderableEvent(t *testing.T) {
	var events []workstream.Event
	model := ModelFunc(func(_ context.Context, _ string, at Attempt) (*Round, error) {
		if at.Round > 0 {
			return answering("好"), nil
		}
		return &Round{ToolCalls: []toolCall{Call("c0", "search", `{"broken`)}}, nil
	})
	_, err := Run(t.Context(), Config{
		Model: model, Emit: workstream.Collect(&events),
		Tools: func(context.Context, toolCall, int) Outcome { return Outcome{Result: "ok", Status: StatusSuccess} },
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == workstream.ToolStart && !isValidJSONOrEmpty(ev.Tool.Input) {
			t.Fatalf("坏参数必须被编码成合法 JSON，否则整帧 SSE 报废: %s", ev.Tool.Input)
		}
	}
}

// TestConfigResultLooksFailedReachesTracker — 这个钩子必须从 Config 够得着。
// ProgressTracker 上写着它、Run 却不透传 = 文档里承诺了一个不可达的能力，
// 宿主照着文档配了却毫无效果，且不会报错。
func TestConfigResultLooksFailedReachesTracker(t *testing.T) {
	model := ModelFunc(func(context.Context, string, Attempt) (*Round, error) {
		return calling("", "search"), nil
	})
	res, err := Run(t.Context(), Config{
		Model: model, MaxRounds: 5, MaxNoProgressRounds: 1,
		Tools: func(context.Context, toolCall, int) Outcome {
			// 每轮结果都不同 —— 若不识别内联失败，就会一直"有进展"、烧满轮次。
			return Outcome{Result: fmt.Sprintf("ERR_TIMEOUT: 第 %d 次", len(t.Name())), Status: StatusSuccess}
		},
		ResultLooksFailed: func(r string) bool { return strings.HasPrefix(r, "ERR_") },
	}, "问题")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopNoProgress {
		t.Fatalf("注入的内联失败识别没生效: stop=%q rounds=%d", res.Stop, res.Rounds)
	}
}
