package workstream

import (
	"sync"
	"time"
)

func SafeEmitter(next Emitter) Emitter {
	if next == nil {
		return Discard()
	}
	var mu sync.Mutex
	return func(ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		return next(ev)
	}
}

// Decorate wraps an emitter so every event passing through gets stamped by fn.
//
// 这是"谁该知道 session_id / turn_id"的答案。可复用的下层（agentloop 之类）只知道
// 自己那点事（round、tool_count），不该也无法知道宿主的标识；宿主不该为了加两个
// meta 字段就去改下层签名。Decorate 让宿主在**注入 Emitter 的那一刻**把自己的上下文
// 焊上去，下层保持零宿主知识。
//
//	emit := workstream.Decorate(baseEmitter, func(ev workstream.Event) workstream.Event {
//	    return ev.WithMeta("session_id", sessionID).WithMeta("turn_id", turnID)
//	})
func Decorate(next Emitter, fn func(Event) Event) Emitter {
	if next == nil {
		return Discard()
	}
	if fn == nil {
		return next
	}
	return func(ev Event) bool {
		return next(fn(ev))
	}
}

func SeqEmitter(next Emitter) Emitter {
	if next == nil {
		return Discard()
	}
	var mu sync.Mutex
	seq := 0
	return func(ev Event) bool {
		mu.Lock()
		seq++
		ev = ev.WithMeta("seq", seq)
		mu.Unlock()
		return next(ev)
	}
}

// TimestampEmitter stamps a millisecond Unix timestamp into Meta["ts"]. It is
// retained for callers that need the old numeric clock in addition to Event.At.
func TimestampEmitter(next Emitter) Emitter {
	if next == nil {
		return Discard()
	}
	return func(ev Event) bool {
		return next(ev.WithMeta("ts", time.Now().UnixMilli()))
	}
}
