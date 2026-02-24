package core

import (
	"context"
	"fmt"
	"hash"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const MaxLogEntries = 1000
const MaxLogMessageSize = 4096

// CryptoKeyEntry holds imported key material and its associated hash algorithm.
type CryptoKeyEntry struct {
	Data        []byte // Raw key bytes (symmetric keys)
	HashAlgo    string // Associated hash algorithm
	AlgoName    string // Algorithm name (HMAC, AES-GCM, ECDSA, AES-CBC)
	KeyType     string // "secret", "public", "private"
	NamedCurve  string // For EC keys: "P-256", "P-384"
	EcKey       any    // *ecdsa.PrivateKey or *ecdsa.PublicKey
	Extractable bool   // WebCrypto extractable flag
}

// RequestState holds per-request mutable state (logs, fetch counter, env, crypto keys).
// The engine sets it before calling into JS and clears it after.
type RequestState struct {
	Logs       []LogEntry
	FetchCount int
	MaxFetches int
	Env        *Env
	CryptoKeys map[int]*CryptoKeyEntry
	NextKeyID  int

	// WebSocket bridge state (set when status 101 response is returned).
	// Typed as any to avoid importing coder/websocket in core.
	WsConn   any // *websocket.Conn
	WsMu     sync.Mutex
	WsClosed bool

	// DigestStream state: per-request hash instances keyed by stream ID.
	DigestStreams map[string]hash.Hash
	NextDigestID int64

	// TCP socket state: per-request TCP connections keyed by socket ID.
	TcpSockets      map[string]net.Conn
	NextTCPSocketID int64

	// In-flight fetch cancellation: maps fetchID -> cancel function.
	FetchCancels map[string]context.CancelFunc
	NextFetchID  int64

	// Extension storage for webapi packages. Each package stores its own
	// typed state using well-known string keys (e.g. "eventSources",
	// "compressStreams", "tcpSocketBuffers", "d1Bridges").
	extMu    sync.Mutex
	ext      map[string]any
	cleanups []func()
}

// SetExt stores a value in the extension map under the given key.
func (rs *RequestState) SetExt(key string, val any) {
	rs.extMu.Lock()
	if rs.ext == nil {
		rs.ext = make(map[string]any)
	}
	rs.ext[key] = val
	rs.extMu.Unlock()
}

// GetExt retrieves a value from the extension map.
func (rs *RequestState) GetExt(key string) any {
	rs.extMu.Lock()
	defer rs.extMu.Unlock()
	if rs.ext == nil {
		return nil
	}
	return rs.ext[key]
}

// RegisterCleanup adds a cleanup function to be called when the request state
// is cleared. Cleanups are called in reverse registration order.
func (rs *RequestState) RegisterCleanup(fn func()) {
	rs.extMu.Lock()
	rs.cleanups = append(rs.cleanups, fn)
	rs.extMu.Unlock()
}

var (
	requestCounter atomic.Uint64
	requestStates  sync.Map // uint64 -> *RequestState
)

// NewRequestState creates a new request state and returns its unique ID.
func NewRequestState(maxFetches int, env *Env) uint64 {
	id := requestCounter.Add(1)
	requestStates.Store(id, &RequestState{
		MaxFetches: maxFetches,
		Env:        env,
	})
	return id
}

// GetRequestState returns the state for the given request ID, or nil.
func GetRequestState(id uint64) *RequestState {
	v, ok := requestStates.Load(id)
	if !ok {
		return nil
	}
	return v.(*RequestState)
}

// ClearRequestState removes the state for the given request ID and returns it.
// It cleans up all per-request resources by running registered cleanup
// functions, cancelling in-flight fetches, and closing TCP sockets.
func ClearRequestState(id uint64) *RequestState {
	v, ok := requestStates.LoadAndDelete(id)
	if !ok {
		return nil
	}
	state := v.(*RequestState)

	// Run registered cleanups in reverse order (webapi-specific resources:
	// EventSources, compression streams, D1 bridges, etc.)
	state.extMu.Lock()
	cleanups := state.cleanups
	state.cleanups = nil
	state.extMu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	// Clean up TCP sockets.
	for _, conn := range state.TcpSockets {
		_ = conn.Close()
	}
	state.TcpSockets = nil

	// Cancel in-flight fetches.
	for _, cancel := range state.FetchCancels {
		cancel()
	}
	state.FetchCancels = nil

	return state
}

// ImportCryptoKey stores key material scoped to the request and returns its ID.
func ImportCryptoKey(reqID uint64, hashAlgo string, data []byte) int {
	state := GetRequestState(reqID)
	if state == nil {
		return -1
	}
	state.NextKeyID++
	id := state.NextKeyID
	if state.CryptoKeys == nil {
		state.CryptoKeys = make(map[int]*CryptoKeyEntry)
	}
	state.CryptoKeys[id] = &CryptoKeyEntry{Data: data, HashAlgo: hashAlgo, Extractable: true}
	return id
}

// ImportCryptoKeyFull stores a complete CryptoKeyEntry and returns its ID.
func ImportCryptoKeyFull(reqID uint64, entry *CryptoKeyEntry) int {
	state := GetRequestState(reqID)
	if state == nil {
		return -1
	}
	state.NextKeyID++
	id := state.NextKeyID
	if state.CryptoKeys == nil {
		state.CryptoKeys = make(map[int]*CryptoKeyEntry)
	}
	state.CryptoKeys[id] = entry
	return id
}

// GetCryptoKey retrieves key material scoped to the request.
func GetCryptoKey(reqID uint64, keyID int) *CryptoKeyEntry {
	state := GetRequestState(reqID)
	if state == nil {
		return nil
	}
	if state.CryptoKeys == nil {
		return nil
	}
	return state.CryptoKeys[keyID]
}

// AddLog appends a log entry to the request state identified by id.
func AddLog(id uint64, level, message string) {
	state := GetRequestState(id)
	if state == nil {
		return
	}
	if len(state.Logs) >= MaxLogEntries {
		return
	}
	if len(message) > MaxLogMessageSize {
		message = message[:MaxLogMessageSize] + "...(truncated)"
	}
	state.Logs = append(state.Logs, LogEntry{
		Level:   level,
		Message: message,
		Time:    time.Now(),
	})
}

// RegisterFetchCancel stores a cancel function for an in-flight fetch and
// returns the unique fetchID string key.
func RegisterFetchCancel(reqID uint64, cancel context.CancelFunc) string {
	state := GetRequestState(reqID)
	if state == nil {
		return ""
	}
	state.NextFetchID++
	id := strconv.FormatInt(state.NextFetchID, 10)
	if state.FetchCancels == nil {
		state.FetchCancels = make(map[string]context.CancelFunc)
	}
	state.FetchCancels[id] = cancel
	return id
}

// RemoveFetchCancel removes and returns the cancel function for a fetch.
func RemoveFetchCancel(reqID uint64, fetchID string) context.CancelFunc {
	state := GetRequestState(reqID)
	if state == nil || state.FetchCancels == nil {
		return nil
	}
	cancel := state.FetchCancels[fetchID]
	delete(state.FetchCancels, fetchID)
	return cancel
}

// CallFetchCancel calls the cancel function for the given fetch, if present.
func CallFetchCancel(reqID uint64, fetchID string) {
	if cancel := RemoveFetchCancel(reqID, fetchID); cancel != nil {
		cancel()
	}
}

// ParseReqID parses a request ID string to uint64.
func ParseReqID(s string) uint64 {
	if s == "" || s == "undefined" {
		return 0
	}
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		// Try fmt.Sscanf as fallback for compatibility.
		var n uint64
		fmt.Sscanf(s, "%d", &n)
		return n
	}
	return id
}

// BoolToInt converts a bool to 1 (true) or 0 (false) for JS interop,
// since some JS engines cannot marshal Go bool return values directly.
func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// JsEscape escapes a string for safe embedding in JavaScript source code.
func JsEscape(s string) string {
	return strconv.Quote(s)
}
