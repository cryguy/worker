# worker

A standalone JavaScript worker runtime for Go with dual-engine support: **QuickJS** (default, pure Go) and **V8** (opt-in via build tag).

```go
import "github.com/cryguy/worker"
```

## Features

- **Dual JS engine support** — QuickJS (default) or V8, selectable at build time
- Cloudflare Workers-compatible API surface:
  - KV namespaces
  - R2 storage buckets
  - D1 databases (SQLite)
  - Durable Objects
  - Queue producers
  - Service bindings (worker-to-worker RPC)
  - Cache API
  - Static assets
  - Custom env bindings
- Web standard APIs: fetch, crypto, streams, WebSocket, HTMLRewriter, URL, TextEncoder/Decoder
- ES module bundling via esbuild
- Resource limits: memory, execution timeout, fetch count
- Cron scheduling support
- Arbitrary function invocation via `ExecuteFunction`

## Engines

The runtime ships with two interchangeable JavaScript backends. The public API is identical regardless of which engine you use — only the build step differs.

### QuickJS (default)

QuickJS is the default engine. It compiles with pure Go (via modernc.org) and requires **no CGO**, making it easy to cross-compile and deploy anywhere.

```bash
go build ./...
go test ./...
```

**Pros:** no CGO, simple cross-compilation, smaller binary, works on any `GOOS/GOARCH` that Go supports.
**Cons:** slower JS execution compared to V8.

### V8 (opt-in)

V8 is available as an opt-in backend using the `v8` build tag. It provides significantly faster JavaScript execution via JIT compilation but requires CGO and platform-specific V8 binaries.

```bash
go build -tags v8 ./...
go test -tags v8 ./...
```

**Pros:** much faster JS execution (JIT), full V8 feature set.
**Cons:** requires CGO, Linux/macOS only (V8 binaries are platform-specific), larger binary.

## Requirements

- Go 1.25+
- **QuickJS backend:** no additional requirements
- **V8 backend:** CGO enabled, Linux or macOS

## Usage

The API is the same for both engines. Your Go code does not change when switching backends — only the build command does.

```go
package main

import "github.com/cryguy/worker"

func main() {
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
}
```

Build with QuickJS (default):
```bash
go build ./cmd/myapp
```

Build with V8:
```bash
go build -tags v8 ./cmd/myapp
```

## Custom Env Bindings

Inject values into the worker's `env` object using `CustomBindings`. Each function receives the engine-agnostic `JSRuntime` interface and returns a value that gets set on `env`:

```go
env := &worker.Env{
    Vars: map[string]string{"APP": "myapp"},
    CustomBindings: map[string]worker.EnvBindingFunc{
        "greeting": func(rt worker.JSRuntime) (any, error) {
            return "Hello from Go!", nil
        },
        "config": func(rt worker.JSRuntime) (any, error) {
            return map[string]any{"mode": "production", "debug": false}, nil
        },
    },
}
```

The worker script can then access `env.greeting` and `env.config.mode`. This works identically on both QuickJS and V8.

## ExecuteFunction

Call any named export on a worker module with JSON-serializable arguments. The return value is JSON-serialized into `result.Data`:

```go
source := `export default {
  add(env, a, b) {
    return { sum: a + b };
  },
};`

bytes, _ := engine.CompileAndCache("site", "deploy", source)
result := engine.ExecuteFunction("site", "deploy", env, "add", 3, 4)
// result.Data == `{"sum":7}`
```

This is useful for plugin-style architectures where Go orchestrates and JS modules provide the logic.

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
