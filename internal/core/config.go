package core

// EngineConfig holds runtime configuration for the worker engine.
type EngineConfig struct {
	PoolSize         int // number of JS runtime instances per site pool
	MemoryLimitMB    int // per-runtime memory limit
	ExecutionTimeout int // milliseconds before worker is terminated
	MaxFetchRequests int // max outbound fetches per request
	FetchTimeoutSec  int // per-fetch timeout in seconds
	MaxResponseBytes int // max response body size
	MaxScriptSizeKB  int // max bundled script size
}
