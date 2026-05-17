package event

import "sync"

// SafeEmitter wraps an Emitter with a mutex for concurrent use.
// Use this when multiple goroutines emit to the same downstream (e.g., Council fan-in).
func SafeEmitter(emit Emitter) Emitter {
	var mu sync.Mutex
	return func(ev Event) bool {
		mu.Lock()
		defer mu.Unlock()
		return emit(ev)
	}
}
