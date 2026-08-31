package workstream

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestAllKindsCoversEveryDeclaredConstant — 加了 Kind 常量却忘了登记 → 本测试红。
//
// 这是防协议漂移的**第一道闸**，也是唯一一道能在 Go 侧自动生效的。它解析 event.go 的
// AST 取出所有 `X Kind = "..."` 声明，与 AllKinds() 比对。没有它，AllKinds() 只是一份
// 会过期的手抄清单 —— 而过期的清单比没有清单更糟，因为消费端会信它。
func TestAllKindsCoversEveryDeclaredConstant(t *testing.T) {
	declared := parseDeclaredKinds(t)
	if len(declared) == 0 {
		t.Fatal("event.go 里一个 Kind 常量都没解析到 —— 解析逻辑坏了，不是真的没有")
	}

	registered := map[Kind]bool{}
	for _, k := range AllKinds() {
		registered[k] = true
	}

	for name, kind := range declared {
		if !registered[kind] {
			t.Errorf("Kind %s (%q) 已声明但没登记进 AllKinds() —— "+
				"消费端拿不到它，事件会被静默丢弃", name, kind)
		}
	}
	for kind := range registered {
		found := false
		for _, d := range declared {
			if d == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllKinds() 登记了 %q 但 event.go 里没有对应常量声明", kind)
		}
	}
}

// TestKnownRejectsUnregistered — Known() 必须真的能判否，否则消费端用它做的
// "未知事件告警" 就是个永远不响的假报警器。
func TestKnownRejectsUnregistered(t *testing.T) {
	if !Known(ToolStart) {
		t.Error("已注册的 Kind 应判 true")
	}
	if Known(Kind("tool_call")) {
		t.Error("tool_call 从未在 Go 侧声明过，必须判 false")
	}
	if Known(Kind("")) {
		t.Error("空 Kind 必须判 false")
	}
}

// TestManifestMatchesAllKinds — Manifest 是跨语言契约测试的数据源，
// 它和 AllKinds() 必须同源，不能各自维护。
func TestManifestMatchesAllKinds(t *testing.T) {
	names, ok := Manifest()["kinds"].([]string)
	if !ok {
		t.Fatal("Manifest()[\"kinds\"] 必须是 []string")
	}
	all := AllKinds()
	if len(names) != len(all) {
		t.Fatalf("Manifest 有 %d 个 Kind, AllKinds 有 %d 个", len(names), len(all))
	}
	for i, n := range names {
		if n != string(all[i]) {
			t.Errorf("第 %d 个不一致: manifest=%q allKinds=%q", i, n, all[i])
		}
	}
}

