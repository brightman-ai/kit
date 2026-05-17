package obs

import (
	"io"
	"time"
)

// Init initializes the obs subsystem. Idempotent: repeated calls update level/output.
// Must be called before the first log entry. Defaults: INFO level, os.Stderr output.
func Init(level Level, out io.Writer) {
	SetLevel(level)
	if out != nil {
		SetOutput(out)
	}
}

// Since returns elapsed seconds since start, as float64.
// Convenience for Histogram.Observe:
//
//	start := time.Now()
//	defer h.Observe(obs.Since(start))
func Since(start time.Time) float64 {
	return time.Since(start).Seconds()
}
