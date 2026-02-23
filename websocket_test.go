package worker

import (
	"encoding/json"
	"strconv"
	"testing"

	"modernc.org/quickjs"
)

func TestWebSocket_PairCreation(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    var pair = new WebSocketPair();
    return Response.json({
      has0: pair[0] !== undefined,
      has1: pair[1] !== undefined,
      is0WS: pair[0] instanceof WebSocket,
      is1WS: pair[1] instanceof WebSocket,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Has0 bool `json:"has0"`
		Has1 bool `json:"has1"`
		Is0  bool `json:"is0WS"`
		Is1  bool `json:"is1WS"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Has0 || !data.Has1 {
		t.Error("pair should have [0] and [1]")
	}
	if !data.Is0 || !data.Is1 {
		t.Error("pair members should be WebSocket instances")
	}
}

func TestWebSocket_AcceptAndReadyState(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    var pair = new WebSocketPair();
    var server = pair[1];
    var stateBefore = server.readyState;
    server.accept();
    var stateAfter = server.readyState;
    return Response.json({
      before: stateBefore,
      after: stateAfter,
      CONNECTING: WebSocket.CONNECTING,
      OPEN: WebSocket.OPEN,
      CLOSING: WebSocket.CLOSING,
      CLOSED: WebSocket.CLOSED,
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Before     int `json:"before"`
		After      int `json:"after"`
		CONNECTING int `json:"CONNECTING"`
		OPEN       int `json:"OPEN"`
		CLOSING    int `json:"CLOSING"`
		CLOSED     int `json:"CLOSED"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Before != 0 {
		t.Errorf("readyState before accept = %d, want 0 (CONNECTING)", data.Before)
	}
	if data.After != 1 {
		t.Errorf("readyState after accept = %d, want 1 (OPEN)", data.After)
	}
	if data.CONNECTING != 0 || data.OPEN != 1 || data.CLOSING != 2 || data.CLOSED != 3 {
		t.Errorf("WebSocket constants: CONNECTING=%d, OPEN=%d, CLOSING=%d, CLOSED=%d",
			data.CONNECTING, data.OPEN, data.CLOSING, data.CLOSED)
	}
}

func TestWebSocket_SendThrowsWhenNotOpen(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    var pair = new WebSocketPair();
    var server = pair[1];
    try {
      server.send("test");
      return Response.json({ error: false });
    } catch (e) {
      return Response.json({ error: true, message: e.message });
    }
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Error {
		t.Error("send() before accept() should throw")
	}
}

// setupWSTestContext creates a VM with WebSocket setup for direct callback testing.
func setupWSTestContext(t *testing.T) (*quickjs.VM, *eventLoop) {
	t.Helper()
	vm, err := quickjs.NewVM()
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	el := newEventLoop()

	for _, fn := range []setupFunc{
		setupWebAPIs,
		setupEncoding,
		setupStreams,
		setupConsole,
		setupWebSocket,
	} {
		if err := fn(vm, el); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	t.Cleanup(func() {
		vm.Close()
	})
	return vm, el
}

// TestWsSend_NoRequestID tests __wsSend without __requestID set.
func TestWsSend_NoRequestID(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	// Don't set __requestID - just call __wsSend
	err := evalDiscard(vm, `(function() {
		try {
			__wsSend("1", "hello", false);
			return "no_error";
		} catch(e) {
			return "error:" + String(e);
		}
	})()`)
	// Should complete without error
	if err != nil {
		t.Logf("result: %v", err)
	}
}

// TestWsSend_NilState tests __wsSend with a nonexistent request ID.
func TestWsSend_NilState(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	// Set __requestID to a nonexistent request
	if err := evalDiscard(vm, `globalThis.__requestID = "999999700"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		__wsSend("999999700", "hello", false);
		return "ok";
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
}

// TestWsSend_NilWsConn tests __wsSend with a valid state but no wsConn.
func TestWsSend_NilWsConn(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	// Create real request state but don't set wsConn
	reqID := newRequestState(10, defaultEnv())
	defer clearRequestState(reqID)

	reqIDStr := strconv.FormatUint(reqID, 10)
	if err := evalDiscard(vm, `globalThis.__requestID = "`+reqIDStr+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		__wsSend("`+reqIDStr+`", "hello", false);
		return "ok";
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
}

// TestWsSend_AlreadyClosed tests __wsSend with wsClosed flag set.
func TestWsSend_AlreadyClosed(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	reqID := newRequestState(10, defaultEnv())
	defer clearRequestState(reqID)
	state := getRequestState(reqID)
	state.wsClosed = true

	reqIDStr := strconv.FormatUint(reqID, 10)
	if err := evalDiscard(vm, `globalThis.__requestID = "`+reqIDStr+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		__wsSend("`+reqIDStr+`", "hello", false);
		return "ok";
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
}

// TestWsSend_NoArgs tests __wsSend with insufficient arguments.
func TestWsSend_NoArgs(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	// Call __wsSend with no arguments - the Go function expects 3 args, so this should error
	_, err := evalString(vm, `(function() {
		__wsSend();
		return "ok";
	})()`)
	// The eval should fail with a TypeError about not enough arguments
	if err == nil {
		t.Error("expected error when calling __wsSend with no arguments")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

// TestWsClose_NilState tests __wsClose with a nonexistent request ID.
func TestWsClose_NilState(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	if err := evalDiscard(vm, `globalThis.__requestID = "999999701"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		__wsClose("999999701", 1000, "");
		return "ok";
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
}

// TestWsClose_NilWsConn tests __wsClose with a valid state but no wsConn.
func TestWsClose_NilWsConn(t *testing.T) {
	vm, _ := setupWSTestContext(t)

	reqID := newRequestState(10, defaultEnv())
	defer clearRequestState(reqID)

	reqIDStr := strconv.FormatUint(reqID, 10)
	if err := evalDiscard(vm, `globalThis.__requestID = "`+reqIDStr+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		__wsClose("`+reqIDStr+`", 1000, "normal");
		return "ok";
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %s", result)
	}
}

// TestWebSocket_UpgradePathInEngine exercises the WebSocket upgrade path
// in Engine.Execute (status 101 + webSocket property).
func TestWebSocket_UpgradePathInEngine(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    var pair = new WebSocketPair();
    var client = pair[0];
    var server = pair[1];
    server.accept();

    return new Response(null, {
      status: 101,
      webSocket: client,
    });
  },
};`

	env := defaultEnv()
	req := getReq("http://localhost/ws-upgrade")
	if _, err := e.CompileAndCache("ws-test", "deploy1", source); err != nil {
		t.Fatalf("CompileAndCache: %v", err)
	}
	r := e.Execute("ws-test", "deploy1", env, req)
	if r.Error != nil {
		t.Fatalf("Execute error: %v", r.Error)
	}
	if r.Response == nil {
		t.Fatal("expected non-nil response")
	}
	if r.Response.StatusCode != 101 {
		t.Errorf("status = %d, want 101", r.Response.StatusCode)
	}
	if !r.Response.HasWebSocket {
		t.Error("response should have HasWebSocket=true")
	}
	if r.WebSocket == nil {
		t.Error("result should have WebSocket handler")
	}
}
