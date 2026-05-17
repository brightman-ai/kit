# Architecture

`kit` is a collection of independent, zero-business-logic infrastructure packages. Each package has no dependency on other packages in this repository (except `log` → `contextx`).

## Package Structure

```
kit/
├── obs/          # Module-scoped observability facade
│   ├── obs.go        Init / New / SetLevel / SetOutput
│   ├── log.go        Leveled log methods on module logger
│   ├── metrics.go    Counter / Histogram / Gauge wrappers
│   ├── ring.go       In-memory ring buffer for recent log entries
│   └── ctx.go        Trace context helpers
│
├── log/          # Standalone JSON structured logger
│   ├── log.go        Logger type, Info/Error/Debug/Warn
│   └── errors.go     Error annotation helpers
│
├── metrics/      # Low-dependency metric registry
│   └── metrics.go    Counter, Histogram, Gauge + default registry
│
├── contextx/     # Goroutine-level trace propagation
│   └── contextx.go   TraceID / SessionID get/set on context.Context
│
├── broadcast/    # Generic typed pub/sub
│   └── broadcast.go  Broadcaster[T] — fan-out to multiple subscribers
│
├── ensure/       # Development assertions (panic on violation)
│   └── ensure.go     NotNil / True / Equal / NoError
│
└── llm/          # LLM streaming primitives
    ├── event/        Unified event model (Text, ToolCall, Usage, Done)
    ├── ssekit/       SSE wire-format reader and writer
    ├── stream/       Provider decoders (Claude; OpenAI-compatible planned)
    └── toolcall/     Incremental JSON reassembly for streaming tool calls
```

## Design Principles

- **Zero private dependencies** — any Go project can import kit directly.
- **Single-direction dependency** — `log` may import `contextx`; nothing else crosses packages.
- **No framework coupling** — packages work with stdlib `io.Writer`, `context.Context`, and `http.ResponseWriter`.
- **Incremental adoption** — import only what you need; packages do not force initialization of siblings.
