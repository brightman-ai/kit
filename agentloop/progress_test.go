package agentloop

import (
	"strings"
	"testing"
)

// 本文件的前三条是从 deepwork-pro internal/conversation/tool_loop_guard_test.go
// 原样搬来的行为契约 —— 它们钉住的是"什么算进展"，那正是这次提取要保住的东西。
// 第四条是提取时新增的：把内联失败识别从写死规则降成可注入策略，必须有测试
// 证明"不注入时不会误伤"，否则这个降级就只是换了个地方继续误伤。

func TestProgressTracksEvidenceNotRepeatedCalls(t *testing.T) {
	tracker := NewProgressTracker()

	if !tracker.Record("browser_search", `{"query":"agentic memory"}`, "搜索结果 URL:\n1. A https://example.com/a", StatusSuccess) {
		t.Fatal("first successful evidence must count as progress")
	}
	if tracker.Record("browser_search", `{"query":"agentic memory"}`, "搜索结果 URL:\n1. A https://example.com/a", StatusSuccess) {
		t.Fatal("identical observation should not count as new progress")
	}
	if !tracker.Record("browser_search", `{"query":"agentic memory"}`, "搜索结果 URL:\n1. B https://example.com/b", StatusSuccess) {
		t.Fatal("same tool with new evidence must count as progress")
	}
	if tracker.Record("browser_search", `{"query":"agentic memory"}`, "ERR_BROWSER_SEARCH: failed", StatusError) {
		t.Fatal("error result must not count as progress")
	}
}

func TestProgressIncludesCanonicalArguments(t *testing.T) {
	tracker := NewProgressTracker()
	if !tracker.Record("write_file", `{"path":"a.md","content":"one"}`, "file written: a.md", StatusSuccess) {
		t.Fatal("first write should count as progress")
	}
	if tracker.Record("write_file", `{"content":"one","path":"a.md"}`, "file written: a.md", StatusSuccess) {
		t.Fatal("equivalent args and result should not count as new progress")
	}
	if !tracker.Record("write_file", `{"path":"a.md","content":"two"}`, "file written: a.md", StatusSuccess) {
		t.Fatal("same tool/result with different args should count as new progress")
	}
}

func TestNoProgressBudgetIsRoundBasedAndConfigurable(t *testing.T) {
	tracker := NewProgressTracker()
	if tracker.MarkNoProgressRound() != 1 {
		t.Fatal("first no-progress round not tracked")
	}
	if tracker.ShouldStop(2) {
		t.Fatal("budget should allow one no-progress round")
	}
	if tracker.MarkNoProgressRound() != 2 {
		t.Fatal("second no-progress round not tracked")
	}
	if !tracker.ShouldStop(2) {
		t.Fatal("budget should stop after configured consecutive no-progress rounds")
	}
	tracker.MarkProgressRound()
	if tracker.ShouldStop(2) {
		t.Fatal("progress must reset no-progress budget")
	}
	if tracker.MarkNoProgressRound() != 1 {
		t.Fatal("no-progress budget should restart after progress")
	}
	if tracker.ShouldStop(0) {
		t.Fatal("zero budget disables early no-progress stop")
	}
}

// TestStatusIsAuthoritativeWithoutHostHeuristic — 默认不做内联嗅探。
//
// 这条钉的是提取时刻意改掉的行为：pro 的原实现无条件跑
// `strings.Contains(result, " failed:")`，于是任何正文里出现这个子串的成功结果
// 都被判成"无进展"。读知识库/读文档类工具返回的正文里出现它是必然的 ——
// 后果是提前止损、答案变浅，且**不报错、不留痕**。默认必须只信 status。
func TestStatusIsAuthoritativeWithoutHostHeuristic(t *testing.T) {
	tracker := NewProgressTracker()
	doc := "决策台账\n- 2026-07-11 部署 failed: 复盘结论见下\n- ERR_ 这三个字母出现在正文里"
	if !tracker.Record("read_tree", `{"root":271}`, doc, StatusSuccess) {
		t.Fatal("status=success 的正文里含 'failed:' / 'ERR_' 不该被判成无进展 —— " +
			"默认只信 status，内联嗅探必须由宿主显式注入")
	}
}

// TestHostHeuristicCanRecogniseInBandFailure — 注入后要真的生效，
// 否则 pro 迁过来就悄悄丢了它原有的一层防护。
func TestHostHeuristicCanRecogniseInBandFailure(t *testing.T) {
	tracker := NewProgressTracker()
	tracker.ResultLooksFailed = func(result string) bool {
		return strings.HasPrefix(strings.TrimSpace(result), "ERR_")
	}
	if tracker.Record("browser_search", `{"q":"x"}`, "ERR_TIMEOUT: upstream gone", StatusSuccess) {
		t.Fatal("宿主注入的内联失败识别必须生效")
	}
	if !tracker.Record("browser_search", `{"q":"x"}`, "正常结果", StatusSuccess) {
		t.Fatal("正常结果仍应算进展")
	}
}

func TestEmptyResultIsNotProgress(t *testing.T) {
	tracker := NewProgressTracker()
	if tracker.Record("noop", `{}`, "   ", StatusSuccess) {
		t.Fatal("空结果不是证据")
	}
}

func TestNilTrackerIsSafe(t *testing.T) {
	var tracker *ProgressTracker
	if tracker.Record("x", "{}", "y", StatusSuccess) {
		t.Fatal("nil tracker 必须一律判无进展")
	}
	if tracker.MarkNoProgressRound() != 0 {
		t.Fatal("nil tracker MarkNoProgressRound 应返回 0")
	}
	if tracker.ShouldStop(1) {
		t.Fatal("nil tracker 不该要求停机")
	}
	tracker.MarkProgressRound() // 不得 panic
}

func TestCanonicalArgumentsPassesThroughNonJSON(t *testing.T) {
	if got := CanonicalArguments("  not json  "); got != "not json" {
		t.Fatalf("非 JSON 参数应原样 trim 返回, got %q", got)
	}
	if got := CanonicalArguments("   "); got != "" {
		t.Fatalf("空参数应返回空, got %q", got)
	}
}
