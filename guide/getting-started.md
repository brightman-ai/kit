# Getting Started

## Requirements

- Go 1.26+

## Installation

```bash
go get github.com/brightman-ai/kit
```

Import individual packages as needed:

```bash
go get github.com/brightman-ai/kit/obs
go get github.com/brightman-ai/kit/log
go get github.com/brightman-ai/kit/metrics
go get github.com/brightman-ai/kit/contextx
go get github.com/brightman-ai/kit/broadcast
go get github.com/brightman-ai/kit/ensure
go get github.com/brightman-ai/kit/llm/ssekit
go get github.com/brightman-ai/kit/llm/event
go get github.com/brightman-ai/kit/llm/stream
go get github.com/brightman-ai/kit/llm/toolcall
```

## Quick Start

### Structured logging

```go
import "github.com/brightman-ai/kit/log"

log.Info("server started", "port", 8080)
log.Error("request failed", "err", err, "path", r.URL.Path)
```

### Module-scoped observability

```go
import "github.com/brightman-ai/kit/obs"

// Initialize once at startup
obs.Init(obs.LevelInfo, os.Stderr)

// Use module logger
logger := obs.New("my-module")
logger.Info("ready")
```

### Metrics

```go
import "github.com/brightman-ai/kit/metrics"

requests := metrics.NewCounter("http_requests_total")
latency  := metrics.NewHistogram("http_latency_seconds", metrics.DefaultBuckets)

// In handler:
requests.Add(1)
start := time.Now()
// ... handle request ...
latency.Observe(obs.Since(start))
```

### Trace context

```go
import "github.com/brightman-ai/kit/contextx"

ctx := contextx.WithTraceID(context.Background(), "req-abc")
traceID := contextx.TraceID(ctx)
```

### Generic event broadcast

```go
import "github.com/brightman-ai/kit/broadcast"

b := broadcast.New[MyEvent]()
sub := b.Subscribe()
go func() {
    for e := range sub.C {
        // handle event
    }
}()
b.Publish(MyEvent{...})
```

### LLM stream decoding

```go
import (
    "github.com/brightman-ai/kit/llm/stream"
    "github.com/brightman-ai/kit/llm/event"
)

decoder := stream.NewClaudeDecoder()
for ev := range decoder.Decode(resp.Body) {
    switch ev.Type {
    case event.TypeText:
        fmt.Print(ev.Text)
    case event.TypeUsage:
        log.Info("tokens", "input", ev.Usage.InputTokens)
    }
}
```
