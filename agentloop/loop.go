// Package agentloop runs the model↔tool round loop that turns a single user
// input into a single answer, and — the part that is actually hard — decides
// when to stop.
//
// # 为什么这层值得独立存在
//
// 调模型、执行工具都是宿主的事，各家都不一样。真正各家都要重新踩一遍的是**收敛**：
// 模型反复调同一个工具拿同一份结果、绕圈、把轮次烧完却什么都没多知道。判"这一轮
// 到底有没有进展"需要一套非平凡的规则（同名同参同结果不算进展、参数 JSON 键序不同
// 但语义相同不算进展、报错不算进展），而这套规则一旦各家各写一份就必然分叉。
//
// 所以这里只放三件事：**轮次预算 · 进展判定 · 停机理由**。
//
// # 边界（这层故意不知道的事）
//
// 会话、轮次、持久化、prompt 组装、token 计费、鉴权 —— 一概不知道。它们是宿主的
// 领域模型，塞进来只会让这个包变成第二个 conversation 包。宿主通过两个闭包
// （Model / Tools）把自己的世界接进来，通过 Observation 把结果映射回自己的世界。
//
// # 观测
//
// 只发它独家知道的事件：tool_start / tool_result（带 progress 判定）/
// tool_loop_no_progress。宿主自己知道的（calling_model、usage）在宿主的闭包里发，
// 用 workstream.Decorate 把 session_id 之类焊上去。
package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brightman-ai/kit/llm"
	"github.com/brightman-ai/kit/workstream"
)

// ErrNoModel — Config.Model 为空。这是编程错误，不是运行时状况。
var ErrNoModel = errors.New("agentloop: Config.Model is required")

// Round is one model turn's output: some text, and zero or more tool calls.
//
// 故意不含 usage —— 计费是宿主的账，宿主的 Model 闭包里就能拿到并记录，
// 让这层知道只会逼它长出 model 名、缓存命中之类跟收敛毫无关系的字段。
type Round struct {
	Content   string
	ToolCalls []llm.ToolCall
}

// Attempt tells the host which model call this is.
//
// Fallback 必须显式，不能让宿主靠 Round 序号推：兜底那一次**必须不带工具**（否则模型
// 会接着调下去，兜底就白做了），而"第几轮算兜底"依赖 MaxRounds、提前止损、轮次是否
// 被工具提前终止 —— 让宿主去推这件事，就是把一个循环内部的实现细节泄露成宿主的契约。
type Attempt struct {
	// Round is 0-based.
	Round int
	// Fallback marks the last-resort call made after the loop stopped without an
	// answer. Hosts MUST advertise no tools on it.
	Fallback bool
}

// ModelFunc asks the model for one round.
type ModelFunc func(ctx context.Context, input string, at Attempt) (*Round, error)

// Termination says whether a tool ends the turn, and what the answer then is.
//
// 用三态枚举而不是 bool + 约定，是因为这两种"结束"真实存在且答案来源不同：
// 工具自己产出了终答（写文件成功的回执），与工具只是个副作用、终答是模型这一轮
// 说的话（派发子任务）。用 bool 表达就得靠"Result 为空则取模型内容"这种隐式规则，
// 而隐式规则会在半年后被人读错。
type Termination uint8

const (
	// Continue — 普通工具，结果喂回模型继续下一轮。
	Continue Termination = iota
	// EndWithResult — 工具结果本身就是这一轮的终答。
	EndWithResult
	// EndWithModelContent — turn 到此为止，终答取模型本轮说的话。
	EndWithModelContent
)

// Outcome is what executing one tool produced.
type Outcome struct {
	Result string
	// Status is "success" or anything else (treated as failure).
	// 只有 success 才可能算进展 —— 见 ProgressTracker.Record。
	Status string
	End    Termination
}

const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// ToolFunc executes one tool call. It must not return an error: a failed tool is
// a normal event in a loop (the model gets to see the failure and try something
// else), so failure is carried in Outcome.Status, not out-of-band.
//
// round is 1-based, passed so a host that emits its own events during tool
// execution can label them consistently with the loop's own events.
type ToolFunc func(ctx context.Context, call llm.ToolCall, round int) Outcome

// Observation records one tool execution, for the host to map into its own
// persistence model (Step rows, transcripts, whatever).
type Observation struct {
	// EventID matches the tool_start / tool_result workstream events, so a host
	// that persists steps can pair them with what the UI showed.
	EventID    string
	Round      int
	Call       llm.ToolCall
	Outcome    Outcome
	Progress   bool
	DurationMs int
}

