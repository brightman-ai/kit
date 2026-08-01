package workstream

import "sort"

// AllKinds returns every Kind this package can emit, sorted.
//
// 这是 workstream 协议的 SSOT 出口。Kind 常量本身散在 event.go 里读得舒服，但
// "到底有哪些 Kind" 必须有一个可被程序读到的答案 —— 否则消费端（Portal 的 reducer、
// 落库的投影、转写器）只能靠人肉 grep 对齐，而人肉对齐必然漂移：
// 后端加一个 Kind、前端 reducer 没加 case，事件就被静默丢进 default 分支，
// **不报错、不留痕、没人知道**。
//
// 两道闸把这件事钉死：
//   - kinds_test.go 扫 event.go 的常量声明与本列表比对 —— 加了 Kind 不登记就红。
//   - Manifest() 把列表导出成消费端可断言的数据 —— 前端契约测试读它。
func AllKinds() []Kind {
	kinds := []Kind{
		Status, Text, Thinking,
		SkillStart, SkillResult,
		ToolStart, ToolResult,
		TaskUpdate,
		ArtifactStart, ArtifactDelta, ArtifactDone,
		ContextStart, ContextDone,
		ProjectionStart, ProjectionDone,
		PermissionRequest, PermissionResolved,
		Usage, Done, Error, Raw, Meta,
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// Known reports whether k is a registered Kind. Consumers that must not silently
// drop unknown events use this to tell "a Kind I haven't implemented yet"
// (registered but unhandled — a bug in the consumer) apart from "garbage on the
// wire" (unregistered — a bug in the producer or a version skew).
func Known(k Kind) bool {
	for _, known := range AllKinds() {
		if known == k {
			return true
		}
	}
	return false
}

// Manifest is the machine-readable protocol surface, for cross-language contract
// tests (the Portal reducer is written in TypeScript and cannot import Go).
func Manifest() map[string]any {
	kinds := AllKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return map[string]any{"kinds": names}
}
