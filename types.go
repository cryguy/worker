package worker

import (
	"time"
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
