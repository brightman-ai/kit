// Package log provides structured JSON logging with trace context support.
//
// adr013_contract_test.go: ADR-013 可观测性框架契约验证
//
// ADR-013 规范 (04_decisions.md:911-1078):
//   - Deep-Debug: 结构化 JSON 日志 + TID/Span/STG
//   - Deep-Ensure: 状态机断言 + 契约验证
//   - Deep-Metrics: 业务 RED + 中台 USE 度量
//   - Deep-AI-Metrics: Token 经济 + 质量评估
//
// 日志三要素:
//   - INFO:  {what}
//   - WARN:  {what} | reason: {reason} | will: {impact}
//   - ERROR: {what} | reason: {reason} | will: {impact} | howto: {fix}
package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brightman-ai/kit/contextx"
)

// TestADR013_LogEntryJSONSchema 验证日志 Entry 结构符合 ADR-013 规范
func TestADR013_LogEntryJSONSchema(t *testing.T) {
	requiredFields := []string{"l", "t", "p", "g", "msg"}
	optionalFields := []string{"tid", "span", "pspan", "stg", "mod", "ev", "f", "ec", "dur"}

	entry := Entry{
		L:   "INFO",
		T:   "20260131120000.000",
		P:   12345,
		G:   1,
		Msg: "test message",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Entry marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Entry unmarshal failed: %v", err)
	}

	// 验证必需字段存在
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("ADR-013 契约违反: 必需字段 '%s' 缺失", field)
		}
	}

	// 验证可选字段不会出现空值 (omitempty)
	for _, field := range optionalFields {
		if val, ok := parsed[field]; ok {
			if val == "" || val == 0 || val == nil {
				t.Errorf("ADR-013 契约违反: 可选字段 '%s' 应该 omitempty，但出现了空值", field)
			}
		}
	}
}

// TestADR013_LogLevelStrings 验证日志级别字符串符合规范
func TestADR013_LogLevelStrings(t *testing.T) {
	expected := map[Level]string{
		LevelDebug: "DEBUG",
		LevelInfo:  "INFO",
		LevelWarn:  "WARN",
		LevelError: "ERROR",
	}

	for level, expectedStr := range expected {
		if level.String() != expectedStr {
			t.Errorf("ADR-013 契约违反: Level %d 应为 '%s'，实际为 '%s'",
				level, expectedStr, level.String())
		}
	}
}

// TestADR013_LogTimeFormat 验证时间格式符合规范 (YYYYMMDDHHmmss.SSS)
func TestADR013_LogTimeFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := &Logger{
		mod:    "test",
		level:  LevelDebug,
		output: &buf,
	}

	logger.Info("test")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Log parse failed: %v", err)
	}

	// 格式: YYYYMMDDHHmmss.SSS (18 chars)
	if len(entry.T) != 18 {
		t.Errorf("ADR-013 契约违反: 时间格式应为 YYYYMMDDHHmmss.SSS (18字符)，实际: %s (%d字符)",
			entry.T, len(entry.T))
	}

	// 验证包含小数点
	if !strings.Contains(entry.T, ".") {
		t.Errorf("ADR-013 契约违反: 时间格式应包含毫秒分隔符 '.'，实际: %s", entry.T)
	}
}

// TestADR013_ContextPropagation 验证上下文传播符合规范
func TestADR013_ContextPropagation(t *testing.T) {
	var buf bytes.Buffer
	logger := &Logger{
		mod:    "test",
		level:  LevelDebug,
		output: &buf,
	}

	// 设置上下文
	contextx.SetTID("http/req-001")
	contextx.SetStage("chat/intent")
	contextx.StartSpan("parse")
	defer contextx.EndSpan()
	defer contextx.Clear()

	logger.Info("test with context")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Log parse failed: %v", err)
	}

	// 验证 TID 传播
	if entry.TID != "http/req-001" {
		t.Errorf("ADR-013 契约违反: TID 未正确传播，期望 'http/req-001'，实际 '%s'", entry.TID)
	}

	// 验证 STG 传播
	if entry.STG != "chat/intent" {
		t.Errorf("ADR-013 契约违反: STG 未正确传播，期望 'chat/intent'，实际 '%s'", entry.STG)
	}

	// 验证 Span 传播
	if entry.Span == "" {
		t.Error("ADR-013 契约违反: Span 未正确传播")
	}
}

