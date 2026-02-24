package core

// EngineBackend is the interface that engine implementations (QuickJS, V8)
// must satisfy. The root worker.Engine facade delegates to one of these
// based on build tags.
type EngineBackend interface {
	Execute(siteID, deployKey string, env *Env, req *WorkerRequest) *WorkerResult
	ExecuteScheduled(siteID, deployKey string, env *Env, cron string) *WorkerResult
	ExecuteTail(siteID, deployKey string, env *Env, events []TailEvent) *WorkerResult
	ExecuteFunction(siteID, deployKey string, env *Env, fnName string, args ...any) *WorkerResult
	EnsureSource(siteID, deployKey string) error
	CompileAndCache(siteID, deployKey string, source string) ([]byte, error)
	InvalidatePool(siteID, deployKey string)
	Shutdown()
	SetDispatcher(d WorkerDispatcher)
	MaxResponseBytes() int
}
