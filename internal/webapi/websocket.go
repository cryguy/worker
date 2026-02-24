package webapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/coder/websocket"
	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// WsConnectionTimeout is the maximum duration for a WebSocket connection.
const WsConnectionTimeout = 5 * time.Minute

// MaxWSMessageBytes is the maximum size of a single WebSocket message (64 KB).
const MaxWSMessageBytes = 64 * 1024

// webSocketJS defines the WebSocket and WebSocketPair classes available to workers.
const webSocketJS = `
(function() {

class WebSocket {
	constructor(url) {
		this._listeners = {};
		this._readyState = 0;
		this._url = url || '';
		this._protocol = '';
		this._extensions = '';
		this._peer = null;
		this.binaryType = 'arraybuffer';
	}

	accept() {
		this._readyState = 1;
	}

	send(data) {
		if (this._readyState !== 1) {
			throw new DOMException('WebSocket is not open', 'InvalidStateError');
		}
		if (!this._isHTTPBridged && this._peer && this._peer._readyState < 2) {
			var peer = this._peer;
			var evt;
			if (typeof data === 'string') {
				evt = { data: data };
			} else if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) {
				var buf = data instanceof ArrayBuffer ? data : data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength);
				evt = { data: buf.slice(0) };
			} else {
				evt = { data: String(data) };
			}
			queueMicrotask(function() {
				peer._dispatch('message', evt);
			});
			return;
		}
		var reqID = String(globalThis.__requestID);
		if (typeof data === 'string') {
			__wsSend(reqID, data, false);
		} else if (data instanceof ArrayBuffer) {
			__wsSend(reqID, __bufferSourceToB64(data), true);
		} else if (ArrayBuffer.isView(data)) {
			__wsSend(reqID, __bufferSourceToB64(data), true);
		} else {
			__wsSend(reqID, String(data), false);
		}
	}

	close(code, reason) {
		if (this._readyState >= 2) return;
		this._readyState = 2;
		if (!this._isHTTPBridged && this._peer && this._peer._readyState < 2) {
			var peer = this._peer;
			var closeCode = code || 1000;
			var closeReason = reason || '';
			queueMicrotask(function() {
				peer._readyState = 3;
				peer._dispatch('close', { code: closeCode, reason: closeReason, wasClean: true });
			});
		}
		if (this._isHTTPBridged) {
			var reqID = String(globalThis.__requestID);
			__wsClose(reqID, code || 1000, reason || '');
		}
		this._readyState = 3;
		this._dispatch('close', { code: code || 1000, reason: reason || '', wasClean: true });
	}

	addEventListener(type, handler) {
		if (!this._listeners[type]) this._listeners[type] = [];
		this._listeners[type].push(handler);
	}

	removeEventListener(type, handler) {
		var list = this._listeners[type];
		if (!list) return;
		this._listeners[type] = list.filter(function(h) { return h !== handler; });
	}

	_dispatch(type, event) {
		var prop = 'on' + type;
		if (typeof this[prop] === 'function') {
			this[prop](event);
		}
		var list = this._listeners[type] || [];
		for (var i = 0; i < list.length; i++) {
			list[i](event);
		}
	}

	get readyState() { return this._readyState; }
	get url() { return this._url; }
	get protocol() { return this._protocol; }
	get extensions() { return this._extensions; }
}

WebSocket.CONNECTING = 0;
WebSocket.OPEN = 1;
WebSocket.CLOSING = 2;
WebSocket.CLOSED = 3;

class WebSocketPair {
	constructor() {
		var ws0 = new WebSocket();
		var ws1 = new WebSocket();
		ws0._peer = ws1;
		ws1._peer = ws0;
		this[0] = ws0;
		this[1] = ws1;
	}
}

WebSocketPair.prototype[Symbol.iterator] = function() {
	return [this[0], this[1]][Symbol.iterator]();
};

globalThis.WebSocket = WebSocket;
globalThis.WebSocketPair = WebSocketPair;

})();
`

