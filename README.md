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
  - Custom env bindings (user-defined V8 values)
- Web standard APIs: fetch, crypto, streams, WebSocket, HTMLRewriter, URL, TextEncoder/Decoder
- ES module bundling via esbuild
- Resource limits: memory, execution timeout, fetch count
- Cron scheduling support
- Arbitrary function invocation via `ExecuteFunction`

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

## Custom Env Bindings

Inject arbitrary V8 values into the worker's `env` object using `CustomBindings`. Each function is called per-request and receives the V8 isolate and context:

```go
env := &worker.Env{
    Vars: map[string]string{"APP": "myapp"},
    CustomBindings: map[string]worker.EnvBindingFunc{
        "greet": func(iso *v8.Isolate, ctx *v8.Context) (*v8.Value, error) {
            fn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
                val, _ := v8.NewValue(iso, "Hello, "+info.Args()[0].String()+"!")
                return val
            })
            return fn.GetFunction(ctx).Value, nil
        },
        "config": func(iso *v8.Isolate, ctx *v8.Context) (*v8.Value, error) {
            obj, _ := worker.NewJSObject(iso, ctx)
            v, _ := v8.NewValue(iso, "production")
            _ = obj.Set("mode", v)
            return obj.Value, nil
        },
    },
}
```

The worker script can then access `env.greet("world")` and `env.config.mode`.

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
