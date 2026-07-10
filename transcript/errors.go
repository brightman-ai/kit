package transcript

import "errors"

// ErrUnknownSource is returned when a transcript load names a source kind that
// is not wired into the aggregator.
var ErrUnknownSource = errors.New("sessionsource: unknown source")
