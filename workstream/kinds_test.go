package workstream

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
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
