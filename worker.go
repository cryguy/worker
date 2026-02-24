package worker

import "github.com/cryguy/worker/internal/core"

// Engine wraps a backend JS engine (QuickJS by default, V8 with -tags v8).
type Engine struct {
	backend core.EngineBackend
}

// NewEngine creates a new Engine with the given config and source loader.
func NewEngine(cfg EngineConfig, loader SourceLoader) *Engine {
	return &Engine{backend: newBackend(cfg, loader)}
}

// Execute runs the worker's fetch handler for the given request.
func (e *Engine) Execute(siteID, deployKey string, env *Env, req *WorkerRequest) *WorkerResult {
	return e.backend.Execute(siteID, deployKey, env, req)
}

// ExecuteScheduled runs the worker's scheduled handler.
func (e *Engine) ExecuteScheduled(siteID, deployKey string, env *Env, cron string) *WorkerResult {
	return e.backend.ExecuteScheduled(siteID, deployKey, env, cron)
}

// ExecuteTail runs the worker's tail handler.
func (e *Engine) ExecuteTail(siteID, deployKey string, env *Env, events []TailEvent) *WorkerResult {
	return e.backend.ExecuteTail(siteID, deployKey, env, events)
}

// ExecuteFunction calls a named exported function on the worker module.
func (e *Engine) ExecuteFunction(siteID, deployKey string, env *Env, fnName string, args ...any) *WorkerResult {
	return e.backend.ExecuteFunction(siteID, deployKey, env, fnName, args...)
}

// EnsureSource ensures the source for the given site/deploy is loaded.
func (e *Engine) EnsureSource(siteID, deployKey string) error {
	return e.backend.EnsureSource(siteID, deployKey)
}

// CompileAndCache compiles the source and caches the bytecode.
func (e *Engine) CompileAndCache(siteID, deployKey, source string) ([]byte, error) {
	return e.backend.CompileAndCache(siteID, deployKey, source)
}

// InvalidatePool marks the pool for the given site as invalid.
func (e *Engine) InvalidatePool(siteID, deployKey string) {
	e.backend.InvalidatePool(siteID, deployKey)
}

// Shutdown disposes of all pools and workers.
func (e *Engine) Shutdown() {
	e.backend.Shutdown()
}

// SetDispatcher sets the worker dispatcher for service bindings.
func (e *Engine) SetDispatcher(d WorkerDispatcher) {
	e.backend.SetDispatcher(d)
}

// MaxResponseBytes returns the configured max response body size.
func (e *Engine) MaxResponseBytes() int {
	return e.backend.MaxResponseBytes()
}
