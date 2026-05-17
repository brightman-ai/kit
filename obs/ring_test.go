package obs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// makeEntry 构造测试用 Entry，T 字段使用统一格式。
func makeEntry(level, msg string) Entry {
	return Entry{
		L:   level,
		T:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Mod: "test",
		Msg: msg,
	}
}

// makeEntryAt 构造指定时间戳的 Entry。
func makeEntryAt(level, msg string, t time.Time) Entry {
	return Entry{
		L:   level,
		T:   t.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Mod: "test",
		Msg: msg,
	}
}

// TestRing_PushAndRecentEntries 验证基本写入与查询。
func TestRing_PushAndRecentEntries(t *testing.T) {
	r := NewRing(10)

	r.Push(makeEntry("WARN", "warn1"))
	r.Push(makeEntry("ERROR", "err1"))
	r.Push(makeEntry("WARN", "warn2"))

	all := r.RecentEntries("", 0, time.Time{})
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}

	warns := r.RecentEntries("WARN", 0, time.Time{})
	if len(warns) != 2 {
		t.Fatalf("expected 2 WARN entries, got %d", len(warns))
	}

	errors := r.RecentEntries("ERROR", 0, time.Time{})
	if len(errors) != 1 {
		t.Fatalf("expected 1 ERROR entry, got %d", len(errors))
	}
	if errors[0].Msg != "err1" {
		t.Fatalf("unexpected msg: %q", errors[0].Msg)
	}
}

// TestRing_Overflow 验证容量溢出时旧条目被覆盖。
func TestRing_Overflow(t *testing.T) {
	capacity := 5
	r := NewRing(capacity)

	// 写入 capacity+2 条目
	for i := 0; i < capacity+2; i++ {
		r.Push(makeEntry("WARN", fmt.Sprintf("msg%d", i)))
	}

	all := r.RecentEntries("", 0, time.Time{})
	if len(all) != capacity {
		t.Fatalf("expected %d entries after overflow, got %d", capacity, len(all))
	}

	stats := r.Stats()
	if stats.Count != capacity {
		t.Fatalf("Stats.Count = %d, want %d", stats.Count, capacity)
	}
	if stats.Capacity != capacity {
		t.Fatalf("Stats.Capacity = %d, want %d", stats.Capacity, capacity)
	}
}

// TestRing_Limit 验证 limit 参数限制返回数量。
func TestRing_Limit(t *testing.T) {
	r := NewRing(20)
	for i := 0; i < 10; i++ {
		r.Push(makeEntry("WARN", fmt.Sprintf("msg%d", i)))
	}

	limited := r.RecentEntries("", 3, time.Time{})
	if len(limited) != 3 {
		t.Fatalf("expected 3 entries with limit=3, got %d", len(limited))
	}
}

// TestRing_SinceFilter 验证 since 时间过滤。
func TestRing_SinceFilter(t *testing.T) {
	r := NewRing(20)
	base := time.Now().UTC()

	r.Push(makeEntryAt("WARN", "old", base.Add(-10*time.Minute)))
	r.Push(makeEntryAt("WARN", "mid", base.Add(-2*time.Minute)))
	r.Push(makeEntryAt("WARN", "new", base.Add(-30*time.Second)))

	// 只要 5 分钟内的条目
	since := base.Add(-5 * time.Minute)
	filtered := r.RecentEntries("", 0, since)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 entries since -5m, got %d", len(filtered))
	}
}

// TestRing_DescOrder 验证返回顺序为时间倒序（最新在前）。
func TestRing_DescOrder(t *testing.T) {
	r := NewRing(10)
	base := time.Now().UTC()

	r.Push(makeEntryAt("WARN", "first", base.Add(-3*time.Second)))
	r.Push(makeEntryAt("WARN", "second", base.Add(-2*time.Second)))
	r.Push(makeEntryAt("WARN", "third", base.Add(-1*time.Second)))

	entries := r.RecentEntries("", 0, time.Time{})
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// 最新（third）应排在最前
	if entries[0].Msg != "third" {
		t.Fatalf("expected 'third' first, got %q", entries[0].Msg)
	}
	if entries[2].Msg != "first" {
		t.Fatalf("expected 'first' last, got %q", entries[2].Msg)
	}
}

// TestRing_ConcurrentSafety 验证并发写入不会 panic 或数据竞争。
func TestRing_ConcurrentSafety(t *testing.T) {
	r := NewRing(64)
	var wg sync.WaitGroup

	// 100 个并发写入 goroutine
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Push(makeEntry("WARN", fmt.Sprintf("concurrent-%d", i)))
		}(i)
	}

	// 10 个并发读取 goroutine
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.RecentEntries("", 10, time.Time{})
			_ = r.Stats()
		}()
	}

	wg.Wait()

	stats := r.Stats()
	if stats.Count > stats.Capacity {
		t.Fatalf("count %d > capacity %d", stats.Count, stats.Capacity)
	}
}

// TestRing_Stats_Empty 验证空 ring buffer 的统计信息。
func TestRing_Stats_Empty(t *testing.T) {
	r := NewRing(10)
	s := r.Stats()
	if s.Count != 0 {
		t.Fatalf("expected Count=0, got %d", s.Count)
	}
	if s.Capacity != 10 {
		t.Fatalf("expected Capacity=10, got %d", s.Capacity)
	}
	if s.OldestTime != "" {
		t.Fatalf("expected empty OldestTime, got %q", s.OldestTime)
	}
}

// TestGlobalRingFunctions 验证包级别函数可以正常调用。
func TestGlobalRingFunctions(t *testing.T) {
	// 全局 ring 在其他测试中可能已有数据，只验证接口可用
	stats := GlobalRingStats()
	if stats.Capacity <= 0 {
		t.Fatalf("GlobalRingStats returned invalid capacity: %d", stats.Capacity)
	}

	_ = RecentEntries("WARN", 10, time.Time{})
}

// TestRing_NewRing_PanicOnZeroCapacity 验证容量为 0 时 panic。
func TestRing_NewRing_PanicOnZeroCapacity(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for capacity=0, but did not panic")
		}
	}()
	NewRing(0)
}
