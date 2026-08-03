package transcript

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNativeSchemaRoundTrip is the round-trip proof that one schema now serves
// both call sites (kills historical drift #1): build a NativeEntry the way
// pkg/worktranscript's Recorder would (an assistant turn carrying
// thinking + tool_use + tool_result + usage), marshal it with encoding/json —
// exactly what the writer does — write it into a dw-<id>.jsonl, then feed it
// through this package's own reader (DeepworkSource) and assert every block
// round-trips. If the write and read shapes ever diverge again (e.g. a tag
// edited on one side only), this test is where it breaks.
func TestNativeSchemaRoundTrip(t *testing.T) {
	in, out, cacheRead := 120, 42, 10
	assistant := NativeEntry{
		Format:    "deepwork.native_transcript.v1.4",
		Type:      "assistant",
		SessionID: "dw-900",
		Timestamp: "2026-06-17T01:00:05Z",
		Message: &NativeMessage{
			Role:  "assistant",
			Model: "claude-opus-4-8",
			Content: []NativeContentBlock{
				{Type: "thinking", Thinking: "let me reason"},
				{Type: "tool_use", ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
				{Type: "tool_result", ToolUseID: "t1", Content: json.RawMessage(`[{"type":"text","text":"file.txt"}]`)},
				{Type: "text", Text: "here is the answer"},
			},
			Usage: &NativeUsage{
				InputTokens:     &in,
				OutputTokens:    &out,
				CacheReadTokens: &cacheRead,
			},
			Invocation: &NativeInvocationIdentity{
				RuntimeID: "whale-agent", ProviderAccountID: "account-a", ProviderKey: "deepseek",
				RequestedModel: "deepseek-v4-pro", ResolvedModel: "deepseek-v4-pro",
				ResolvedSource: "provider_response", CatalogRevision: "catalog-r7", ConfigRevision: "config-r7",
			},
			AuxiliaryInvocations: []NativeInvocationAttempt{{
				ID: "inv-vision", Purpose: "vision_assist", Status: "success", Output: "a whale", DurationMs: 321,
				Invocation: NativeInvocationIdentity{RuntimeID: "whale-agent", ProviderAccountID: "account-a", RequestedModel: "vision-model"},
			}},
			Status: "success",
		},
	}
	assistantLine, err := json.Marshal(assistant)
	if err != nil {
		t.Fatalf("marshal NativeEntry: %v", err)
	}
	var wireCopy NativeEntry
	if err := json.Unmarshal(assistantLine, &wireCopy); err != nil {
		t.Fatalf("unmarshal NativeEntry: %v", err)
	}
	if wireCopy.Message == nil || wireCopy.Message.Invocation == nil ||
		wireCopy.Message.Invocation.ConfigRevision != "config-r7" ||
		len(wireCopy.Message.AuxiliaryInvocations) != 1 ||
		wireCopy.Message.AuxiliaryInvocations[0].ID != "inv-vision" ||
		wireCopy.Message.AuxiliaryInvocations[0].DurationMs != 321 {
		t.Fatalf("invocation facts did not round-trip: %#v", wireCopy.Message)
	}

	userLine, err := json.Marshal(NativeEntry{
		Format:    "deepwork.native_transcript.v1.1",
		Type:      "user",
		SessionID: "dw-900",
		Timestamp: "2026-06-17T01:00:00Z",
		Message: &NativeMessage{
			Role:    "user",
			Content: []NativeContentBlock{{Type: "text", Text: "explain"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal user NativeEntry: %v", err)
	}
	resultLine, err := json.Marshal(NativeEntry{
		Format: "deepwork.native_transcript.v1.1", Type: "result",
		SessionID: "dw-900", Timestamp: "2026-06-17T01:00:06Z", Subtype: "success",
	})
	if err != nil {
		t.Fatalf("marshal result NativeEntry: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "dw-900.jsonl")
	content := append(append(userLine, '\n'), append(assistantLine, '\n')...)
	content = append(content, append(resultLine, '\n')...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	src := NewDeepworkSourceWithDir(nil, 1, dir)
	tr, err := src.LoadTranscript(context.Background(), SessionRef{ID: "900"})
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	if len(tr.Turns) != 2 {
		t.Fatalf("want 2 turns (user+assistant), got %d: %+v", len(tr.Turns), tr.Turns)
	}
	if tr.Turns[0].Role != "user" || tr.Turns[0].Blocks[0].Type != BlockUser || tr.Turns[0].Blocks[0].Text != "explain" {
		t.Fatalf("user turn malformed: %+v", tr.Turns[0])
	}

	asst := tr.Turns[1]
	var sawThinking, sawTool, sawText, sawUsage bool
	for _, b := range asst.Blocks {
		switch b.Type {
		case BlockThinking:
			sawThinking = b.Text == "let me reason"
		case BlockTool:
			sawTool = b.ToolName == "Bash" && b.ToolUseID == "t1" && b.ToolResult == "file.txt" &&
				b.ToolInput["command"] == "ls"
		case BlockText:
			sawText = b.Text == "here is the answer"
		case BlockUsage:
			sawUsage = usageVal(b.Usage, "input_tokens") == 120 &&
				usageVal(b.Usage, "output_tokens") == 42 &&
				usageVal(b.Usage, "cache_read_input_tokens") == 10
		}
	}
	if !sawThinking || !sawTool || !sawText || !sawUsage {
		t.Fatalf("round-trip blocks missing: thinking=%v tool=%v text=%v usage=%v blocks=%+v",
			sawThinking, sawTool, sawText, sawUsage, asst.Blocks)
	}
}
