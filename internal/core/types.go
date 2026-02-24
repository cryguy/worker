package core

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// WebSocketBridger is implemented by engine-specific WebSocket handlers.
// Consumers call Bridge to relay messages between an HTTP WebSocket
// connection and the JS runtime.
type WebSocketBridger interface {
	Bridge(ctx context.Context, httpConn *websocket.Conn)
}

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
	WebSocket WebSocketBridger // engine-specific WebSocket handler
	Data      string // JSON-serialized return value from ExecuteFunction
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
