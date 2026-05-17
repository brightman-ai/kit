package contextx

import (
	"context"
	"sync"
	"testing"
)

func TestGoroutineID(t *testing.T) {
	gid := GoroutineID()
	if gid <= 0 {
		t.Errorf("expected positive goroutine ID, got %d", gid)
	}

	// Same goroutine should return same ID
	gid2 := GoroutineID()
	if gid != gid2 {
		t.Errorf("goroutine ID changed: %d -> %d", gid, gid2)
	}
}

func TestTID(t *testing.T) {
	defer Clear()

	// Default empty
	if tid := GetTID(); tid != "" {
		t.Errorf("expected empty TID, got %q", tid)
	}

	// Set and get
	SetTID("chat/req-001")
	if tid := GetTID(); tid != "chat/req-001" {
		t.Errorf("expected 'chat/req-001', got %q", tid)
	}

	// Update
	SetTID("upgrade/task-xyz")
	if tid := GetTID(); tid != "upgrade/task-xyz" {
		t.Errorf("expected 'upgrade/task-xyz', got %q", tid)
	}
}

func TestStage(t *testing.T) {
	defer Clear()

	// Default empty
	if stg := GetStage(); stg != "" {
		t.Errorf("expected empty stage, got %q", stg)
	}

	// Set and get
	SetStage("app/init")
	if stg := GetStage(); stg != "app/init" {
		t.Errorf("expected 'app/init', got %q", stg)
	}
}

func TestCurrent(t *testing.T) {
	defer Clear()

	SetTID("test/123")
	SetStage("http/handler")

	ctx := Current()
	if ctx.TID != "test/123" {
		t.Errorf("expected TID 'test/123', got %q", ctx.TID)
	}
	if ctx.Stage != "http/handler" {
		t.Errorf("expected stage 'http/handler', got %q", ctx.Stage)
	}
}

func TestClear(t *testing.T) {
	SetTID("test/clear")
	SetStage("test/stage")
	Clear()

	if tid := GetTID(); tid != "" {
		t.Errorf("expected empty TID after clear, got %q", tid)
	}
	if stg := GetStage(); stg != "" {
		t.Errorf("expected empty stage after clear, got %q", stg)
	}
}

func TestClone(t *testing.T) {
	defer Clear()

	SetTID("parent/001")
	SetStage("app/init")

	clone := Clone()

	// Modify original
	SetTID("parent/002")
	SetStage("app/running")

	// Clone should preserve original values
	if clone.TID != "parent/001" {
		t.Errorf("clone TID changed: got %q", clone.TID)
	}
	if clone.Stage != "app/init" {
		t.Errorf("clone stage changed: got %q", clone.Stage)
	}
}

func TestInherit(t *testing.T) {
	defer Clear()

	parent := &Context{
		TID:   "parent/001",
		Stage: "parent/stage",
	}

	Inherit(parent)

	if tid := GetTID(); tid != "parent/001" {
		t.Errorf("expected inherited TID 'parent/001', got %q", tid)
	}
	if stg := GetStage(); stg != "parent/stage" {
		t.Errorf("expected inherited stage 'parent/stage', got %q", stg)
	}

	// Inherit nil should be no-op
	SetTID("test/123")
	Inherit(nil)
	if tid := GetTID(); tid != "test/123" {
		t.Errorf("inherit nil changed TID: got %q", tid)
	}
}

func TestWithTID(t *testing.T) {
	cleanup := WithTID("scoped/001")

	if tid := GetTID(); tid != "scoped/001" {
		t.Errorf("expected 'scoped/001', got %q", tid)
	}

	cleanup()

	if tid := GetTID(); tid != "" {
		t.Errorf("expected empty after cleanup, got %q", tid)
	}
}

func TestWithStage(t *testing.T) {
	defer Clear()

	SetStage("outer/stage")
	cleanup := WithStage("inner/stage")

	if stg := GetStage(); stg != "inner/stage" {
		t.Errorf("expected 'inner/stage', got %q", stg)
	}

	cleanup()

	if stg := GetStage(); stg != "outer/stage" {
		t.Errorf("expected 'outer/stage' after cleanup, got %q", stg)
	}
}

func TestGo(t *testing.T) {
	defer Clear()

	SetTID("parent/task")
	SetStage("parent/stage")

	var wg sync.WaitGroup
	var childTID, childStage string

	wg.Add(1)
	Go(func() {
		defer wg.Done()
		childTID = GetTID()
		childStage = GetStage()
	})
	wg.Wait()

	if childTID != "parent/task" {
		t.Errorf("child should inherit TID, got %q", childTID)
	}
	if childStage != "parent/stage" {
		t.Errorf("child should inherit stage, got %q", childStage)
	}
}

func TestConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	n := 100

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer Clear()

			tid := "concurrent/" + string(rune('A'+id%26))
			SetTID(tid)

			// Do some work
			for j := 0; j < 100; j++ {
				if got := GetTID(); got != tid {
					t.Errorf("goroutine %d: TID changed from %q to %q", id, tid, got)
					return
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestSessionID_SetGet(t *testing.T) {
	defer Clear()
	SetSessionID("test-session-123")

	if got := GetSessionID(); got != "test-session-123" {
		t.Errorf("GetSessionID() = %q, want %q", got, "test-session-123")
	}
}

func TestWithSessionID_Context(t *testing.T) {
	ctx := context.Background()
	ctx = WithSessionID(ctx, "ctx-session-456")

	if got := SessionID(ctx); got != "ctx-session-456" {
		t.Errorf("SessionID() = %q, want %q", got, "ctx-session-456")
	}
}

func TestSessionID_EmptyContext(t *testing.T) {
	ctx := context.Background()
	if got := SessionID(ctx); got != "" {
		t.Errorf("SessionID() on empty context = %q, want empty", got)
	}
}

func TestClone_IncludesSessionID(t *testing.T) {
	defer Clear()
	SetSessionID("clone-session")

	cloned := Clone()
	if cloned.SessionID != "clone-session" {
		t.Errorf("Clone().SessionID = %q, want %q", cloned.SessionID, "clone-session")
	}
}

func BenchmarkGoroutineID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		GoroutineID()
	}
}

func BenchmarkGetTID(b *testing.B) {
	defer Clear()
	SetTID("bench/test")
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		GetTID()
	}
}

func BenchmarkSetTID(b *testing.B) {
	defer Clear()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		SetTID("bench/test")
	}
}