// StopReason says why the loop ended. It is on Result rather than an error
// because none of these are failures — a stalled loop that produced partial
// evidence is a legitimate outcome the host may still want to answer from.
type StopReason string

const (
	// StopAnswered — 模型不再要工具，正常收敛。
	StopAnswered StopReason = "answered"
	// StopTerminalTool — 某个工具声明 turn 到此结束。
	StopTerminalTool StopReason = "terminal_tool"
	// StopNoProgress — 连续若干轮没有新证据，提前止损。
	StopNoProgress StopReason = "no_progress"
	// StopExhausted — 轮次预算用完。
	StopExhausted StopReason = "rounds_exhausted"
)

// Config wires the loop to its host.
type Config struct {
	// Model is required.
	Model ModelFunc
	// Tools may be nil if the host advertises no tools (the loop then runs
	// exactly one round — which is the correct degenerate behaviour, not a bug).
	Tools ToolFunc

	// MaxRounds bounds total model calls. <=0 → DefaultMaxRounds.
	MaxRounds int
	// MaxNoProgressRounds stops the loop after this many consecutive rounds that
	// produced no new evidence. 0 disables the early stop (MaxRounds still caps).
	MaxNoProgressRounds int

	// Emit receives the loop's own events. nil → discarded.
	Emit workstream.Emitter

	// ResultLooksFailed lets a host recognise tools that report failure in-band
	// (Outcome.Status says success, the payload says "ERR_..."). nil → status is
	// the only signal. See ProgressTracker.ResultLooksFailed for why this is a
	// host decision rather than a built-in heuristic.
	//
	// 宿主若已在 ToolFunc 里把内联失败翻译成 Status（推荐做法），这里就该留空。
	ResultLooksFailed func(result string) bool

	// FoldResults builds the next round's input from this round's observations.
	// nil → DefaultFold (results joined, then the original request restated).
	//
	// 收 Observation 而不是纯结果串，是因为宿主折叠时真的需要工具身份：pro 要给每条
	// 结果套「不可信证据」外框（工具返回的文本可能试图改变模型目标），而外框里必须写
	// 工具名。只给 []string 就得让宿主自己在别处偷偷维护一份"这轮调了哪些工具"的
	// 平行数组 —— 那种平行数组必然会和真相错位。
	FoldResults func(round []Observation, original string, roundIndex int) string

	// Fallback gets one last shot at an answer when the loop stops without one
	// (exhausted or stalled): it returns a prompt that will be sent with NO
	// tools. Returning "" skips the attempt. nil → no fallback.
	//
	// 为什么留给宿主：这纯粹是 prompt 措辞，跟收敛无关，而措辞是领域和语种的事。
	Fallback func(observations []Observation, stop StopReason) string
}

// DefaultMaxRounds is deliberately small. A loop that needs many rounds usually
// needs better tools, not a bigger budget — and every extra round is a full
// model call the user waits for.
const DefaultMaxRounds = 5

// Result is what one full loop produced.
type Result struct {
	Content      string
	Rounds       int
	Stop         StopReason
	Observations []Observation
	// FallbackUsed reports whether Content came from the Fallback prompt rather
	// than from the loop proper.
	FallbackUsed bool
}

