package worker

import (
	"time"

	v8 "github.com/tommie/v8go"
)

// WorkerRequest represents an incoming HTTP request to a worker.
type WorkerRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// WorkerResponse represents the HTTP response from a worker.
type WorkerResponse struct {
	StatusCode   int
	Headers      map[string]string
	Body         []byte
	HasWebSocket bool // true when status is 101 and webSocket was set
}

// WorkerResult wraps a response with execution metadata.
type WorkerResult struct {
	Response  *WorkerResponse
	Logs      []LogEntry
	Error     error
	Duration  time.Duration
	WebSocket *WebSocketHandler // non-nil for WebSocket upgrade responses
	// Data holds the JSON-serialized return value from ExecuteFunction.
	// It is empty for Execute/ExecuteScheduled/ExecuteTail calls.
	Data string
}

// LogEntry is a single console.log/warn/error captured from a worker.
type LogEntry struct {
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// TailEvent represents a log event forwarded to a tail worker.
type TailEvent struct {
	ScriptName string     `json:"scriptName"`
	Logs       []LogEntry `json:"logs"`
	Exceptions []string   `json:"exceptions"`
	Outcome    string     `json:"outcome"`
	Timestamp  time.Time  `json:"timestamp"`
}

// EnvBindingFunc creates a JS value to be set on the worker's env object.
// It receives the V8 isolate and context for the current execution.
// Downstream users can use this to register custom bindings (objects, functions,
// etc.) that their worker scripts can access via env.<name>.
type EnvBindingFunc func(iso *v8.Isolate, ctx *v8.Context) (*v8.Value, error)

// Env holds all bindings passed to the worker as the second argument.
type Env struct {
	Vars    map[string]string
	Secrets map[string]string

	// Opt-in bindings — nil means disabled
	KV              map[string]KVStore
	Cache           CacheStore
	Storage         map[string]R2Store
	Queues          map[string]QueueSender
	D1Bindings      map[string]string // binding name -> database ID
	DurableObjects  map[string]DurableObjectStore
	ServiceBindings map[string]ServiceBindingConfig

	// CustomBindings allows downstream users to add arbitrary bindings
	// to the env object. Each function is called per-request and its
	// returned V8 value is set on env under the map key name.
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
