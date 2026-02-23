package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"modernc.org/quickjs"
)

// setupTCPTestContext creates a VM with all APIs needed for TCP tests.
func setupTCPTestContext(t *testing.T) (*quickjs.VM, *eventLoop) {
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
		setupTCPSocket,
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

// TestTCPConnectGlobalExists verifies that connect() is registered as a global function.
func TestTCPConnectGlobalExists(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	result, err := evalBool(vm, `typeof connect === 'function'`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !result {
		t.Fatal("expected connect to be a global function")
	}
}

// TestTCPSocketSSRFBlocksLoopback verifies that connect() blocks connections
// to loopback addresses (127.0.0.1).
func TestTCPSocketSSRFBlocksLoopback(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	// Set up a request state so the JS side has __requestID.
	reqID := newRequestState(10, defaultEnv())
	if err := evalDiscard(vm, `globalThis.__requestID = "`+strconv.FormatUint(reqID, 10)+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}
	defer clearRequestState(reqID)

	result, err := evalString(vm, `(function() {
		try {
			connect("127.0.0.1:8080");
			return "not_blocked";
		} catch(e) {
			return e.message || String(e);
		}
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !strings.Contains(result, "private") {
		t.Fatalf("expected SSRF block for 127.0.0.1, got: %s", result)
	}
}

// TestTCPSocketSSRFBlocksLocalhost verifies that connect() blocks connections
// to "localhost".
func TestTCPSocketSSRFBlocksLocalhost(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	reqID := newRequestState(10, defaultEnv())
	if err := evalDiscard(vm, `globalThis.__requestID = "`+strconv.FormatUint(reqID, 10)+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}
	defer clearRequestState(reqID)

	result, err := evalString(vm, `(function() {
		try {
			connect("localhost:8080");
			return "not_blocked";
		} catch(e) {
			return e.message || String(e);
		}
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !strings.Contains(result, "private") {
		t.Fatalf("expected SSRF block for localhost, got: %s", result)
	}
}

// TestTCPSocketSSRFBlocksPrivateRanges verifies that connect() blocks connections
// to private IP ranges (10.x.x.x, 172.16.x.x, 192.168.x.x).
func TestTCPSocketSSRFBlocksPrivateRanges(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	reqID := newRequestState(10, defaultEnv())
	if err := evalDiscard(vm, `globalThis.__requestID = "`+strconv.FormatUint(reqID, 10)+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}
	defer clearRequestState(reqID)

	privateIPs := []string{"10.0.0.1:80", "172.16.0.1:80", "192.168.1.1:80"}
	for _, addr := range privateIPs {
		result, err := evalString(vm, `(function() {
			try {
				connect("`+addr+`");
				return "not_blocked";
			} catch(e) {
				return e.message || String(e);
			}
		})()`)
		if err != nil {
			t.Fatalf("eval for %s: %v", addr, err)
		}
		if !strings.Contains(result, "private") {
			t.Fatalf("expected SSRF block for %s, got: %s", addr, result)
		}
	}
}

// TestTCPCheckSSRFDirect tests the Go-level ssrfSafeTCPDial function directly.
func TestTCPCheckSSRFDirect(t *testing.T) {
	old := tcpSSRFEnabled
	tcpSSRFEnabled = true
	defer func() { tcpSSRFEnabled = old }()

	tests := []struct {
		hostname string
		blocked  bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"0.0.0.0", true},
		{"localhost", true},
		{"::1", true},
		// Public IPs should pass (will fail to connect, but not with "private" error).
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}

	for _, tt := range tests {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, err := ssrfSafeTCPDial(ctx, tt.hostname, "80")
		cancel()
		if tt.blocked && err == nil {
			t.Errorf("ssrfSafeTCPDial(%q) should have been blocked but was allowed", tt.hostname)
		}
		if tt.blocked && err != nil && !strings.Contains(err.Error(), "private") {
			t.Errorf("ssrfSafeTCPDial(%q) should block with 'private' error, got: %v", tt.hostname, err)
		}
		if !tt.blocked && err != nil && strings.Contains(err.Error(), "private") {
			t.Errorf("ssrfSafeTCPDial(%q) should have been allowed but was blocked: %v", tt.hostname, err)
		}
	}
}

// TestTCPSocketBuffer_Take tests the tcpSocketBuffer.take method directly.
func TestTCPSocketBuffer_Take(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()

	buf := &tcpSocketBuffer{conn: client}
	go buf.readLoop()

	// Write some data from the server side.
	testData := []byte("hello TCP")
	_, _ = server.Write(testData)

	// Poll for data availability with short sleeps.
	var data string
	var eof bool
	for i := 0; i < 50; i++ {
		data, eof, _ = buf.take(1024)
		if data != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if data == "" {
		t.Fatal("expected data from buffer, got empty string")
	}
	if eof {
		t.Fatal("unexpected EOF")
	}

	// Close server side and verify EOF.
	_ = server.Close()
	for i := 0; i < 50; i++ {
		_, eof, _ = buf.take(1024)
		if eof {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !eof {
		t.Fatal("expected EOF after server close")
	}
}

// disableTCPSSRF temporarily disables SSRF protection so tests can connect
// to loopback/private addresses. Restores the original value on cleanup.
func disableTCPSSRF(t *testing.T) {
	t.Helper()
	orig := tcpSSRFEnabled
	tcpSSRFEnabled = false
	t.Cleanup(func() {
		tcpSSRFEnabled = orig
	})
}

// TestTCPSocket_ConnectAndWrite verifies that connect() can establish a real
// TCP connection and write data through the writable stream.
func TestTCPSocket_ConnectAndWrite(t *testing.T) {
	disableTCPSSRF(t)

	// Start a local TCP server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			received <- ""
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		received <- string(buf[:n])
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	e := newTestEngine(t)

	source := fmt.Sprintf(`export default {
  async fetch(request, env) {
    var socket = connect("127.0.0.1:%d");
    var writer = socket.writable.getWriter();
    await writer.write(new TextEncoder().encode("hello TCP"));
    await writer.close();
    return Response.json({ ok: true });
  },
};`, port)

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct{ Ok bool }
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Ok {
		t.Fatalf("expected ok:true, got: %s", r.Response.Body)
	}

	select {
	case got := <-received:
		if got != "hello TCP" {
			t.Fatalf("server received %q, want %q", got, "hello TCP")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server to receive data")
	}
}

// TestTCPSocket_ConnectAndRead verifies that connect() can read data sent by
// the server through the readable stream.
func TestTCPSocket_ConnectAndRead(t *testing.T) {
	disableTCPSSRF(t)

	// Start a local TCP server that immediately writes data and closes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("server says hi"))
		_ = conn.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	e := newTestEngine(t)

	source := fmt.Sprintf(`export default {
  async fetch(request, env) {
    var socket = connect("127.0.0.1:%d");
    // Wait for Go's readLoop to receive the data.
    await scheduler.wait(200);
    var reader = socket.readable.getReader();
    var result = await reader.read();
    var text = "";
    if (result.value) {
      text = new TextDecoder().decode(result.value);
    }
    return Response.json({ text: text });
  },
};`, port)

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct{ Text string }
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(data.Text, "server says hi") {
		t.Fatalf("expected %q in text, got: %q", "server says hi", data.Text)
	}
}

// TestTCPSocket_ReadWithoutExplicitWait is the critical regression test.
// It verifies that JS code can read TCP data WITHOUT using scheduler.wait().
func TestTCPSocket_ReadWithoutExplicitWait(t *testing.T) {
	disableTCPSSRF(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Server writes data after a small delay, then closes the connection.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = conn.Write([]byte("no-wait-needed"))
		_ = conn.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	e := newTestEngine(t)

	// KEY: No scheduler.wait() anywhere — the read should block internally
	source := fmt.Sprintf(`export default {
  async fetch(request, env) {
    var socket = connect("127.0.0.1:%d");
    var reader = socket.readable.getReader();
    var result = await reader.read();
    var text = "";
    if (result.value) {
      text = new TextDecoder().decode(result.value);
    }
    return Response.json({ text: text, done: result.done });
  },
};`, port)

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Text != "no-wait-needed" {
		t.Fatalf("expected %q, got %q", "no-wait-needed", data.Text)
	}
}

func TestTCPConnect_MissingArgs(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	// __tcpConnect with too few arguments should throw
	result, err := evalString(vm, `(function() {
		try {
			__tcpConnect("1", "host");
			return "no_error";
		} catch(e) {
			return "error:" + String(e);
		}
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !strings.Contains(result, "error:") {
		t.Errorf("expected error for missing args, got: %s", result)
	}
}

func TestTCPRead_MissingArgs(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	result, err := evalString(vm, `(function() {
		try {
			__tcpRead("1");
			return "no_error";
		} catch(e) {
			return "error:" + String(e);
		}
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !strings.Contains(result, "error:") {
		t.Errorf("expected error for missing args, got: %s", result)
	}
}

func TestTCPRead_UnknownSocketID(t *testing.T) {
	vm, _ := setupTCPTestContext(t)

	reqID := newRequestState(10, defaultEnv())
	defer clearRequestState(reqID)
	if err := evalDiscard(vm, `globalThis.__requestID = "`+strconv.FormatUint(reqID, 10)+`"`); err != nil {
		t.Fatalf("set __requestID: %v", err)
	}

	result, err := evalString(vm, `(function() {
		try {
			__tcpRead("`+strconv.FormatUint(reqID, 10)+`", "unknown_socket", 4096);
			return "no_error";
		} catch(e) {
			return "error:" + String(e);
		}
	})()`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if !strings.Contains(result, "unknown socket") {
		t.Errorf("expected unknown socket error, got: %s", result)
	}
}
