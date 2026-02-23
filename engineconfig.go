package worker

// EngineConfig holds runtime configuration for the worker engine.
// This is a library-owned struct that replaces config.WorkerConfig,
// omitting application-level fields (MaxLogRetention, DataDir).
type EngineConfig struct {
	PoolSize         int // number of QuickJS VMs per site pool
	MemoryLimitMB    int // per-VM memory limit
	ExecutionTimeout int // milliseconds before worker is terminated
	MaxFetchRequests int // max outbound fetches per request
	FetchTimeoutSec  int // per-fetch timeout in seconds
	MaxResponseBytes int // max response body size
	MaxScriptSizeKB  int // max bundled script size
}
