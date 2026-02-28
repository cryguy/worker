package webapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

const maxEventSources = 10
const maxSSEEvents = 1000

// sseEvent represents a single Server-Sent Event.
type sseEvent struct {
	Type string `json:"type"`
	Data string `json:"data"`
	ID   string `json:"id,omitempty"`
}

// eventSourceState holds state for a single SSE connection.
type eventSourceState struct {
	url        string
	events     []sseEvent
	mu         sync.Mutex
	closed     bool
	connected  bool
	resp       *http.Response
	body       io.ReadCloser
	cancelFunc func()
}

// eventSourceMap holds all SSE connections for a request, stored via the
// RequestState extension map under the key "eventSources".
type eventSourceMap struct {
	sources      map[string]*eventSourceState
	nextSourceID int64
}

// EventSourceSSRFEnabled controls SSRF protection for EventSource connections.
var EventSourceSSRFEnabled = true

// EventSourceTransport is the HTTP transport used for EventSource connections.
var EventSourceTransport http.RoundTripper = &http.Transport{
	DialContext: ssrfSafeDialContext,
}

// eventSourceJS defines the EventSource class as a JS polyfill that delegates
// network I/O to Go-backed helpers.
const eventSourceJS = `
(function() {
	var CONNECTING = 0;
	var OPEN = 1;
	var CLOSED = 2;

	class EventSource extends EventTarget {
		constructor(url, options) {
			super();
			if (typeof url !== 'string' && !(url instanceof URL)) {
				throw new TypeError('EventSource: url must be a string or URL');
			}
			this._url = typeof url === 'string' ? url : url.toString();
			this._withCredentials = !!(options && options.withCredentials);
			this._readyState = CONNECTING;
			this._sourceID = null;
			this._pollTimer = null;

			var reqID = globalThis.__requestID;
			try {
				this._sourceID = __eventSourceConnect(String(reqID), this._url);
				this._readyState = OPEN;
				this.dispatchEvent(new Event('open'));
				this._startPolling(String(reqID));
			} catch(e) {
				this._readyState = CLOSED;
				var errEvt = new Event('error');
				errEvt.message = e.message || String(e);
				this.dispatchEvent(errEvt);
			}
		}

		_startPolling(reqID) {
			var self = this;
			function poll() {
				if (self._readyState === CLOSED || !self._sourceID) return;
				try {
					var eventsJSON = __eventSourcePoll(reqID, self._sourceID);
					if (eventsJSON && eventsJSON !== '[]') {
						var events = JSON.parse(eventsJSON);
						for (var i = 0; i < events.length; i++) {
							var evt = events[i];
							if (evt.type === '__error__') {
								var errEvt = new Event('error');
								errEvt.message = evt.data;
								self.dispatchEvent(errEvt);
								self._readyState = CLOSED;
								return;
							}
							if (evt.type === '__done__') {
								self._readyState = CLOSED;
								return;
							}
							var me = new Event(evt.type || 'message');
							me.data = evt.data;
							me.lastEventId = evt.id || '';
							self.dispatchEvent(me);
						}
					}
				} catch(e) {}
				if (self._readyState !== CLOSED) {
					self._pollTimer = setTimeout(poll, 50);
				}
			}
			self._pollTimer = setTimeout(poll, 10);
		}

		get url() { return this._url; }
		get readyState() { return this._readyState; }
		get withCredentials() { return this._withCredentials; }

		set onopen(fn) {
			if (this._onopen) this.removeEventListener('open', this._onopen);
			this._onopen = fn;
			if (fn) this.addEventListener('open', fn);
		}
		get onopen() { return this._onopen || null; }

		set onmessage(fn) {
			if (this._onmessage) this.removeEventListener('message', this._onmessage);
			this._onmessage = fn;
			if (fn) this.addEventListener('message', fn);
		}
		get onmessage() { return this._onmessage || null; }

		set onerror(fn) {
			if (this._onerror) this.removeEventListener('error', this._onerror);
			this._onerror = fn;
			if (fn) this.addEventListener('error', fn);
		}
		get onerror() { return this._onerror || null; }

		close() {
			if (this._readyState === CLOSED) return;
			this._readyState = CLOSED;
			if (this._pollTimer) {
				clearTimeout(this._pollTimer);
				this._pollTimer = null;
			}
			if (this._sourceID) {
				try {
					var reqID = globalThis.__requestID;
					__eventSourceClose(String(reqID), this._sourceID);
				} catch(e) {}
			}
		}
	}

	EventSource.CONNECTING = CONNECTING;
	EventSource.OPEN = OPEN;
	EventSource.CLOSED = CLOSED;
	EventSource.prototype.CONNECTING = CONNECTING;
	EventSource.prototype.OPEN = OPEN;
	EventSource.prototype.CLOSED = CLOSED;

	globalThis.EventSource = EventSource;
})();
`

// getEventSourceMap retrieves the eventSourceMap from the request state
// extension map, creating it if necessary.
func getEventSourceMap(state *core.RequestState) *eventSourceMap {
	v := state.GetExt("eventSources")
	if v != nil {
		return v.(*eventSourceMap)
	}
	esm := &eventSourceMap{
		sources: make(map[string]*eventSourceState),
	}
	state.SetExt("eventSources", esm)
	return esm
}

