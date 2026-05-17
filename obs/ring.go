package obs

import (
	"sync"
	"time"
)

// Ring 是固定容量的循环缓冲区，保存最近的日志条目。
// 线程安全，使用 sync.RWMutex 保护并发访问。
type Ring struct {
	mu       sync.RWMutex
	buf      []Entry
	capacity int
	head     int // 下一次写入位置（循环）
	count    int // 当前存储的条目数（≤ capacity）
}

// RingStats 描述 ring buffer 的当前状态。
type RingStats struct {
	Count      int    `json:"count"`
	Capacity   int    `json:"capacity"`
	OldestTime string `json:"oldest_time"`
}

// NewRing 创建一个指定容量的 Ring。capacity 必须 > 0，否则 panic。
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("obs.NewRing: capacity must be > 0")
	}
	return &Ring{
		buf:      make([]Entry, capacity),
		capacity: capacity,
	}
}

// Push 将一条日志条目写入 ring buffer。
// 当 buffer 已满时，最旧的条目被覆盖。
func (r *Ring) Push(e Entry) {
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.capacity
	if r.count < r.capacity {
		r.count++
	}
	r.mu.Unlock()
}

// RecentEntries 返回满足过滤条件的条目，按时间倒序排列（最新在前）。
//   - level: 空字符串表示不过滤级别；否则只返回 l == level 的条目
//   - limit: ≤ 0 表示不限数量
//   - since: 零值表示不过滤时间；否则只返回 T >= since 的条目
func (r *Ring) RecentEntries(level string, limit int, since time.Time) []Entry {
	r.mu.RLock()
	// 把当前有效条目按写入顺序复制出来
	snapshot := make([]Entry, r.count)
	if r.count < r.capacity {
		// 尚未绕回：有效区间 [0, r.count)，head 就是 count
		copy(snapshot, r.buf[:r.count])
	} else {
		// 已绕回：最旧的起点是 head，向右循环
		n := copy(snapshot, r.buf[r.head:])
		copy(snapshot[n:], r.buf[:r.head])
	}
	r.mu.RUnlock()

	// 过滤 + 收集
	filtered := make([]Entry, 0, len(snapshot))
	for i := len(snapshot) - 1; i >= 0; i-- {
		e := snapshot[i]
		// 级别过滤
		if level != "" && e.L != level {
			continue
		}
		// 时间过滤
		if !since.IsZero() {
			t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", e.T)
			if err == nil && t.Before(since) {
				continue
			}
		}
		filtered = append(filtered, e)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}

	// filtered 已经是倒序（最新在前），直接返回
	return filtered
}

// Stats 返回 ring buffer 的统计信息。
func (r *Ring) Stats() RingStats {
	r.mu.RLock()
	count := r.count
	capacity := r.capacity
	head := r.head

	var oldestTime string
	if count > 0 {
		// 最旧条目的索引
		var oldestIdx int
		if count < capacity {
			oldestIdx = 0
		} else {
			oldestIdx = head // head 指向下一个写入位置，即最旧的位置
		}
		oldestTime = r.buf[oldestIdx].T
	}
	r.mu.RUnlock()

	return RingStats{
		Count:      count,
		Capacity:   capacity,
		OldestTime: oldestTime,
	}
}

// --- 全局 ring buffer ---

// globalRing 是包级别的全局 ring buffer，容量 2048。
// 由 Logger.emit() 在 level >= WARN 时自动写入。
var globalRing = NewRing(2048)

// RecentEntries 从全局 ring buffer 中查询最近的日志条目。
// 参数说明同 Ring.RecentEntries。
func RecentEntries(level string, limit int, since time.Time) []Entry {
	return globalRing.RecentEntries(level, limit, since)
}

// GlobalRingStats 返回全局 ring buffer 的统计信息。
func GlobalRingStats() RingStats {
	return globalRing.Stats()
}

