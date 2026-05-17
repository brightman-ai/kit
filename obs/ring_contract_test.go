// CT-06: Observation Ring Buffer Behavior Contract
//
// 仅补充 ring_test.go 中未覆盖的场景。
// 与 ring_test.go 共享同一 package obs（白盒测试），直接运行不需要服务器。
//
// 已覆盖（不重复，见 ring_test.go）:
//   - 基本 Push + RecentEntries (TestRing_PushAndRecentEntries)
//   - 溢出 Count/Capacity (TestRing_Overflow)
//   - limit 参数 (TestRing_Limit)
//   - since 过滤 (TestRing_SinceFilter)
//   - 倒序排列 (TestRing_DescOrder)
//   - 并发读写安全 (TestRing_ConcurrentSafety)
//   - 空 ring Stats (TestRing_Stats_Empty)
//   - 全局函数 (TestGlobalRingFunctions)
//   - 容量 0 panic (TestRing_NewRing_PanicOnZeroCapacity)
//
// 新增覆盖:
//   CT-06-01  溢出后 Stats.OldestTime 指向正确的最旧存活条目
//   CT-06-02  溢出后 level 过滤仍然正确（wrap-around 不破坏过滤语义）
//   CT-06-03  limit + since 组合过滤（AND 语义）
//   CT-06-04  大量写入后 RecentEntries 返回数量 == capacity（不超出也不少于）
//   CT-06-05  Stats.OldestTime 在未满 / 刚满 / 溢出三种状态下的语义差异
//   CT-06-06  并发高频溢出下 Stats 的 Count <= Capacity 不变量
package obs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// makeContractEntry 创建指定时间戳的 Entry，避免与 ring_test.go 中 makeEntryAt 重名。
func makeContractEntry(level, msg string, t time.Time) Entry {
	return Entry{
		L:   level,
		T:   t.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Mod: "contract",
		Msg: msg,
	}
}

// parseTimestamp 解析 Entry.T 字段为 time.Time。
func parseTimestamp(t *testing.T, ts string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z07:00", ts)
	if err != nil {
		t.Fatalf("invalid timestamp %q: %v", ts, err)
	}
	return parsed
}

// ── CT-06-01: 溢出后 OldestTime 指向最旧存活条目 ─────────────────────────────

// TestRingContract_OldestTimeAfterOverflow 验证：
// ring 溢出后 Stats.OldestTime 指向当前未被覆盖的最旧条目，而非初始写入的第 0 条。
func TestRingContract_OldestTimeAfterOverflow(t *testing.T) {
	capacity := 3
	r := NewRing(capacity)
	base := time.Now().UTC().Truncate(time.Millisecond)

	// 写入 capacity+1 条，时间依次递增
	for i := 0; i < capacity+1; i++ {
		r.Push(makeContractEntry("WARN", fmt.Sprintf("msg%d", i), base.Add(time.Duration(i)*time.Second)))
	}

	stats := r.Stats()

	if stats.Count != capacity {
		t.Errorf("Count = %d, want %d", stats.Count, capacity)
	}
	if stats.OldestTime == "" {
		t.Fatal("OldestTime must not be empty after overflow")
	}

	oldestT := parseTimestamp(t, stats.OldestTime)

	// 溢出 1 条后，第 0 条（base+0s）已被覆盖，最旧存活的是第 1 条（base+1s）
	expectedOldest := base.Add(1 * time.Second)
	diff := oldestT.Sub(expectedOldest)
	if diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("OldestTime after overflow = %v, want ~%v (msg1, msg0 overwritten)", oldestT, expectedOldest)
	}

	t.Logf("CT-06-01 OK: OldestTime = %s", stats.OldestTime)
}

// ── CT-06-02: 溢出后 level 过滤正确 ─────────────────────────────────────────

// TestRingContract_LevelFilterAfterOverflow 验证：
// ring 环形覆盖后，level 过滤语义不受 wrap-around 影响，WARN + ERROR 条目之和等于全部。
func TestRingContract_LevelFilterAfterOverflow(t *testing.T) {
	capacity := 5
	r := NewRing(capacity)
	base := time.Now().UTC()

	// 写 capacity*2 条交替 WARN/ERROR
	for i := 0; i < capacity*2; i++ {
		level := "WARN"
		if i%2 == 0 {
			level = "ERROR"
		}
		r.Push(makeContractEntry(level, fmt.Sprintf("msg%d", i), base.Add(time.Duration(i)*time.Second)))
	}

	all := r.RecentEntries("", 0, time.Time{})
	errors := r.RecentEntries("ERROR", 0, time.Time{})
	warns := r.RecentEntries("WARN", 0, time.Time{})

	if len(all) != capacity {
		t.Errorf("all = %d, want %d", len(all), capacity)
	}

	// WARN + ERROR == all（无泄漏、无遗漏）
	if len(errors)+len(warns) != len(all) {
		t.Errorf("ERROR(%d)+WARN(%d) != all(%d): level filter inconsistent after wrap-around",
			len(errors), len(warns), len(all))
	}

	// 每条 ERROR 的 level 必须确实是 ERROR
	for i, e := range errors {
		if e.L != "ERROR" {
			t.Errorf("errors[%d].L = %q, want ERROR", i, e.L)
		}
	}

	for i, e := range warns {
		if e.L != "WARN" {
			t.Errorf("warns[%d].L = %q, want WARN", i, e.L)
		}
	}

	t.Logf("CT-06-02 OK: after overflow ERROR=%d WARN=%d all=%d", len(errors), len(warns), len(all))
}

// ── CT-06-03: limit + since 组合过滤（AND 语义）──────────────────────────────