// TestAllTaskStatusesCoversEveryDeclaredConstant — 与 Kind 同一道闸，同一个理由。
// 加了 TaskStatus* 常量却没登记进 AllTaskStatuses() → 红。没有它，那个列表就只是一份
// 会过期的手抄清单，而跨语言消费端正是照着它对账的。
func TestAllTaskStatusesCoversEveryDeclaredConstant(t *testing.T) {
	declared := parseDeclaredTaskStatuses(t)
	if len(declared) == 0 {
		t.Fatal("event.go 里一个 TaskStatus 常量都没解析到 —— 解析逻辑坏了，不是真的没有")
	}

	registered := map[string]bool{}
	for _, s := range AllTaskStatuses() {
		registered[s] = true
	}

	for name, status := range declared {
		if !registered[status] {
			t.Errorf("TaskStatus %s (%q) 已声明但没登记进 AllTaskStatuses() —— "+
				"消费端不认得它，那一步会被画成「还没开始」", name, status)
		}
	}
	for status := range registered {
		found := false
		for _, d := range declared {
			if d == status {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllTaskStatuses() 登记了 %q 但 event.go 里没有对应常量声明", status)
		}
	}
}

// TestTaskStatusBackwardCompatible — 老三档的**取值**是线上协议的一部分：改名会让所有
// 在跑的生产者/消费端瞬间对不上。这道闸让"顺手改个更好听的名字"必须先看见代价。
func TestTaskStatusBackwardCompatible(t *testing.T) {
	for _, legacy := range []string{"pending", "in_progress", "completed"} {
		if !KnownTaskStatus(legacy) {
			t.Errorf("老取值 %q 必须继续被认得（改它 = 打断所有在跑的生产者）", legacy)
		}
	}
	if TaskStatusPending != "pending" || TaskStatusInProgress != "in_progress" || TaskStatusCompleted != "completed" {
		t.Error("老三档的字面取值不得变更")
	}
}

// TestTaskStatusTerminal — 三种终态语义不同，但"不会再动了"必须是同一个判断。
func TestTaskStatusTerminal(t *testing.T) {
	for _, s := range []string{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled} {
		if !TaskStatusTerminal(s) {
			t.Errorf("%q 是终态", s)
		}
	}
	for _, s := range []string{TaskStatusPending, TaskStatusInProgress, "", "weird"} {
		if TaskStatusTerminal(s) {
			t.Errorf("%q 不是终态", s)
		}
	}
}

// TestKnownTaskStatusRejectsUnregistered — 同 Known()：判否得真的能判否。
func TestKnownTaskStatusRejectsUnregistered(t *testing.T) {
	if !KnownTaskStatus(TaskStatusFailed) {
		t.Error("已登记的状态应判 true")
	}
	if KnownTaskStatus("done") || KnownTaskStatus("") {
		t.Error("未登记取值必须判 false")
	}
}

// TestManifestCarriesTaskStatuses — Manifest 是跨语言契约的数据源；新加的键必须与
// AllTaskStatuses() 同源，且**不能**动老的 "kinds" 键（老消费端只读它）。
func TestManifestCarriesTaskStatuses(t *testing.T) {
	m := Manifest()
	if _, ok := m["kinds"].([]string); !ok {
		t.Fatal("老键 \"kinds\" 必须原样保留 —— 现有消费端读的就是它")
	}
	statuses, ok := m["task_statuses"].([]string)
	if !ok {
		t.Fatal("Manifest()[\"task_statuses\"] 必须是 []string")
	}
	all := AllTaskStatuses()
	if len(statuses) != len(all) {
		t.Fatalf("Manifest 有 %d 档, AllTaskStatuses 有 %d 档", len(statuses), len(all))
	}
	for i, s := range statuses {
		if s != all[i] {
			t.Errorf("第 %d 档不一致: manifest=%q allTaskStatuses=%q", i, s, all[i])
		}
	}
}

// parseDeclaredTaskStatuses 从 event.go 的 AST 取出所有 `TaskStatusX = "value"` 常量。
// 这些是**无类型**字符串常量（故意的：要能直接赋给 TaskItem.Status 这个 string 字段），
// 所以认不了类型名，改按名字前缀认 —— 前缀就是这批常量的显式契约。
func parseDeclaredTaskStatuses(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "event.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 event.go 失败: %v", err)
	}

	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "TaskStatus") {
					continue
				}
				// 名字对上了就**必须**解析成功。原来这里对"带显式类型/非字面量/解析失败"
				// 一律 continue —— 那等于：写法一变，这一档就被静默漏掉，闸还是绿的
				// （codex 评审点出的假闸）。认不出就直接失败，让人来看，不要假装没看见。
				if vs.Type != nil {
					t.Fatalf("常量 %s 带了显式类型 %v —— 本解析器只认无类型字符串常量；"+
						"若确实要改写法，请同步改这里的解析，不要让它被静默跳过", name.Name, vs.Type)
				}
				if i >= len(vs.Values) {
					t.Fatalf("常量 %s 没有对应的值表达式（可能用了 iota 或续行省略写法）", name.Name)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("常量 %s 的值不是字符串字面量（%T）—— 枚举取值必须是可静态核对的字面量",
						name.Name, vs.Values[i])
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("常量 %s 的字面量无法解析: %v", name.Name, err)
				}
				out[name.Name] = val
			}
		}
	}
	return out
}

// parseDeclaredKinds 从 event.go 的 AST 取出所有 `Name Kind = "value"` 常量。
// 用 AST 而不是正则：常量块的排版随时会变，正则会静默漏掉，AST 不会。
func parseDeclaredKinds(t *testing.T) map[string]Kind {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "event.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 event.go 失败: %v", err)
	}

	out := map[string]Kind{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// 只认显式写了 `Kind` 类型的那些（常量块里可能混别的类型）。
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "Kind" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[name.Name] = Kind(val)
			}
		}
	}
	return out
}
