package core

// EnvBindingFunc creates a JS binding to be set on the worker's env object.
// It receives the JSRuntime for the current execution. The returned value
// is a basic Go type (string, int, float64, bool, nil) that is set on the
// env object via SetGlobal. For complex objects, use rt.Eval() to construct
// them in JS-land and return nil.
type EnvBindingFunc func(rt JSRuntime) (any, error)

// Env holds all bindings passed to the worker as the second argument.
type Env struct {
	Vars    map[string]string
	Secrets map[string]string

	// Opt-in bindings — nil means disabled
	KV              map[string]KVStore
	Cache           CacheStore
	Storage         map[string]R2Store
	Queues          map[string]QueueSender
	D1Bindings      map[string]string   // binding name -> database ID
	D1              map[string]D1Store  // binding name -> D1Store (opened by engine)
	DurableObjects  map[string]DurableObjectStore
	ServiceBindings map[string]ServiceBindingConfig

	// CustomBindings allows downstream users to add arbitrary bindings
	// to the env object. Each function is called per-request and its
	// returned value is set on env under the map key name.
	CustomBindings map[string]EnvBindingFunc

	// D1 configuration
	D1DataDir string

	// Runtime references
	Assets     AssetsFetcher
	Dispatcher WorkerDispatcher // set by Engine before execution
	SiteID     string           // site isolation key
}

// AssetsFetcher is implemented by the static pipeline to handle env.ASSETS.fetch().
type AssetsFetcher interface {
	Fetch(req *WorkerRequest) (*WorkerResponse, error)
}

// ServiceBindingConfig identifies a target worker for service bindings.
type ServiceBindingConfig struct {
	TargetSiteID    string
	TargetDeployKey string
}