// Run drives the loop. It returns an error only when the model call itself
// failed — every other ending is a Result with a StopReason.
func Run(ctx context.Context, cfg Config, input string) (*Result, error) {
	if cfg.Model == nil {
		return nil, ErrNoModel
	}
	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	noProgressBudget := cfg.MaxNoProgressRounds
	if noProgressBudget < 0 {
		noProgressBudget = 0
	}
	emit := cfg.Emit
	if emit == nil {
		emit = workstream.Discard()
	}
	fold := cfg.FoldResults
	if fold == nil {
		fold = DefaultFold
	}

	res := &Result{Stop: StopExhausted}
	tracker := NewProgressTracker()
	tracker.ResultLooksFailed = cfg.ResultLooksFailed
	current := input
	toolSeq := 0
	seenEventIDs := make(map[string]struct{})

	for round := 0; round < maxRounds; round++ {
		out, err := cfg.Model(ctx, current, Attempt{Round: round})
		res.Rounds = round + 1
		if err != nil {
			return res, err
		}
		if out == nil {
			return res, fmt.Errorf("agentloop: model returned nil round %d", round+1)
		}

		if len(out.ToolCalls) == 0 || cfg.Tools == nil {
			res.Content = out.Content
			res.Stop = StopAnswered
			return res, nil
		}

		roundObs := make([]Observation, 0, len(out.ToolCalls))
		roundProgress := false

		for _, call := range out.ToolCalls {
			toolSeq++
			// The provider call id is the canonical identity shared by model output,
			// workstream events, observations and transcripts.  Only synthesize an id
			// when the provider omitted one (or emitted an invalid duplicate).
			eventID := strings.TrimSpace(call.ID)
			if _, duplicate := seenEventIDs[eventID]; eventID == "" || duplicate {
				for {
					eventID = fmt.Sprintf("tool-%d", toolSeq)
					if _, exists := seenEventIDs[eventID]; !exists {
						break
					}
					toolSeq++
				}
			}
			seenEventIDs[eventID] = struct{}{}
			// Canonicalize the call itself before any consumer sees it. Events,
			// execution, observations and downstream transcripts must carry the
			// same identity; keeping a synthesized id only in a side variable
			// creates two facts when a provider omits or duplicates ids.
			call.ID = eventID
			name := ToolName(call)

			emit(workstream.ToolStartEvent(eventID, name, rawJSONOrString(ToolArguments(call))).
				WithMeta("round", round+1))

			start := time.Now()
			outcome := cfg.Tools(ctx, call, round+1)
			elapsed := int(time.Since(start).Milliseconds())

			progress := tracker.Record(name, ToolArguments(call), outcome.Result, outcome.Status)
			roundProgress = roundProgress || progress

			emit(workstream.ToolResultEvent(eventID, name, outcome.Result,
				outcome.Status != StatusSuccess, elapsed).
				WithMeta("round", round+1).
				WithMeta("progress", progress))

			obs := Observation{
				EventID:    eventID,
				Round:      round + 1,
				Call:       call,
				Outcome:    outcome,
				Progress:   progress,
				DurationMs: elapsed,
			}
			res.Observations = append(res.Observations, obs)

			switch outcome.End {
			case EndWithResult:
				res.Content = outcome.Result
				res.Stop = StopTerminalTool
				return res, nil
			case EndWithModelContent:
				res.Content = out.Content
				res.Stop = StopTerminalTool
				return res, nil
			}

			roundObs = append(roundObs, obs)
		}

		if roundProgress {
			tracker.MarkProgressRound()
		} else {
			n := tracker.MarkNoProgressRound()
			emit(workstream.StatusEvent("tool_loop_no_progress").
				WithMeta("round", round+1).
				WithMeta("no_progress_rounds", n).
				WithMeta("max_no_progress_rounds", noProgressBudget).
				WithMeta("tool_call_count", len(out.ToolCalls)))
			if tracker.ShouldStop(noProgressBudget) {
				res.Stop = StopNoProgress
				break
			}
		}

		if len(roundObs) > 0 {
			current = fold(roundObs, input, round)
		}
	}

	if cfg.Fallback != nil {
		if prompt := cfg.Fallback(res.Observations, res.Stop); strings.TrimSpace(prompt) != "" {
			if out, err := cfg.Model(ctx, prompt, Attempt{Round: res.Rounds, Fallback: true}); err == nil && out != nil &&
				strings.TrimSpace(out.Content) != "" {
				res.Content = out.Content
				res.FallbackUsed = true
			}
		}
	}
	return res, nil
}

// DefaultFold joins this round's tool results and restates the original request,
// so a long tool trace cannot push the actual question out of the model's focus.
func DefaultFold(round []Observation, original string, _ int) string {
	parts := make([]string, 0, len(round))
	for _, o := range round {
		parts = append(parts, o.Outcome.Result)
	}
	joined := strings.Join(parts, "\n\n")
	if strings.TrimSpace(original) == "" {
		return joined
	}
	return fmt.Sprintf("%s\nOriginal request: %s", joined, original)
}

// Call builds a kit/llm ToolCall without making hosts repeat the wire defaults.
func Call(id, name, arguments string) llm.ToolCall {
	c := llm.ToolCall{ID: id, Type: "function"}
	c.Function.Name = name
	c.Function.Arguments = arguments
	return c
}

// ToolName reads the tool name off a kit/llm ToolCall. The nested anonymous
// struct is awkward to reach through at every call site; this keeps that
// awkwardness in one place instead of spreading it.
func ToolName(call llm.ToolCall) string { return call.Function.Name }

// ToolArguments reads the raw argument JSON off a kit/llm ToolCall.
func ToolArguments(call llm.ToolCall) string { return call.Function.Arguments }

// rawJSONOrString returns args as raw JSON when it parses, else as a JSON string
// — so a provider emitting malformed arguments produces a renderable event
// rather than a broken SSE frame.
func rawJSONOrString(args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return nil
	}
	return encoded
}
