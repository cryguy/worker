# worker

A standalone JavaScript worker runtime for Go, powered by QuickJS.

```go
import "github.com/cryguy/worker"
```

## Features

- QuickJS-based JavaScript execution with isolate pooling
- Cloudflare Workers-compatible API surface:
  - KV namespaces
  - R2 storage buckets
  - D1 databases (SQLite)
  - Durable Objects
  - Queue producers
  - Service bindings (worker-to-worker RPC)
  - Cache API
  - Static assets
  - Custom env bindings (user-defined QuickJS values)
- Web standard APIs: fetch, crypto, streams, WebSocket, HTMLRewriter, URL, TextEncoder/Decoder
- ES module bundling via esbuild
- Resource limits: memory, execution timeout, fetch count
- Cron scheduling support
- Arbitrary function invocation via `ExecuteFunction`

## Requirements

- Go 1.25+

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

Inject arbitrary QuickJS values into the worker's `env` object using `CustomBindings`. Each function is called per-request and receives the QuickJS VM:

```go
env := &worker.Env{
    Vars: map[string]string{"APP": "myapp"},
    CustomBindings: map[string]worker.EnvBindingFunc{
        "greet": func(vm *quickjs.VM) (quickjs.Value, error) {
            vm.RegisterFunc("__custom_greet", func(name string) string {
                return "Hello, " + name + "!"
            })
            return vm.EvalValue(`(name) => __custom_greet(name)`)
        },
        "config": func(vm *quickjs.VM) (quickjs.Value, error) {
            return vm.EvalValue(`({mode: "production"})`)
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
