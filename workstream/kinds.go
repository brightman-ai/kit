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

// AllTaskStatuses returns every TaskItem.Status value this package defines, in
// lifecycle order (未开始 → 进行中 → 三种终态).
//
// 与 AllKinds() 同一个理由：枚举必须有一个**程序读得到**的答案，否则消费端只能靠人肉
// grep 对齐，而人肉对齐必然漂移。两道闸：kinds_test.go 扫 event.go 的常量声明与本列表
// 比对（漏登记就红）；Manifest() 把它导出给跨语言消费端断言（例如 TypeScript 前端可以
// 用一个跨语言对账测试读本包源码，核对它那份联合类型与这里一致）。
//
// 顺序是语义（生命周期），不排序 —— 与 AllKinds() 的字典序不同，那里顺序无意义。
func AllTaskStatuses() []string {
	return []string{
		TaskStatusPending,
		TaskStatusInProgress,
		TaskStatusCompleted,
		TaskStatusFailed,
		TaskStatusCancelled,
	}
}

// TaskStatusTerminal reports whether a step will never change again.
//
// 三种终态语义不同（成 / 砸了 / 没跑），但"不会再动了"这件事是同一个判断，值得有唯一
// 出处：生产方据它决定还要不要再发快照，消费端据它给那一行收尾（停掉转圈）。
func TaskStatusTerminal(status string) bool {
	switch status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// KnownTaskStatus reports whether status is a registered TaskItem.Status value.
// 与 Known 同理：区分"我还没实现的档"(已登记 = 消费端的 bug) 与"线上的垃圾"(未登记 =
// 生产方 bug 或版本错位)。
func KnownTaskStatus(status string) bool {
	for _, s := range AllTaskStatuses() {
		if s == status {
			return true
		}
	}
	return false
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
	// task_statuses 是**新增**键，老消费端只读 "kinds"，不受影响。
	return map[string]any{
		"kinds":         names,
		"task_statuses": AllTaskStatuses(),
	}
}
