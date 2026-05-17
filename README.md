# kit

Shared Go infrastructure library for [Brightman AI](https://github.com/brightman-ai) projects.

## Packages

| Package | Description |
|---------|-------------|
| `obs` | Module-scoped structured logger with ring buffer |
| `log` | JSON structured logging with caller info |
| `metrics` | Lightweight counter/histogram/gauge registry |
| `contextx` | Goroutine-level trace context (TraceID, SessionID) |
| `broadcast` | Generic typed event broadcaster |
| `ensure` | Development-time assertions |
| `llm/ssekit` | SSE stream reader and writer |
| `llm/toolcall` | Incremental tool call reassembly |
| `llm/event` | Unified streaming event model |
| `llm/stream` | Multi-provider LLM stream decoders (Claude, OpenAI-compatible) |

## Installation

```bash
go get github.com/brightman-ai/kit
```

## Usage

```go
import (
    "github.com/brightman-ai/kit/log"
    "github.com/brightman-ai/kit/obs"
    "github.com/brightman-ai/kit/metrics"
)
```

See [guide/](guide/) for detailed documentation.

## License

[MIT](LICENSE)