// TestADR013_STGBusinessFlowFirst 验证 STG 命名遵循业务流优先原则
func TestADR013_STGBusinessFlowFirst(t *testing.T) {
	// ADR-013 规范:
	// ✅ 正确: stg = "chat/intent"
	// ❌ 错误: stg = "http/handler/chat/intent"

	validSTGs := []string{
		"chat/intent",
		"chat/respond",
		"workforce/create",
		"skill/browser",
		"memory/search",
		"session/create",
		"thinkflow/execute",
		"thinkflow/plan",
	}

	invalidSTGs := []string{
		"http/handler/chat/intent",  // http 在前
		"grpc/service/chat/intent",  // grpc 在前
		"websocket/handler/chat",    // websocket 在前
	}

	// 这些应该是有效的 STG
	for _, stg := range validSTGs {
		if strings.HasPrefix(stg, "http/") ||
			strings.HasPrefix(stg, "grpc/") ||
			strings.HasPrefix(stg, "websocket/") {
			t.Errorf("ADR-013 契约违反: STG '%s' 不应以基础设施前缀开头", stg)
		}
	}

	// 这些应该被标记为无效
	for _, stg := range invalidSTGs {
		if !strings.HasPrefix(stg, "http/") &&
			!strings.HasPrefix(stg, "grpc/") &&
			!strings.HasPrefix(stg, "websocket/") {
			t.Errorf("测试数据错误: '%s' 应该是无效的 STG", stg)
		}
	}
}

// TestADR013_ErrorLogHasLocation 验证 WARN/ERROR 日志包含源码位置
func TestADR013_ErrorLogHasLocation(t *testing.T) {
	var buf bytes.Buffer
	logger := &Logger{
		mod:    "test",
		level:  LevelDebug,
		output: &buf,
	}

	// WARN 日志
	buf.Reset()
	logger.Warn("test warning | reason: test | will: nothing")
	var warnEntry Entry
	if err := json.Unmarshal(buf.Bytes(), &warnEntry); err != nil {
		t.Fatalf("WARN log parse failed: %v", err)
	}
	if warnEntry.F == "" {
		t.Error("ADR-013 契约违反: WARN 日志应包含源码位置 (f 字段)")
	}

	// ERROR 日志
	buf.Reset()
	logger.Error("test error | reason: test | will: fail | howto: fix it")
	var errorEntry Entry
	if err := json.Unmarshal(buf.Bytes(), &errorEntry); err != nil {
		t.Fatalf("ERROR log parse failed: %v", err)
	}
	if errorEntry.F == "" {
		t.Error("ADR-013 契约违反: ERROR 日志应包含源码位置 (f 字段)")
	}
}

// TestADR013_ErrorCodeInErrorLog 验证 ERROR 日志可以包含错误码
func TestADR013_ErrorCodeInErrorLog(t *testing.T) {
	var buf bytes.Buffer
	logger := &Logger{
		mod:    "test",
		level:  LevelDebug,
		output: &buf,
	}

	logger.WithErrorCode(1001).Error("test error with code")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Log parse failed: %v", err)
	}

	if entry.EC != 1001 {
		t.Errorf("ADR-013 契约违反: 错误码未正确记录，期望 1001，实际 %d", entry.EC)
	}
}

// TestADR013_ModuleLogger 验证模块日志器正确设置模块名
func TestADR013_ModuleLogger(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	defer SetOutput(nil)

	logger := Module("desktop")
	logger.Info("test module log")

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Log parse failed: %v", err)
	}

	if entry.Mod != "desktop" {
		t.Errorf("ADR-013 契约违反: 模块名未正确设置，期望 'desktop'，实际 '%s'", entry.Mod)
	}
}

// TestADR013_SensitiveDataMasking 验证敏感数据脱敏功能
func TestADR013_SensitiveDataMasking(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maskFunc func(string) string
		contains string
		notContains string
	}{
		{
			name:     "MaskAPIKey",
			input:    "sk-1234567890abcdef",
			maskFunc: MaskAPIKey,
			contains: "sk-123",
			notContains: "1234567890abcdef",
		},
		{
			name:     "MaskPassword",
			input:    "mySecretPassword123",
			maskFunc: MaskPassword,
			contains: "***",
			notContains: "mySecretPassword",
		},
		{
			name:     "MaskEmail",
			input:    "user@example.com",
			maskFunc: MaskEmail,
			contains: "@example.com",
			notContains: "user@",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := tt.maskFunc(tt.input)
			if !strings.Contains(masked, tt.contains) {
				t.Errorf("ADR-013 契约违反: %s 结果应包含 '%s'，实际 '%s'",
					tt.name, tt.contains, masked)
			}
			if tt.notContains != "" && strings.Contains(masked, tt.notContains) {
				t.Errorf("ADR-013 契约违反: %s 结果不应包含敏感数据 '%s'，实际 '%s'",
					tt.name, tt.notContains, masked)
			}
		})
	}
}