// SetupWebSocket registers the WebSocket/WebSocketPair JS classes and the
// Go-backed __wsSend/__wsClose functions that bridge to the HTTP WebSocket.
func SetupWebSocket(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __wsSend(reqIDStr, data, isBinary) — sends a message to the HTTP WebSocket client.
	if err := rt.RegisterFunc("__wsSend", func(reqIDStr, data string, isBinary bool) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.WsConn == nil {
			return
		}

		wsConn, ok := state.WsConn.(*websocket.Conn)
		if !ok {
			return
		}

		state.WsMu.Lock()
		defer state.WsMu.Unlock()
		if state.WsClosed {
			return
		}

		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if isBinary {
			decoded, decErr := base64.StdEncoding.DecodeString(data)
			if decErr != nil {
				log.Printf("worker: ws send base64 decode error: %v", decErr)
				return
			}
			if writeErr := wsConn.Write(writeCtx, websocket.MessageBinary, decoded); writeErr != nil {
				log.Printf("worker: ws send error: %v", writeErr)
			}
		} else {
			if writeErr := wsConn.Write(writeCtx, websocket.MessageText, []byte(data)); writeErr != nil {
				log.Printf("worker: ws send error: %v", writeErr)
			}
		}
	}); err != nil {
		return err
	}

	// __wsClose(reqIDStr, code, reason) — closes the HTTP WebSocket connection.
	if err := rt.RegisterFunc("__wsClose", func(reqIDStr string, code int, reason string) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.WsConn == nil {
			return
		}

		wsConn, ok := state.WsConn.(*websocket.Conn)
		if !ok {
			return
		}

		state.WsMu.Lock()
		defer state.WsMu.Unlock()
		if !state.WsClosed {
			state.WsClosed = true
			_ = wsConn.Close(websocket.StatusCode(code), reason)
		}
	}); err != nil {
		return err
	}

	return rt.Eval(webSocketJS)
}

// WebSocketHandler holds references needed for WebSocket bridging after
// the initial fetch handler returns a 101 response with a webSocket.
type WebSocketHandler struct {
	// Runtime is the JSRuntime for evaluating JS during the WebSocket bridge.
	Runtime core.JSRuntime
	// Loop is the event loop for draining pending operations.
	Loop *eventloop.EventLoop
	// ReqID is the request ID associated with this WebSocket connection.
	ReqID uint64
	// Timeout is the maximum duration for the WebSocket connection.
	Timeout time.Duration
	// OnComplete is called when the bridge finishes. The engine uses this
	// to return the worker to its pool and clean up request state.
	OnComplete func()
}

// wsMessage holds a single WebSocket message read from the HTTP connection.
type wsMessage struct {
	typ  websocket.MessageType
	data []byte
}

// Bridge starts the WebSocket message bridge between the HTTP connection
// and the JS runtime. This method blocks until the WebSocket connection
// closes or the timeout is reached.
func (wsh *WebSocketHandler) Bridge(ctx context.Context, httpConn *websocket.Conn) {
	rt := wsh.Runtime

	defer func() {
		// Dispatch close event to the server WebSocket.
		_ = rt.Eval(`
			if (globalThis.__ws_active_server) {
				globalThis.__ws_active_server._dispatch('close', {
					code: 1000, reason: '', wasClean: true
				});
				delete globalThis.__ws_active_server;
			}
		`)
		// Microtask checkpoint.
		rt.RunMicrotasks()

		// Let the engine clean up (return worker to pool, clear request state).
		if wsh.OnComplete != nil {
			wsh.OnComplete()
		}
	}()

	state := core.GetRequestState(wsh.ReqID)
	if state == nil {
		return
	}
	state.WsConn = httpConn

	// Apply message size limit.
	httpConn.SetReadLimit(MaxWSMessageBytes)

	// Reader goroutine: reads from HTTP WebSocket into a channel.
	incoming := make(chan wsMessage, 64)
	go func() {
		defer close(incoming)
		for {
			msgType, data, err := httpConn.Read(ctx)
			if err != nil {
				return
			}
			select {
			case incoming <- wsMessage{typ: msgType, data: data}:
			case <-ctx.Done():
				return
			}
		}
	}()

	connDeadline := time.After(wsh.Timeout)
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case msg, ok := <-incoming:
			if !ok {
				return
			}
			if msg.typ == websocket.MessageBinary {
				b64 := base64.StdEncoding.EncodeToString(msg.data)
				js := fmt.Sprintf(`(function() {
					var b64 = %s;
					var binary = __b64ToBuffer(b64);
					if (globalThis.__ws_active_server) {
						globalThis.__ws_active_server._dispatch('message', { data: binary });
					}
				})();`, core.JsEscape(b64))
				_ = rt.Eval(js)
			} else {
				js := fmt.Sprintf(`(function() {
					var data = %s;
					if (globalThis.__ws_active_server) {
						globalThis.__ws_active_server._dispatch('message', { data: data });
					}
				})();`, core.JsEscape(string(msg.data)))
				_ = rt.Eval(js)
			}
			rt.RunMicrotasks()

			if wsh.Loop.HasPending() {
				deadline := time.Now().Add(50 * time.Millisecond)
				wsh.Loop.Drain(rt, deadline)
			}

		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := httpConn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}

		case <-connDeadline:
			return

		case <-ctx.Done():
			return
		}
	}
}
