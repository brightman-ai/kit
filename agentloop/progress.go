package agentloop

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// ProgressTracker answers the one question a tool loop must answer to terminate:
// **这一轮之后，我比之前多知道了什么吗？**
//
// 不是"模型有没有调工具"（它可以永远调下去），也不是"工具有没有成功"（同一次成功
// 的调用重复一百遍仍然是零新增信息）。判据是**有没有拿到没见过的证据**：
// 工具名 + 规范化参数 + 结果 的摘要没出现过，才算进展。
//
// 三条规则各自挡一种真实的绕圈：
//   - 同名同参同结果 → 不算。模型卡住时最常见的形态就是复读同一次调用。
//   - 参数 JSON 键序不同但语义相同 → 不算。序列化顺序不该被当成新信息。
//   - 失败 → 不算。失败不是证据，且反复失败正是最该止损的情形。
//
// 零值不可用，用 NewProgressTracker。nil receiver 是安全的（一律判无进展）。
type ProgressTracker struct {
	seen             map[string]struct{}
	noProgressRounds int

	// ResultLooksFailed lets a host recognise tools that report failure in-band
	// (status says success, the payload says "ERR_..."). nil → status is the only
	// signal, which is the safe default.
	//
	// 为什么是可注入策略而不是写死的启发式：识别"文本里的失败"必然要猜措辞，而猜错
	// 的代价是**把真进展误判成没进展**，进而提前止损、答案变浅 —— 且不留任何痕迹。
	// 一个工具返回的正文里恰好出现 "failed:" 就被判定无进展，这在读知识库/读文档类
	// 工具上是必然会发生的，不是假想。所以措辞由知道自己工具长什么样的宿主给，
	// 这层只提供机制。
	ResultLooksFailed func(result string) bool
}

// NewProgressTracker returns a tracker with status as the only failure signal.
func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{seen: make(map[string]struct{})}
}

// Record reports whether this tool execution added new evidence.
func (t *ProgressTracker) Record(name, arguments, result, status string) bool {
	if t == nil {
		return false
	}
	if strings.TrimSpace(status) != StatusSuccess || strings.TrimSpace(result) == "" {
		return false
	}
	if t.ResultLooksFailed != nil && t.ResultLooksFailed(result) {
		return false
	}
	digest := ProgressDigest(name, arguments, result)
	if digest == "" {
		return false
	}
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	if _, ok := t.seen[digest]; ok {
		return false
	}
	t.seen[digest] = struct{}{}
	return true
}

// MarkNoProgressRound records a round that produced nothing new, returning the
// current consecutive count.
func (t *ProgressTracker) MarkNoProgressRound() int {
	if t == nil {
		return 0
	}
	t.noProgressRounds++
	return t.noProgressRounds
}

// MarkProgressRound resets the consecutive no-progress budget.
func (t *ProgressTracker) MarkProgressRound() {
	if t != nil {
		t.noProgressRounds = 0
	}
}

// ShouldStop reports whether the consecutive no-progress budget is exhausted.
// budget <= 0 disables the early stop.
func (t *ProgressTracker) ShouldStop(budget int) bool {
	return t != nil && budget > 0 && t.noProgressRounds >= budget
}

// maxProgressDigestInputChars bounds hashing cost on very large tool payloads.
// 截断只影响"多长算同一份证据"，不影响正确性：两份前 8192 字符相同的结果被当成
// 同一份，代价是极偶尔少算一次进展 —— 远好过对着 MB 级正文反复算哈希。
const maxProgressDigestInputChars = 8192

// ProgressDigest is the identity of one observation: same digest = same evidence.
func ProgressDigest(name, arguments, result string) string {
	normalized := strings.Join(strings.Fields(CanonicalArguments(arguments)+"\n"+result), " ")
	if normalized == "" {
		return ""
	}
	runes := []rune(normalized)
	if len(runes) > maxProgressDigestInputChars {
		normalized = string(runes[:maxProgressDigestInputChars])
	}
	sum := sha256.Sum256([]byte(name + "\x00" + normalized))
	return fmt.Sprintf("%x", sum[:])
}

// CanonicalArguments re-encodes JSON arguments so key order does not affect
// identity. Non-JSON arguments are returned trimmed, unchanged.
func CanonicalArguments(arguments string) string {
	raw := strings.TrimSpace(arguments)
	if raw == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return raw
	}
	return string(encoded)
}