// TestRingContract_LimitAndSinceCombined 验证：
// limit 和 since 同时指定时两个条件同时生效，返回时间 >= since 且数量 <= limit。
func TestRingContract_LimitAndSinceCombined(t *testing.T) {
	r := NewRing(20)
	base := time.Now().UTC()

	// 写 10 条，时间从 base-9m 到 base-0m（每隔 1 分钟）
	for i := 0; i < 10; i++ {
		r.Push(makeContractEntry("WARN", fmt.Sprintf("msg%d", i), base.Add(-time.Duration(9-i)*time.Minute)))
	}

	// since=base-5m, limit=2 → 5 条满足 since，但只返回最新的 2 条
	since := base.Add(-5 * time.Minute)
	result := r.RecentEntries("", 2, since)

	if len(result) != 2 {
		t.Errorf("limit+since: want 2 entries, got %d", len(result))
	}

	for i, e := range result {
		et := parseTimestamp(t, e.T)
		if et.Before(since) {
			t.Errorf("result[%d] timestamp %v is before since %v — since filter broken", i, et, since)
		}
	}

	t.Logf("CT-06-03 OK: limit+since returned %d entries", len(result))
}

// ── CT-06-04: 大量写入后 RecentEntries 返回数量恰好等于 capacity ──────────────

// TestRingContract_RecentEntriesExactlyCapacityAfterOverflow 验证：
// 写入量远超 capacity 后，RecentEntries("", 0, time.Time{}) 返回的数量恰好等于 capacity。
func TestRingContract_RecentEntriesExactlyCapacityAfterOverflow(t *testing.T) {
	capacity := 7
	r := NewRing(capacity)

	for i := 0; i < capacity*5; i++ {
		r.Push(makeContractEntry("WARN", fmt.Sprintf("msg%d", i), time.Now()))
	}

	all := r.RecentEntries("", 0, time.Time{})
	if len(all) != capacity {
		t.Errorf("RecentEntries after large overflow = %d, want exactly %d (capacity)", len(all), capacity)
	}

	t.Logf("CT-06-04 OK: entries=%d capacity=%d", len(all), capacity)
}

// ── CT-06-05: OldestTime 在三种状态下的语义 ──────────────────────────────────

// TestRingContract_OldestTimeSemantics 验证：
// 未满时 OldestTime == 第 0 条；刚满时 OldestTime == 第 0 条；
// 溢出 1 条后 OldestTime 变为第 1 条（第 0 条已被覆盖）。
func TestRingContract_OldestTimeSemantics(t *testing.T) {
	capacity := 4
	r := NewRing(capacity)
	base := time.Now().UTC().Truncate(time.Millisecond)

	t0 := base
	t1 := base.Add(1 * time.Second)
	t2 := base.Add(2 * time.Second)
	t3 := base.Add(3 * time.Second)
	t4 := base.Add(4 * time.Second) // 覆盖 t0

	// 未满：写 2 条
	r.Push(makeContractEntry("WARN", "e0", t0))
	r.Push(makeContractEntry("WARN", "e1", t1))

	oldest2 := parseTimestamp(t, r.Stats().OldestTime)
	if oldest2.Unix() != t0.Unix() {
		t.Errorf("partial fill (2/%d): OldestTime=%v want t0=%v", capacity, oldest2, t0)
	}

	// 刚好满：写满
	r.Push(makeContractEntry("WARN", "e2", t2))
	r.Push(makeContractEntry("WARN", "e3", t3))

	oldestFull := parseTimestamp(t, r.Stats().OldestTime)
	if oldestFull.Unix() != t0.Unix() {
		t.Errorf("exactly full (%d/%d): OldestTime=%v want t0=%v", capacity, capacity, oldestFull, t0)
	}

	// 溢出：写第 5 条
	r.Push(makeContractEntry("WARN", "e4", t4))

	oldestOver := parseTimestamp(t, r.Stats().OldestTime)
	if oldestOver.Unix() != t1.Unix() {
		t.Errorf("after 1 overflow: OldestTime=%v want t1=%v (t0 overwritten)", oldestOver, t1)
	}

	t.Logf("CT-06-05 OK: partial=%v full=%v overflow=%v", oldest2, oldestFull, oldestOver)
}

// ── CT-06-06: 并发高频溢出 Stats 不变量 ──────────────────────────────────────

// TestRingContract_ConcurrentOverflowStats 验证：
// 大量并发写入（远超 capacity）后 Stats().Count <= Capacity，
// OldestTime 非空（ring 有数据）且 RecentEntries 返回数量合法。
func TestRingContract_ConcurrentOverflowStats(t *testing.T) {
	capacity := 16
	r := NewRing(capacity)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				r.Push(makeContractEntry("WARN", fmt.Sprintf("g%d-j%d", gid, j), time.Now()))
			}
		}(i)
	}
	wg.Wait()

	stats := r.Stats()

	if stats.Count > stats.Capacity {
		t.Errorf("Count(%d) > Capacity(%d) after concurrent overflow", stats.Count, stats.Capacity)
	}
	if stats.Count == 0 {
		t.Error("Count must be > 0 after concurrent writes")
	}
	if stats.OldestTime == "" {
		t.Error("OldestTime must be non-empty when Count > 0")
	}

	entries := r.RecentEntries("", 0, time.Time{})
	if len(entries) > capacity {
		t.Errorf("RecentEntries returned %d > capacity %d", len(entries), capacity)
	}

	t.Logf("CT-06-06 OK: Count=%d Capacity=%d entries=%d", stats.Count, stats.Capacity, len(entries))
}
