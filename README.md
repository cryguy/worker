# worker

A standalone JavaScript worker runtime for Go, powered by V8.

```go
import "github.com/cryguy/worker"
```

## Features

- V8-based JavaScript execution with isolate pooling
- Cloudflare Workers-compatible API surface:
  - KV namespaces
  - R2 storage buckets
  - D1 databases (SQLite)
  - Durable Objects
  - Queue producers
  - Service bindings (worker-to-worker RPC)
  - Cache API
  - Static assets
- Web standard APIs: fetch, crypto, streams, WebSocket, HTMLRewriter, URL, TextEncoder/Decoder
- ES module bundling via esbuild
- Resource limits: memory, execution timeout, fetch count
- Cron scheduling support

## Requirements

- Go 1.25+
- CGO enabled (required by V8)
- Linux or macOS (V8 binaries are platform-specific)

## Usage

```go
cfg := worker.EngineConfig{
    PoolSize:         4,
    MemoryLimitMB:    128,
    ExecutionTimeout: 30000,
    MaxFetchRequests: 50,
}

engine := worker.NewEngine(cfg, mySourceLoader)
defer engine.Shutdown()

env := &worker.Env{
    Vars:    map[string]string{"API_KEY": "..."},
    Secrets: map[string]string{"SECRET": "..."},
    KV:      map[string]worker.KVStore{"MY_KV": myKVImpl},
}

result := engine.Execute("site-id", "deploy-key", env, &worker.WorkerRequest{
    Method:  "GET",
    URL:     "https://example.com/",
    Headers: map[string]string{},
})
```

## Interfaces

The runtime is decoupled from storage backends via interfaces. Implement these to provide platform bindings:

- `SourceLoader` - Load worker JavaScript source code
- `KVStore` - Key-value storage
- `CacheStore` - HTTP cache
- `R2Store` - Object storage (S3/R2 compatible)
- `DurableObjectStore` - Durable Object storage
- `QueueSender` - Message queue producer
- `AssetsFetcher` - Static asset serving
- `WorkerDispatcher` - Service binding dispatch

## License

MIT
