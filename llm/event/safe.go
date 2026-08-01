package event

import "github.com/brightman-ai/kit/workstream"

// SafeEmitter is kept as a function (rather than a variable alias) so package
// documentation and call sites remain clear while all behaviour lives in the
// canonical workstream implementation.
func SafeEmitter(emit Emitter) Emitter { return workstream.SafeEmitter(emit) }
