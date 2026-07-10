package transcript

import (
	"os"
	"sync"
	"time"
)

// 进程级 scanMeta 结果缓存 (2026-07-03 生产事故修复)。
//
// 事故：/api/workspaces/:id/runtime-sessions 被前端长开 tab 轮询（12 分钟 4.3k 次），
// 每请求对全部 rollout/jsonl 做完整流式重解析（4-5s/次，claude 单文件可达 18MB）→
// CPU 打满、HTTP 层饿死、整站不可访问。列表元数据只依赖文件内容，天然可按
// (path, size, mtime) 记忆化：未变文件零重解析，活跃会话文件（mtime 变）只重解析它自己。
//
// 缓存放包级而非 source 实例：buildAggregator 目前每请求新建 source，实例级缓存
// 会被每次请求丢弃（codex cwdIndexCache 的注释已指出这一点）。
// SessionMeta 全值类型（string/time/int/bool），值拷贝进出缓存无共享可变状态；
// aggregator 的 hidden/rename overlay 打在 ListSessions 返回的副本上，不污染缓存。
// 容量上界 = 会话文件数（数百量级），无需逐出。
type metaCacheEntry struct {
	size  int64
	mtime time.Time
	meta  SessionMeta
	cwd   string // codex: session_meta.cwd（claude 恒空）
	ok    bool   // codex: scanMeta 的 ok（无 id 的坏文件也缓存，避免反复重扫垃圾）
}

var metaCache sync.Map // path(string) → metaCacheEntry

// loadMetaCache 返回 path 的缓存命中项。st 一并返回给未命中方复用（省一次 Stat）。
func loadMetaCache(path string) (entry metaCacheEntry, st os.FileInfo, hit bool) {
	st, err := os.Stat(path)
	if err != nil {
		return metaCacheEntry{}, nil, false // 让调用方走原扫描路径（其自身对 open 失败已有诚实降级）
	}
	if v, ok := metaCache.Load(path); ok {
		e := v.(metaCacheEntry)
		if e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
			return e, st, true
		}
	}
	return metaCacheEntry{}, st, false
}

func storeMetaCache(path string, st os.FileInfo, meta SessionMeta, cwd string, ok bool) {
	if st == nil {
		return
	}
	metaCache.Store(path, metaCacheEntry{size: st.Size(), mtime: st.ModTime(), meta: meta, cwd: cwd, ok: ok})
}