// SetupEventSource registers Go-backed helpers for EventSource SSE and
// evaluates the JS wrapper.
func SetupEventSource(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __eventSourceConnect(reqIDStr, url) -> sourceID
	if err := rt.RegisterFunc("__eventSourceConnect", func(reqIDStr, rawURL string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)

		if EventSourceSSRFEnabled && IsPrivateHostname(rawURL) {
			return "", fmt.Errorf("EventSource: connection to private IP addresses is not allowed")
		}

		state := core.GetRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("EventSource: invalid request state")
		}

		esm := getEventSourceMap(state)
		if len(esm.sources) >= maxEventSources {
			return "", fmt.Errorf("EventSource: maximum connection limit reached")
		}

		esm.nextSourceID++
		sourceID := strconv.FormatInt(esm.nextSourceID, 10)

		es := &eventSourceState{
			url: rawURL,
		}
		esm.sources[sourceID] = es

		// Register cleanup so ClearRequestState closes all SSE connections.
		state.RegisterCleanup(func() {
			cleanupEventSources(state)
		})

		connCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		es.cancelFunc = cancel

		go func() {
			defer cancel()
			req, err := http.NewRequestWithContext(connCtx, "GET", rawURL, nil)
			if err != nil {
				es.mu.Lock()
				es.events = append(es.events, sseEvent{Type: "__error__", Data: err.Error()})
				es.closed = true
				es.mu.Unlock()
				return
			}
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Cache-Control", "no-cache")

			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: EventSourceTransport,
			}
			resp, err := client.Do(req)
			if err != nil {
				es.mu.Lock()
				es.events = append(es.events, sseEvent{Type: "__error__", Data: err.Error()})
				es.closed = true
				es.mu.Unlock()
				return
			}

			es.mu.Lock()
			es.resp = resp
			es.body = resp.Body
			es.connected = true
			es.mu.Unlock()

			parseSSEStream(es, resp)
		}()

		// Wait briefly for connection to establish or fail.
		time.Sleep(50 * time.Millisecond)

		es.mu.Lock()
		hasError := false
		for _, evt := range es.events {
			if evt.Type == "__error__" {
				hasError = true
				break
			}
		}
		es.mu.Unlock()

		if hasError {
			return "", fmt.Errorf("EventSource: connection failed")
		}

		return sourceID, nil
	}); err != nil {
		return err
	}

	// __eventSourcePoll(reqIDStr, sourceID) -> JSON array of events
	if err := rt.RegisterFunc("__eventSourcePoll", func(reqIDStr, sourceID string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return "[]", nil
		}
		esm := getEventSourceMap(state)
		es, ok := esm.sources[sourceID]
		if !ok {
			return "[]", nil
		}

		es.mu.Lock()
		events := es.events
		es.events = nil
		closed := es.closed
		es.mu.Unlock()

		if len(events) == 0 && closed {
			events = append(events, sseEvent{Type: "__done__"})
		}

		data, _ := json.Marshal(events)
		return string(data), nil
	}); err != nil {
		return err
	}

	// __eventSourceClose(reqIDStr, sourceID)
	if err := rt.RegisterFunc("__eventSourceClose", func(reqIDStr, sourceID string) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil {
			return
		}
		esm := getEventSourceMap(state)
		es, ok := esm.sources[sourceID]
		if !ok {
			return
		}
		closeEventSource(es)
		delete(esm.sources, sourceID)
	}); err != nil {
		return err
	}

	return rt.Eval(eventSourceJS)
}

// parseSSEStream reads SSE events from an HTTP response and queues them.
func parseSSEStream(es *eventSourceState, resp *http.Response) {
	defer func() {
		_ = resp.Body.Close()
		es.mu.Lock()
		es.closed = true
		es.mu.Unlock()
	}()

	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	var dataLines []string
	var lastID string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				if eventType == "" {
					eventType = "message"
				}
				evt := sseEvent{
					Type: eventType,
					Data: data,
					ID:   lastID,
				}
				es.mu.Lock()
				if len(es.events) >= maxSSEEvents {
					es.mu.Unlock()
					eventType = ""
					dataLines = nil
					continue
				}
				es.events = append(es.events, evt)
				es.mu.Unlock()
			}
			eventType = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, ":") {
			continue
		}

		var field, value string
		if idx := strings.Index(line, ":"); idx >= 0 {
			field = line[:idx]
			value = line[idx+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		} else {
			field = line
		}

		switch field {
		case "event":
			eventType = value
		case "data":
			dataLines = append(dataLines, value)
		case "id":
			lastID = value
		case "retry":
			// Ignore retry field.
		}
	}

	// Flush any remaining data.
	if len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		if eventType == "" {
			eventType = "message"
		}
		evt := sseEvent{
			Type: eventType,
			Data: data,
			ID:   lastID,
		}
		es.mu.Lock()
		if len(es.events) < maxSSEEvents {
			es.events = append(es.events, evt)
		}
		es.mu.Unlock()
	}
}

// closeEventSource closes an SSE connection and cleans up resources.
func closeEventSource(es *eventSourceState) {
	es.mu.Lock()
	defer es.mu.Unlock()

	es.closed = true
	if es.cancelFunc != nil {
		es.cancelFunc()
	}
	if es.body != nil {
		_ = es.body.Close()
	}
}

// cleanupEventSources closes all open event sources for a request.
func cleanupEventSources(state *core.RequestState) {
	v := state.GetExt("eventSources")
	if v == nil {
		return
	}
	esm := v.(*eventSourceMap)
	for id, es := range esm.sources {
		closeEventSource(es)
		delete(esm.sources, id)
	}
}
